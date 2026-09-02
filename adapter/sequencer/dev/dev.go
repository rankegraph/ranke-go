// package: adapter/sequencer/dev / testkit
// type:    adapter
// job:     a blocking reference Sequencer for tests and development — the sole writer that
// advances a Ranke-Archive by driving the paper's seven steps one contribution at a time
// limits:  a merge holds the lock end to end, so callers queue; manages named branches
// without propagating between them (paper 2's cross-branch merge); mints from the
// injected Clock. NOT for production (-> adapter/sequencer/concurrent).
package dev

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rankegraph/ranke-go"
)

var (
	errNilArg            = errors.New("dev.NewSequencer: nil argument")
	errNoSigningKey      = errors.New("dev.NewSequencer: self contributor carries no signing key")
	errNoBookmarks       = errors.New("dev.NewSequencer: universe holds no 𝒰_hist to bookmark the head in")
	errNewSequencer      = errors.New("dev.NewSequencer")
	errSequencer         = errors.New("dev.Sequencer")
	errSealed            = errors.New("dev.Contribution: already sealed")
	errNoBranch          = errors.New("dev.Contribution: claims must name the branch they join")
	errNilClaim          = errors.New("dev.Contribution.AddClaims: nil claim")
	errEmptyContribution = errors.New("dev.Contribution.CompleteAndVerify: nothing was added")
	errForeign           = errors.New("dev.Sequencer.Merge: contribution came from another Sequencer")
)

var _ ranke.Sequencer = (*Sequencer)(nil)

// Clock is the deterministic time source for the claims the Sequencer mints,
// local to this adapter so it depends only on ranke. Tick returns now, then
// advances; one Clock shared with the caller keeps a whole run monotonic.
type Clock interface {
	Tick() time.Time
}

// Sequencer is the reference Ranke-Archive write path (RankeDB §Sequencer): the
// single writer advancing the head k → k′ by merging a contribution, running the
// seven steps serially in one blocking AddClaims call. Safe for concurrent use, bought
// by queueing — a merge runs alone (-> adapter/sequencer/concurrent for parallel).
type Sequencer struct {
	u     ranke.Universe
	marks *ranke.Bookmarks
	self  ranke.Contributor
	clock Clock

	// mu guards head and heads, and holds across a whole Merge: two merges sharing a
	// read of one branch head would both fold from it, and one would be lost.
	mu    sync.Mutex
	head  ranke.Id            // current archive head k (a contribution/branches claim)
	heads map[string]ranke.Id // current consolidated head per branch name (empty until first add)

	// cmu serialises clock.Tick on its own, so an injected Clock needs no locking of
	// its own and a Tick outside mu (step 3's consolidation) is still safe.
	cmu sync.Mutex
}

// tick reads the next timestamp. Every mint goes through here, so one unsynchronised
// Clock serves however many goroutines the caller drives this Sequencer from.
func (s *Sequencer) tick() time.Time {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	return s.clock.Tick()
}

// NewSequencer bootstraps over u: it stores the key-carrying self contributor and
// appends the empty contribution/branches claim whose id is the head k₀ to the list
// loc names, at its NEXT FREE slot — so over a list that already holds entries, a
// second and empty head lands above the archive there (foundation §Ranke-Archive).
// A reproducible run passes a seed of its own, keeping "same (seed, spec) →
// identical ids" true of the bookmark slots too.
func NewSequencer(ctx context.Context, u ranke.Universe, loc ranke.BookmarkLocator, self ranke.Contributor, clock Clock) (*Sequencer, error) {
	if u == nil || self == nil || clock == nil {
		return nil, errNilArg
	}
	if self.SigningKey() == nil {
		return nil, errNoSigningKey
	}
	// Refused here rather than at the first append: a Sequencer that cannot record
	// its head would advance an archive nobody can reach again.
	if !u.Capabilities().Bookmarks {
		return nil, errors.Join(errNoBookmarks, ranke.ErrUnsupported)
	}
	marks, err := loc.Open(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("%w: open bookmark list: %w", errNewSequencer, err)
	}
	s := &Sequencer{u: u, marks: marks, self: self, clock: clock, heads: map[string]ranke.Id{}}

	// Store the self contributor so branch-table claims attributed to it resolve.
	if err := s.u.PutClaims(ctx, []ranke.Claim{self}); err != nil {
		return nil, fmt.Errorf("%w: store contributor: %w", errNewSequencer, err)
	}
	// Empty branch table → archive head k₀, bookmark index 0.
	bt0, err := s.mintBranchTable(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := s.marks.Append(ctx, self, bt0.ID()); err != nil {
		return nil, fmt.Errorf("%w: bookmark k₀: %w", errNewSequencer, err)
	}
	s.head = bt0.ID()
	// Index k₀, so a layer answering membership from its own index holds the operator,
	// which sits on the spine.
	if err := s.u.Tag(ctx, s.head); err != nil {
		return nil, fmt.Errorf("%w: tag: %w", errNewSequencer, err)
	}
	return s, nil
}

// GetContributor returns the contributor the Sequencer signs branch advances with.
func (s *Sequencer) GetContributor() ranke.Contributor { return s.self }

// BookmarkId returns the id of the bookmark written at bootstrap.
func (s *Sequencer) BookmarkId() ranke.Id { return s.marks.BookmarkId() }

// GetArchive returns the immutable snapshot RA_k at the current head, read under the
// lock and built outside it, so a reader neither tears nor waits on a merge's I/O.
func (s *Sequencer) GetArchive(ctx context.Context) (ranke.Archive, error) {
	s.mu.Lock()
	head := s.head
	s.mu.Unlock()
	return ranke.NewArchive(ctx, s.u, head)
}

// NewContribution is step 1: it captures the base (k, t) and returns a
// contribution to fill, whose claims name the branches they join. Head and time come
// together, so the base is a pair that held at one instant.
func (s *Sequencer) NewContribution(_ context.Context, opts ...ranke.ContributionOption) (ranke.Contribution, error) {
	s.mu.Lock()
	base, at := s.head, s.tick()
	s.mu.Unlock()
	return &contribution{
		s:           s,
		baseHead:    base,
		baseTime:    at,
		constraints: ranke.NewConstraints(opts...),
		staged:      map[string][]ranke.Claim{},
	}, nil
}

// Merge is steps 6–7: it folds each named branch's head in, mints one branch table
// restating them all, bookmarks it, and publishes the new archive head. The lock
// covers both steps, since it derives the next head from the one it read.
func (s *Sequencer) Merge(ctx context.Context, mc ranke.MergableContribution) (ranke.Receipt, error) {
	m, ok := mc.(*mergable)
	if !ok || m.s != s {
		return nil, errForeign
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	advanced := make([]string, 0, len(m.branches))
	newHeads := make(map[string]ranke.Id, len(m.branches))
	for _, branch := range m.branches {
		prior, hasPrior := s.heads[branch]
		// Drop what the branch already reaches: identical claims carry identical ids
		// (§Idempotency), so re-offering them cannot change RG_k.
		fresh := make([]ranke.Id, 0, len(m.heads[branch]))
		for _, h := range m.heads[branch] {
			if hasPrior {
				held, err := ranke.InClosure(ctx, s.u, ranke.BranchUniverse, []ranke.Id{prior}, h)
				if err != nil {
					return nil, fmt.Errorf("%w: branch %q closure test: %w", errSequencer, branch, err)
				}
				if held {
					continue
				}
			}
			fresh = append(fresh, h)
		}
		if len(fresh) == 0 {
			continue // the branch holds it all already
		}
		// Fold the branch's previous head in so its closure accumulates across merges.
		folded := fresh
		if hasPrior {
			folded = append(append([]ranke.Id{}, fresh...), prior)
		}
		newHead := folded[0]
		if len(folded) > 1 {
			hc, err := s.consolidateHeads(ctx, folded...)
			if err != nil {
				return nil, err
			}
			if err := s.u.PutClaims(ctx, []ranke.Claim{hc}); err != nil {
				return nil, fmt.Errorf("%w: fold heads: %w", errSequencer, err)
			}
			newHead = hc.ID()
		}
		newHeads[branch] = newHead
		advanced = append(advanced, branch)
	}
	// Nothing new: RG_k' = RG_k, so there is no advance to make. Idempotent, not an
	// error — the caller asked for a state that already holds.
	if len(advanced) == 0 {
		return receipt{head: s.head}, nil
	}
	for branch, h := range newHeads {
		s.heads[branch] = h
	}

	bt, err := s.mintBranchTable(ctx, advanced)
	if err != nil {
		return nil, err
	}
	// Step 7 is where the advance k→k' takes effect, and it writes k' as the next
	// bookmark (`R-C7BOOKMARK`) — Append derives the index itself, so this can neither
	// skip a slot nor clobber one already written.
	if _, err := s.marks.Append(ctx, s.self, bt.ID()); err != nil {
		return nil, fmt.Errorf("%w: bookmark advance: %w", errSequencer, err)
	}
	s.head = bt.ID()
	// The head advanced: signal storage to refresh its query accelerators. What a
	// layer indexes, and how, is the layer's own business.
	if err := s.u.Tag(ctx, s.head); err != nil {
		return nil, fmt.Errorf("%w: tag: %w", errSequencer, err)
	}
	return receipt{head: s.head}, nil
}

// consolidateGraph returns g's single open head, folding several into one via a
// contribution/head claim added through the graph.
func (s *Sequencer) consolidateGraph(ctx context.Context, g ranke.Graph) (ranke.Id, error) {
	if g.IsConsolidated() {
		return g.Heads()[0], nil
	}
	head, err := g.Consolidate(ctx, s.self, s.tick())
	if err != nil {
		return nil, fmt.Errorf("%w: consolidate: %w", errSequencer, err)
	}
	return head.ID(), nil
}

// consolidateHeads builds a contribution/head claim over heads living in 𝒰 —
// the construction Graph.Consolidate performs for one in-memory graph.
func (s *Sequencer) consolidateHeads(ctx context.Context, heads ...ranke.Id) (ranke.Claim, error) {
	edges := make([]ranke.Edge, 0, len(heads))
	for _, h := range heads {
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: h, Type: ranke.EdgeTypeHead})
		if err != nil {
			return nil, fmt.Errorf("%w: head edge: %w", errSequencer, err)
		}
		edges = append(edges, e)
	}
	// The heads and self contributor live in 𝒰, so WithAutoHeight resolves the
	// new head's height (§4.1) from their committed heights.
	return ranke.NewClaim(ranke.NodeHead, s.self).
		WithEdges(edges...).
		WithCreatedAt(s.tick()).
		WithAutoHeight(ctx, s.u).
		Sign()
}

// mintBranchTable builds and stores the next contribution/branches claim, whose
// id is the new archive head k: a diff over the previous table restating ONLY the
// changed branches, so the others are inherited by overlaying the chain (§Branches)
// and the prior tables stay in the head's provenance — the spine (§Archive).
func (s *Sequencer) mintBranchTable(ctx context.Context, changed []string) (ranke.Claim, error) {
	b := ranke.NewClaim(ranke.NodeBranches, s.self).WithCreatedAt(s.tick())
	if s.head != nil {
		b = b.WithDiff(s.head) // diff over the previous table — build the spine
	}
	for _, branch := range changed {
		// Restate each advanced branch, its edge named as diff edges must be.
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: s.heads[branch],
			Type:      ranke.EdgeTypeBranch,
			Fields:    map[string]string{ranke.FieldName: branch},
		})
		if err != nil {
			return nil, fmt.Errorf("%w: branch edge: %w", errSequencer, err)
		}
		b = b.WithEdges(e)
	}
	// Contributor, previous table and branch head are in 𝒰; resolve height (§4.1).
	b = b.WithAutoHeight(ctx, s.u)
	table, err := b.Sign()
	if err != nil {
		return nil, fmt.Errorf("%w: mint branch table: %w", errSequencer, err)
	}
	if err := s.u.PutClaims(ctx, []ranke.Claim{table}); err != nil {
		return nil, fmt.Errorf("%w: store branch table: %w", errSequencer, err)
	}
	return table, nil
}
