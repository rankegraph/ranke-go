// package: adapter/sequencer/concurrent / adapter
// type:    adapter
// job:     the concurrent Sequencer's contribution stages — steps 2–5 (adding, completing,
// verifying, persisting), the work that runs OFF the sequencing thread
// limits:  one contribution is not itself concurrency-safe for filling beyond the mutex here (fill
// it from one goroutine); merging is step 6 and lives with the Sequencer (-> concurrent.go)
package concurrent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/rankegraph/ranke-go"
)

var (
	errSealed            = errors.New("concurrent.Contribution: already sealed")
	errNoBranch          = errors.New("concurrent.Contribution: claims must name the branch they join")
	errNilClaim          = errors.New("concurrent.Contribution.AddClaims: nil claim")
	errEmptyContribution = errors.New("concurrent.Contribution.CompleteAndVerify: nothing was added")
	errContribution      = errors.New("concurrent.Contribution")
)

var (
	_ ranke.Contribution         = (*contribution)(nil)
	_ ranke.VerifiedContribution = (*verified)(nil)
	_ ranke.MergableContribution = (*mergable)(nil)
)

// contribution is an in-progress advance of the archive — steps 2–4 against the
// base (k, t) of step 1, staged per branch, off the sequencing thread. A bulk
// contribution may name several branches; CompleteAndVerify seals them all.
type contribution struct {
	s           *Sequencer
	baseHead    ranke.Id
	baseTime    time.Time
	constraints ranke.Constraints

	mu       sync.Mutex
	sealed   bool
	staged   map[string][]ranke.Claim // per branch, in dependency order
	branches []string                 // first-named order, so a merge is deterministic
}

// Base is the (k, t) the contribution was opened against.
func (c *contribution) Base() (ranke.Id, time.Time) { return c.baseHead, c.baseTime }

// AddClaims is step 2: it stages a batch for branch in dependency order
// (references first), each claim checked against the base. In-memory work alone.
func (c *contribution) AddClaims(branch string, claims []ranke.Claim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed {
		return errSealed
	}
	if branch == "" {
		return errNoBranch
	}
	for _, cl := range claims {
		if cl == nil {
			return errNilClaim
		}
		if err := c.admissible(cl); err != nil {
			return err
		}
	}
	c.note(branch)
	c.staged[branch] = append(c.staged[branch], claims...)
	return nil
}

// AddWire is step 2 from a wire stream, whose records name their own branches.
func (c *contribution) AddWire(ctx context.Context, wr *ranke.WireReader) error {
	return ranke.DrainWire(ctx, c, c.s.u, wr)
}

// note records a branch the first time it is named.
func (c *contribution) note(branch string) {
	if _, seen := c.staged[branch]; seen {
		return
	}
	c.branches = append(c.branches, branch)
}

// admitReferences checks the branch's staged claims against what the contribution may
// read, resolved at its base so the permitted set holds still while it is open.
func (c *contribution) admitReferences(ctx context.Context, branch string) error {
	base, err := ranke.NewArchive(ctx, c.s.u, c.baseHead)
	if err != nil {
		return fmt.Errorf("%w: open base: %w", errContribution, err)
	}
	if err := c.constraints.AdmitBranch(ctx, base, branch); err != nil {
		return err
	}
	return c.constraints.AdmitReferences(ctx, c.s.u, base, branch, c.staged[branch])
}

// admissible applies the two step-2 rules: a claim is dated at or before the base
// time t (§Timestamping), and its type is not one the constraints withhold.
func (c *contribution) admissible(cl ranke.Claim) error {
	if err := ranke.AdmitCreatedAt(cl, c.baseTime); err != nil {
		return err
	}
	if err := c.constraints.AdmitType(cl.Node().Type()); err != nil {
		return fmt.Errorf("%w: %s", err, cl.ID())
	}
	return nil
}

// CompleteAndVerify runs steps 3 and 4 in one traversal per branch: it closes the
// contribution over its base, verifies what it reaches, and seals the result.
func (c *contribution) CompleteAndVerify(ctx context.Context) (ranke.VerifiedContribution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed {
		return nil, errSealed
	}
	if len(c.branches) == 0 {
		return nil, errEmptyContribution
	}

	var ids []ranke.Id
	heads := make(map[string][]ranke.Id, len(c.branches))
	// One graph per branch: each branch takes its own root, and a claim named for
	// two branches is written once — 𝒰 is add-only.
	for _, branch := range c.branches {
		g, err := ranke.NewGraph(ctx, c.s.u)
		if err != nil {
			return nil, fmt.Errorf("%w: new graph: %w", errContribution, err)
		}
		// The verifier reads the closure out of 𝒰, so staging happens here. 𝒰 is
		// add-only, leaving a failed contribution's claims unreachable from any head.
		if err := g.AddClaims(ctx, c.staged[branch]...); err != nil {
			return nil, fmt.Errorf("%w: stage %q: %w", errContribution, branch, err)
		}
		for _, cl := range c.staged[branch] {
			ids = append(ids, cl.ID())
		}

		// Step 3's read requirement, over the base.
		if err := c.admitReferences(ctx, branch); err != nil {
			return nil, err
		}

		// Pruning at merged claims costs a contribution its own closure, not the archive's.
		run := g.Verify(ranke.WithTrusted(c.s.isCommitted))
		run.Wait()
		if err := run.Err(); err != nil {
			return nil, fmt.Errorf("%w: verify %q: %w", errContribution, branch, err)
		}
		if fs := run.Failures(); len(fs) > 0 {
			// The Failure travels as the cause, so a caller matches the rule it broke
			// with errors.Is and recovers the claim with errors.As.
			return nil, ranke.WrapDetail(errContribution,
				"verify "+branch+": "+strconv.Itoa(len(fs))+" failure(s), first", fs[0])
		}

		// Deletion leaves an explained gap where a claim's bytes were, and its edges with
		// them, so the head cites whatever a walk could then no longer reach.
		stranded, err := ranke.StrandedByDeletion(ctx, c.s.u, g.Heads(), c.staged[branch])
		if err != nil {
			return nil, fmt.Errorf("%w: reachability past a deleted claim: %w", errContribution, err)
		}
		g.Cite(stranded...)

		head, err := c.consolidate(ctx, g)
		if err != nil {
			return nil, err
		}
		heads[branch] = []ranke.Id{head}
	}

	c.sealed = true
	return &verified{c: c, ids: ids, heads: heads}, nil
}

// consolidate folds a branch's open heads into one, so it takes a single root.
// Stamped with the base time t, since the Clock belongs to the sequencing thread
// and every claim folded here is dated at or before t.
func (c *contribution) consolidate(ctx context.Context, g ranke.Graph) (ranke.Id, error) {
	if g.IsConsolidated() {
		return g.Heads()[0], nil
	}
	head, err := g.Consolidate(ctx, c.s.self, c.baseTime)
	if err != nil {
		return nil, fmt.Errorf("%w: consolidate: %w", errContribution, err)
	}
	return head.ID(), nil
}

// verified is a sealed contribution: its contents are fixed, so the verification
// holds however long the merge waits.
type verified struct {
	c     *contribution
	ids   []ranke.Id
	heads map[string][]ranke.Id
}

// Ids are the claim ids the contribution adds.
func (v *verified) Ids() []ranke.Id {
	out := make([]ranke.Id, len(v.ids))
	copy(out, v.ids)
	return out
}

// Persist is step 5, the durability barrier before the head advances: HasClaims
// confirms every id and head reads back, since a head may not outrun its closure.
func (v *verified) Persist(ctx context.Context) (ranke.MergableContribution, error) {
	ids := make([]ranke.Id, 0, len(v.ids)+len(v.heads))
	ids = append(ids, v.ids...)
	for _, branch := range v.c.branches {
		ids = append(ids, v.heads[branch]...)
	}

	present, err := v.c.s.u.HasClaims(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: persist: %w", errContribution, err)
	}
	if len(present) != len(ids) {
		return nil, fmt.Errorf("%w: persist: HasClaims answered %d of %d ids",
			errContribution, len(present), len(ids))
	}
	for i, ok := range present {
		if !ok {
			return nil, fmt.Errorf("%w: persist: %s did not land in the Universe", errContribution, ids[i])
		}
	}
	return &mergable{s: v.c.s, branches: v.c.branches, heads: v.heads, ids: v.ids}, nil
}

// mergable is a persisted contribution ready for step 6. It names the branches it
// advances and the Sequencer that opened it, so a merge lands in the right place.
type mergable struct {
	s        *Sequencer
	branches []string // first-named order, so a merge is deterministic
	heads    map[string][]ranke.Id
	ids      []ranke.Id
}

// Heads are the contribution's open head claim ids per branch, which the
// Sequencer folds into each branch's new head.
func (m *mergable) Heads() map[string][]ranke.Id {
	out := make(map[string][]ranke.Id, len(m.heads))
	for b, hs := range m.heads {
		out[b] = append([]ranke.Id{}, hs...)
	}
	return out
}
