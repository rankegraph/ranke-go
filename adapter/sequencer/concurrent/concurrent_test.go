package concurrent_test

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	concseq "github.com/rankegraph/ranke-go/adapter/sequencer/concurrent"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// clock is a monotonic time source safe for concurrent use — the test drives it
// from many goroutines at once (building claims) while the Sequencer ticks it on
// its sequencing thread.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Tick() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.t
	c.t = c.t.Add(time.Second)
	return out
}

// operator builds a signed root contributor carrying its own signing key, so the
// Sequencer can attribute and sign with it. It is an initial claim (height 0).
func operator(t *testing.T, ctx context.Context, at time.Time) ranke.Contributor {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	pub, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	cc, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pub).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(at).
		Sign(priv)
	require.NoError(t, err)
	op, err := cc.AsContributor(ctx, nil, priv)
	require.NoError(t, err)
	return op
}

type fixture struct {
	u     ranke.Universe
	marks *ranke.Bookmarks
	seq   *concseq.Sequencer
	op    ranke.Contributor
	clk   *clock
}

func newFixture(t *testing.T, ctx context.Context) *fixture {
	t.Helper()
	u := ranke.NewMemoryUniverse()
	clk := &clock{t: time.Unix(1000, 0).UTC()}
	op := operator(t, ctx, clk.Tick())
	seq, err := concseq.NewSequencer(ctx, u, ranke.Seed([]byte(op.ID().String())), op, clk)
	require.NoError(t, err)
	require.True(t, seq.InGenesis(), "an empty list is an archive not yet founded")
	_, err = helpers.Found(ctx, seq, "concurrent")
	require.NoError(t, err)
	// A second, independent view over the same bookmark list, reached the way any
	// external reader would: from one bookmark id, never discovered (paper §Backup).
	marks, err := ranke.OpenBookmarks(ctx, u, seq.BookmarkId())
	require.NoError(t, err)
	return &fixture{u: u, marks: marks, seq: seq, op: op, clk: clk}
}

// note builds a source/note claim attributed to the operator: its only reference
// is the auto-added contributor edge (height 0, already in 𝒰), so it is height 1
// and its closure resolves.
func (f *fixture) note(t *testing.T, body string) ranke.Claim {
	t.Helper()
	c, err := ranke.NewClaim(ranke.TypeSource("note"), f.op).
		WithInlineContent([]byte(body)).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(f.clk.Tick()).
		Sign()
	require.NoError(t, err)
	return c
}

// TestDeniedContributionNamesTheRule: a contribution the verifier refuses carries the
// rule it broke and the claim that broke it, so a client branches on the rule rather
// than reading the message.
func TestDeniedContributionNamesTheRule(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	bad, err := ranke.NewClaim(ranke.TypeSource("note"), f.op).
		WithInlineContent([]byte("body")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(99). // its lone contributor edge fixes 1
		WithCreatedAt(f.clk.Tick()).
		Sign()
	require.NoError(t, err, "the claim seals; height is the verifier's to judge")

	_, err = helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{bad})
	require.Error(t, err)
	require.ErrorIs(t, err, ranke.ErrHeightMismatch, "the rule reaches the caller")

	var vf ranke.Failure
	require.ErrorAs(t, err, &vf, "and so does the failure")
	require.True(t, vf.ID.Equal(bad.ID()), "naming the claim that broke it")
}

// head resolves the archive head k through a fresh snapshot.
func (f *fixture) head(t *testing.T, ctx context.Context) ranke.Id {
	t.Helper()
	arc, err := f.seq.GetArchive(ctx)
	require.NoError(t, err)
	return arc.Head()
}

// branchHead resolves the current head of a branch through a fresh snapshot.
func (f *fixture) branchHead(t *testing.T, ctx context.Context, name string) ranke.Id {
	t.Helper()
	arc, err := f.seq.GetArchive(ctx)
	require.NoError(t, err)
	br, err := arc.GetBranch(ctx, name)
	require.NoError(t, err, "branch %q exists", name)
	return br.Head()
}

// requireReachable asserts every id is in the closure of the branch head — the
// only thing that ultimately matters about a merged contribution.
func (f *fixture) requireReachable(t *testing.T, ctx context.Context, branch string, ids []ranke.Id) {
	t.Helper()
	g, err := ranke.NewGraphFromClosure(ctx, f.branchHead(t, ctx, branch), f.u)
	require.NoError(t, err)
	for _, id := range ids {
		ok, err := g.ContainsClaim(ctx, id)
		require.NoError(t, err)
		require.Truef(t, ok, "%s must be reachable from branch %q", id, branch)
	}
}

// TestConcurrentAddsAllSurvive is the property the whole adapter exists for: many
// writers contributing at once to the SAME branch, every one of them opening
// against whatever head it happened to see, and not a single write lost. This is
// what the dev Sequencer cannot do — it serialises the six steps end to end — and
// what makes folding against the live branch head (rather than the contribution's
// base) the load-bearing decision in the design.
func TestConcurrentAddsAllSurvive(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	const writers = 8
	ids := make([]ranke.Id, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := f.note(t, fmt.Sprintf("concurrent note %d", i))
			ids[i] = c.ID()
			_, errs[i] = helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{c})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "writer %d", i)
	}
	f.requireReachable(t, ctx, "main", ids)
}

// TestConcurrentBranchesAllSurvive checks the other axis: writers spread across
// several branches. One commit can advance more than one branch, since a branch
// table is a set of named edges — so a batch touching three branches must still
// leave all three correct and independent.
func TestConcurrentBranchesAllSurvive(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	branches := []string{"main", "alpha", "beta"}
	const perBranch = 3
	ids := map[string][]ranke.Id{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, b := range branches {
		for i := range perBranch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := f.note(t, fmt.Sprintf("%s note %d", b, i))
				_, err := helpers.Contribute(ctx, f.seq, b, []ranke.Claim{c})
				mu.Lock()
				defer mu.Unlock()
				require.NoError(t, err)
				ids[b] = append(ids[b], c.ID())
			}()
		}
	}
	wg.Wait()

	for _, b := range branches {
		require.Len(t, ids[b], perBranch)
		f.requireReachable(t, ctx, b, ids[b])
	}
	// Branches stay independent: a note written to alpha is not in beta.
	g, err := ranke.NewGraphFromClosure(ctx, f.branchHead(t, ctx, "beta"), f.u)
	require.NoError(t, err)
	in, err := g.ContainsClaim(ctx, ids["alpha"][0])
	require.NoError(t, err)
	require.False(t, in, "a claim contributed to alpha must not appear in beta")
}

// TestOneContributionAdvancesSeveralBranches is the bulk-contribution capability:
// a contribution names the branch each batch joins and may name SEVERAL, so one
// merge advances them all — a branch table being a set of named edges. Where
// TestConcurrentBranchesAllSurvive spreads branches across contributions, here a
// single contribution does the fan-out, so nothing else can account for it.
func TestOneContributionAdvancesSeveralBranches(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	// Seed both branches first, so "the head moved" is a comparison, not an appearance.
	seedA, seedB := f.note(t, "alpha seed"), f.note(t, "beta seed")
	_, err := helpers.Contribute(ctx, f.seq, "alpha", []ranke.Claim{seedA})
	require.NoError(t, err)
	_, err = helpers.Contribute(ctx, f.seq, "beta", []ranke.Claim{seedB})
	require.NoError(t, err)
	beforeA, beforeB := f.branchHead(t, ctx, "alpha"), f.branchHead(t, ctx, "beta")
	revisions, err := f.marks.Len(ctx)
	require.NoError(t, err)

	onAlpha, onBeta := f.note(t, "for alpha"), f.note(t, "for beta")
	c, err := f.seq.NewContribution(ctx, ranke.WithReferencableBranches(ranke.BranchArchive),
		ranke.WithCreatableBranches(ranke.TargetBranches))
	require.NoError(t, err)
	require.NoError(t, c.AddClaims("alpha", []ranke.Claim{onAlpha}))
	require.NoError(t, c.AddClaims("beta", []ranke.Claim{onBeta}))
	v, err := c.CompleteAndVerify(ctx)
	require.NoError(t, err)
	m, err := v.Persist(ctx)
	require.NoError(t, err)
	require.Len(t, m.Heads(), 2, "one open head per branch the contribution named")
	r, err := f.seq.Merge(ctx, m)
	require.NoError(t, err)
	require.True(t, r.Head().Equal(f.head(t, ctx)))

	after, err := f.marks.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, revisions+1, after, "one contribution, one revision, two branches advanced")
	require.False(t, beforeA.Equal(f.branchHead(t, ctx, "alpha")), "alpha advanced")
	require.False(t, beforeB.Equal(f.branchHead(t, ctx, "beta")), "beta advanced")

	// Each branch reaches its own batch from its own head, and keeps its seed.
	f.requireReachable(t, ctx, "alpha", []ranke.Id{seedA.ID(), onAlpha.ID()})
	f.requireReachable(t, ctx, "beta", []ranke.Id{seedB.ID(), onBeta.ID()})
	g, err := ranke.NewGraphFromClosure(ctx, f.branchHead(t, ctx, "beta"), f.u)
	require.NoError(t, err)
	in, err := g.ContainsClaim(ctx, onAlpha.ID())
	require.NoError(t, err)
	require.False(t, in, "a batch named for alpha must not land in beta")
}

// TestUnnamedBranchRefused: every batch names the branch it joins, and there is no
// default to fall back on — an unnamed one is refused, admitting nothing.
func TestUnnamedBranchRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	c, err := f.seq.NewContribution(ctx, ranke.WithReferencableBranches(ranke.BranchArchive),
		ranke.WithCreatableBranches(ranke.TargetBranches))
	require.NoError(t, err)
	require.Error(t, c.AddClaims("", []ranke.Claim{f.note(t, "nowhere")}))

	_, err = c.CompleteAndVerify(ctx)
	require.Error(t, err, "nothing was admitted, so there is nothing to verify")
}

// TestInvalidContributionDenied is the guarantee the Sequencer must uphold under
// concurrency exactly as it does serially: valid(A) → valid(A′). A contribution
// referencing a claim that does not exist is structurally invalid, so step 4 must
// reject it and the head must not move.
func TestInvalidContributionDenied(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	before := f.head(t, ctx)

	bogus, err := ranke.HashContent([]byte("no-such-claim"))
	require.NoError(t, err)
	de, err := ranke.NewEdge(ranke.EdgeConfig{Reference: bogus, Type: ranke.TypeDerivation("note")})
	require.NoError(t, err)
	bad, err := ranke.NewClaim(ranke.TypeDerivation("note"), f.op).
		WithInlineContent([]byte("derived from nothing")).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(de).
		WithHeight(1).
		WithCreatedAt(f.clk.Tick()).
		Sign()
	require.NoError(t, err, "the claim itself is well-formed; only its reference dangles")

	_, err = helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{bad})
	require.Error(t, err, "a contribution referencing a non-existent claim must be denied")
	require.True(t, before.Equal(f.head(t, ctx)), "a denied contribution must not advance the head")
}

// TestValidContributionAccepted is the positive control for the above: without
// it, denial could pass by denying everything.
func TestValidContributionAccepted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	before := f.head(t, ctx)

	head, err := helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{f.note(t, "a real source note")})
	require.NoError(t, err)
	require.False(t, before.Equal(head), "a merged contribution advances the head")
	require.True(t, head.Equal(f.head(t, ctx)))
}

// TestReservedTypeRefused: branch tables are the Sequencer's alone (step 2). A
// client that could author one could forge the archive head, so the contribution
// must be refused before anything is staged.
func TestReservedTypeRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	before := f.head(t, ctx)

	forged, err := ranke.NewClaim(ranke.NodeBranches, f.op).
		WithCreatedAt(f.clk.Tick()).
		WithHeight(1).
		Sign()
	require.NoError(t, err)

	_, err = helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{forged})
	require.Error(t, err, "a client-authored branch table must be refused")
	require.True(t, before.Equal(f.head(t, ctx)))
}

// TestFutureDatedClaimRefused: a contribution is stamped with the server's time t
// at opening, and no claim in it may be dated later (§Timestamping) — otherwise a
// client could future-date its way past the archive's own clock.
func TestFutureDatedClaimRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	c, err := f.seq.NewContribution(ctx, ranke.WithReferencableBranches(ranke.BranchArchive),
		ranke.WithCreatableBranches(ranke.TargetBranches))
	require.NoError(t, err)
	_, base := c.Base()

	future, err := ranke.NewClaim(ranke.TypeSource("note"), f.op).
		WithInlineContent([]byte("from the future")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(base.Add(time.Hour)).
		Sign()
	require.NoError(t, err)

	require.Error(t, c.AddClaims("main", []ranke.Claim{future}),
		"a claim dated after the contribution base must be refused")
}

// TestBaseIsTheSnapshotItOpenedAt: opening is step 1 and records (k, t) — the
// head as it was, not as it later becomes. A contribution opened before another
// writer's merge must still report the older base, which is what makes it safe to
// verify off the sequencing thread while the head moves on.
func TestBaseIsTheSnapshotItOpenedAt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	// Claims must predate the base their contribution is opened against, so
	// build this one first — it is contributed last, after the head has moved.
	late := f.note(t, "late but not lost")

	c, err := f.seq.NewContribution(ctx, ranke.WithReferencableBranches(ranke.BranchArchive),
		ranke.WithCreatableBranches(ranke.TargetBranches))
	require.NoError(t, err)
	baseHead, _ := c.Base()

	_, err = helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{f.note(t, "someone else got there first")})
	require.NoError(t, err)
	require.False(t, baseHead.Equal(f.head(t, ctx)), "the head moved while the contribution was open")

	stillBase, _ := c.Base()
	require.True(t, baseHead.Equal(stillBase), "the base is pinned to the snapshot it opened at")

	// And the contribution still merges cleanly onto the advanced head.
	require.NoError(t, c.AddClaims("main", []ranke.Claim{late}))
	v, err := c.CompleteAndVerify(ctx)
	require.NoError(t, err)
	m, err := v.Persist(ctx)
	require.NoError(t, err)
	_, err = f.seq.Merge(ctx, m)
	require.NoError(t, err)
	f.requireReachable(t, ctx, "main", v.Ids())
}

// TestSealedContributionRefusesMoreClaims: once verified, a contribution's
// contents are fixed — that is what lets "whatever verified stays valid" hold
// however long it waits for its merge.
func TestSealedContributionRefusesMoreClaims(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	first, second := f.note(t, "sealed"), f.note(t, "too late")

	c, err := f.seq.NewContribution(ctx, ranke.WithReferencableBranches(ranke.BranchArchive),
		ranke.WithCreatableBranches(ranke.TargetBranches))
	require.NoError(t, err)
	require.NoError(t, c.AddClaims("main", []ranke.Claim{first}))
	_, err = c.CompleteAndVerify(ctx)
	require.NoError(t, err)

	require.Error(t, c.AddClaims("main", []ranke.Claim{second}),
		"a sealed contribution must refuse further claims")
	_, err = c.CompleteAndVerify(ctx)
	require.Error(t, err, "a sealed contribution must refuse to re-seal")
}

// TestForeignContributionRefused: a mergable carries the branches it advances and
// the archive it belongs to, neither of which is on the public contract. Merging
// one into a different Sequencer would be silent corruption, so it is refused.
func TestForeignContributionRefused(t *testing.T) {
	ctx := context.Background()
	a, b := newFixture(t, ctx), newFixture(t, ctx)

	mine := a.note(t, "belongs to a")
	c, err := a.seq.NewContribution(ctx, ranke.WithReferencableBranches(ranke.BranchArchive),
		ranke.WithCreatableBranches(ranke.TargetBranches))
	require.NoError(t, err)
	require.NoError(t, c.AddClaims("main", []ranke.Claim{mine}))
	v, err := c.CompleteAndVerify(ctx)
	require.NoError(t, err)
	m, err := v.Persist(ctx)
	require.NoError(t, err)

	before := b.head(t, ctx)
	_, err = b.seq.Merge(ctx, m)
	require.Error(t, err, "a Sequencer must refuse a contribution it did not open")
	require.True(t, before.Equal(b.head(t, ctx)))
}

// TestMultiHeadContributionKeepsBoth: two unrelated claims leave the contribution
// two-headed, and consolidation must gather both.
func TestMultiHeadContributionKeepsBoth(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	one, two := f.note(t, "note one"), f.note(t, "note two")
	c, err := f.seq.NewContribution(ctx, ranke.WithReferencableBranches(ranke.BranchArchive),
		ranke.WithCreatableBranches(ranke.TargetBranches))
	require.NoError(t, err)
	require.NoError(t, c.AddClaims("main", []ranke.Claim{one, two}))
	v, err := c.CompleteAndVerify(ctx)
	require.NoError(t, err)
	m, err := v.Persist(ctx)
	require.NoError(t, err)
	_, err = f.seq.Merge(ctx, m)
	require.NoError(t, err)

	f.requireReachable(t, ctx, "main", []ranke.Id{one.ID(), two.ID()})
}

// noBookmarks presents a Universe holding no 𝒰_hist — neo4j's shape, a projection
// you drop and reindex.
type noBookmarks struct{ ranke.Universe }

func (n noBookmarks) Capabilities() ranke.Capabilities {
	c := n.Universe.Capabilities()
	c.Bookmarks = false
	return c
}

// TestNewSequencerRefusesAUniverseHoldingNoBookmarks: the head has nowhere to be
// recorded, so the archive it advanced would be unreachable. Refused at construction
// rather than at the first append, which is already one merge too late.
func TestNewSequencerRefusesAUniverseHoldingNoBookmarks(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1000, 0).UTC()}
	op := operator(t, ctx, clk.Tick())

	_, err := concseq.NewSequencer(ctx, noBookmarks{ranke.NewMemoryUniverse()},
		ranke.Seed([]byte("no-hist")), op, clk)
	require.ErrorIs(t, err, ranke.ErrUnsupported)
}

// TestBookmarkIdIsRaceFreeUnderMerge: BookmarkId() takes no lock, where Merge holds
// the sequencing lock across the Append that used to write the field it reads. The
// value is settled before the Sequencer exists, so there is nothing to race on —
// under -race this test is what says so.
func TestBookmarkIdIsRaceFreeUnderMerge(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	want := f.seq.BookmarkId()
	require.NotNil(t, want, "the locator is settled at construction, not at the first append")

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{f.note(t, "merging")})
			require.NoError(t, err)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				require.True(t, want.Equal(f.seq.BookmarkId()), "the locator never moves")
			}
		}()
	}
	wg.Wait()
}
