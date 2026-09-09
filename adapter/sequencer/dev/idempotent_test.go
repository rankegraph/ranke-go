package dev_test

// Idempotence of the reference write path. It is a property of the ADT, not of one
// adapter: identical claims carry identical ids (§Idempotency), so a contribution
// the branch already holds cannot change RG_k and must not advance the archive.
// The concurrent Sequencer asserts the same thing, and the two must not diverge —
// the reference is what conformance is judged against.

import (
	"context"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// newSeqWithUniverse stands up a Sequencer and hands back its Universe too, so a
// test can walk a branch closure rather than settle for archive-wide reachability.
func newSeqWithUniverse(t *testing.T, ctx context.Context) (*devseq.Sequencer, ranke.Universe, ranke.Contributor, *clock) {
	t.Helper()
	u := ranke.NewMemoryUniverse()
	clk := &clock{t: time.Unix(1000, 0).UTC()}
	op := operator(t, ctx, clk.Tick())
	seq, err := devseq.NewSequencer(ctx, u, ranke.Seed([]byte(op.ID().String())), op, clk)
	require.NoError(t, err)
	_, err = helpers.Found(ctx, seq, "dev-idempotent")
	require.NoError(t, err)
	return seq, u, op, clk
}

// note builds a source/note claim attributed to op: its only reference is the
// auto-added contributor edge (height 0, in 𝒰), so it is height 1 and resolves.
func note(t *testing.T, op ranke.Contributor, clk *clock, body string) ranke.Claim {
	t.Helper()
	c, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte(body)).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)
	return c
}

// branchHead resolves a branch's current head through a fresh snapshot.
func branchHead(t *testing.T, ctx context.Context, seq ranke.Sequencer, name string) ranke.Id {
	t.Helper()
	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err)
	br, err := arc.GetBranch(ctx, name)
	require.NoError(t, err, "branch %q exists", name)
	return br.Head()
}

// TestReContributionIsNoop: the same claim contributed twice leaves the archive
// where it was. No new head, and no error — the caller asked for a state that
// already holds.
func TestReContributionIsNoop(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	c := note(t, op, clk, "contributed twice")
	first, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{c})
	require.NoError(t, err)

	headAfter := head(t, ctx, seq)
	branchAfter := branchHead(t, ctx, seq, "main")

	second, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{c})
	require.NoError(t, err, "re-contributing an existing claim is a no-op, not an error")

	require.True(t, first.Equal(second), "the second merge returns the standing head")
	require.True(t, headAfter.Equal(head(t, ctx, seq)), "the archive head must not advance")
	require.True(t, branchAfter.Equal(branchHead(t, ctx, seq, "main")),
		"the branch head must not advance")
}

// TestReContributionOfInteriorClaimIsNoop is the case a head-equality check would
// miss: the branch head has moved past the claim, so it is interior. Folding it
// back in would wrap the head and its own ancestor in a new consolidating claim —
// a fresh head over an unchanged closure.
func TestReContributionOfInteriorClaimIsNoop(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	first := note(t, op, clk, "first")
	_, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{first})
	require.NoError(t, err)
	second := note(t, op, clk, "second")
	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{second})
	require.NoError(t, err)

	branchAfter := branchHead(t, ctx, seq, "main")
	require.False(t, branchAfter.Equal(first.ID()), "the head has moved past the first claim")

	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{first})
	require.NoError(t, err)
	require.True(t, branchAfter.Equal(branchHead(t, ctx, seq, "main")),
		"re-offering an interior claim must not fold a new head over the branch")
}

// TestPartlyNewContributionAdvances is the control: a batch mixing a held claim
// with a new one still advances. Without it, idempotence could pass by refusing
// everything.
func TestPartlyNewContributionAdvances(t *testing.T) {
	ctx := context.Background()
	seq, u, op, clk := newSeqWithUniverse(t, ctx)

	old := note(t, op, clk, "already present")
	_, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{old})
	require.NoError(t, err)
	before := branchHead(t, ctx, seq, "main")

	fresh := note(t, op, clk, "genuinely new")
	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{old, fresh})
	require.NoError(t, err)

	require.False(t, before.Equal(branchHead(t, ctx, seq, "main")),
		"a batch containing anything new must advance the branch")

	g, err := ranke.NewGraphFromClosure(ctx, branchHead(t, ctx, seq, "main"), u)
	require.NoError(t, err)
	for _, id := range []ranke.Id{old.ID(), fresh.ID()} {
		in, err := g.ContainsClaim(ctx, id)
		require.NoError(t, err)
		require.Truef(t, in, "%s must be reachable after the advance", id)
	}
}

// TestSameClaimToAnotherBranchAdvances: "already in the archive" is not "already in
// this branch". Branches are independent heads, so the closure test has to be per
// branch or a claim would never reach a second one.
func TestSameClaimToAnotherBranchAdvances(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	c := note(t, op, clk, "shared across branches")
	_, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{c})
	require.NoError(t, err)

	_, err = helpers.Contribute(ctx, seq, "alpha", []ranke.Claim{c})
	require.NoError(t, err, "a claim another branch holds is new to this one")

	require.True(t, branchHead(t, ctx, seq, "alpha").Equal(c.ID()),
		"alpha takes the claim as its head")
}
