package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Branch confinement in the reference executor. A read is the intersection of the
// scope's graph and closure(Select.Head), so a Head naming a claim the branch does
// not hold narrows the read rather than reaching into the other branch.
//
// The agreement matrix cannot cover this: it compares every backend against this
// engine, so a leak here reads as correct and the native lowering's confinement
// reads as the divergence. These assert exact answers instead.

// twoBranches builds two graphs in one Universe, each under its own contributor,
// so the branches share no claim at all:
//
//	"a":  rootA(0) ← sA(1)
//	"b":  rootB(0) ← sB(1)
func twoBranches(t *testing.T) (Universe, map[string]Claim) {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	rootA, rootB := contributor(t), contributor(t)
	sA, sB := srcClaim(t, rootA, "in branch a"), srcClaim(t, rootB, "in branch b")

	all := map[string]Claim{"rootA": rootA, "rootB": rootB, "sA": sA, "sB": sB}
	for _, c := range []Claim{rootA, rootB, sA, sB} {
		require.NoError(t, PutClaim(ctx, u, c))
	}
	return u, all
}

// confinedTo is the Scope an Archive resolves for a branch: its head bounds the
// read, whatever Select.Head then names.
func confinedTo(name string, head Claim) Scope {
	return Scope{Head: head.ID(), Branch: name}
}

// reachedUnder runs q under scope and returns the reached id set.
func reachedUnder(t *testing.T, u Universe, q Query, scope Scope) map[string]bool {
	t.Helper()
	rs, err := u.Query(context.Background(), q, scope)
	require.NoError(t, err)
	return idSet(drain(t, rs))
}

// TestConfineHeadOutsideBranchReadsNothing is the acceptance case: branch "a" with
// a Head in "b". The two graphs are disjoint, so the intersection is empty and the
// read returns nothing — never branch b's claims.
func TestConfineHeadOutsideBranchReadsNothing(t *testing.T) {
	u, a := twoBranches(t)
	q := Query{Select: Select{Branch: "a", Head: a["sB"].ID()}}

	got := reachedUnder(t, u, q, confinedTo("a", a["sA"]))
	require.Empty(t, got, "a head outside the branch must not read the branch it lives in")
}

// TestConfineClaimOutsideBranchReadsNothing covers the other anchor. Select.Claim
// takes an arbitrary id just as Head does, and walkStart fetches it without a
// membership test, so the endpoint gate in reach is the only thing holding the
// door. Min 0 makes the anchor itself a candidate, so a gate that missed hop 0
// would return it.
//
// It is load-bearing for the obvious optimisation — skipping confine when
// Select.Head is nil — which would reopen the leak here while every Head test
// stayed green.
func TestConfineClaimOutsideBranchReadsNothing(t *testing.T) {
	u, a := twoBranches(t)
	q := Query{Select: Select{
		Branch: "a", Claim: Anchors(a["sB"].ID()),
		Path: []PathStep{{Min: Hops(0), Max: 3}},
	}}

	got := reachedUnder(t, u, q, confinedTo("a", a["sA"]))
	require.Empty(t, got, "a claim anchor outside the branch must not read the branch it lives in")
}

// TestConfineClaimInsideBranchStillReads is that anchor's control: the same walk
// from a claim the branch holds still returns it.
func TestConfineClaimInsideBranchStillReads(t *testing.T) {
	u, a := twoBranches(t)
	q := Query{Select: Select{
		Branch: "a", Claim: Anchors(a["sA"].ID()),
		Path: []PathStep{{Min: Hops(0), Max: 3}},
	}}

	got := reachedUnder(t, u, q, confinedTo("a", a["sA"]))
	require.Equal(t, idsOf(a["sA"], a["rootA"]), got)
}

// TestConfineHeadInsideBranchStillReads is the control: the same scope with a Head
// the branch does hold returns that head's closure. Without it, confinement could
// pass by returning nothing for every query.
func TestConfineHeadInsideBranchStillReads(t *testing.T) {
	u, a := twoBranches(t)
	q := Query{Select: Select{Branch: "a", Head: a["sA"].ID()}}

	got := reachedUnder(t, u, q, confinedTo("a", a["sA"]))
	require.Equal(t, idsOf(a["sA"], a["rootA"]), got)
}

// TestConfineIsIntersectionNotEitherSide: with a contributor shared across both
// branches, a Head in the other branch still yields the shared claim — the read is
// the intersection, not the scope's whole graph and not the head's.
func TestConfineIsIntersectionNotEitherSide(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	shared := contributor(t)
	sA, sB := srcClaim(t, shared, "in a"), srcClaim(t, shared, "in b")
	for _, c := range []Claim{shared, sA, sB} {
		require.NoError(t, PutClaim(ctx, u, c))
	}

	q := Query{Select: Select{Branch: "a", Head: sB.ID()}}
	got := reachedUnder(t, u, q, confinedTo("a", sA))

	require.Equal(t, idsOf(shared), got,
		"only what both graphs hold: the shared contributor, never sB itself")
}

// TestConfineUnscopedReadIsUnchanged: $universe sets no scope head, so nothing is
// confined and a Head reads its whole closure. Confinement must not narrow the
// privileged read.
func TestConfineUnscopedReadIsUnchanged(t *testing.T) {
	u, a := twoBranches(t)
	q := Query{Select: Select{Branch: BranchUniverse, Head: a["sB"].ID()}}

	got := reachedUnder(t, u, q, Scope{Branch: BranchUniverse})
	require.Equal(t, idsOf(a["sB"], a["rootB"]), got)
}

// TestConfineReverseStepStaysInBranch guards the reverse path, which reads its
// referrers from an inverted closure rather than from edges. Built from the head
// the query names, that index would invert the other branch; it is built over the
// scope's graph, so a reverse step finds only referrers the branch holds.
func TestConfineReverseStepStaysInBranch(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	shared := contributor(t)
	// Two derivations of one source, committed to different branches.
	src := srcClaim(t, shared, "cited twice")
	inA := entityClaim(t, shared, "person", "from a", src)
	inB := entityClaim(t, shared, "person", "from b", src)
	for _, c := range []Claim{shared, src, inA, inB} {
		require.NoError(t, PutClaim(ctx, u, c))
	}

	// Who uses src? Branch a holds inA only, so the answer is inA — never inB,
	// though both reference src and the walk starts from b's head.
	q := Query{Select: Select{
		Branch: "a", Head: inB.ID(), Claim: Anchors(src.ID()),
		Path: []PathStep{{Dir: DirUses, Max: 1}},
	}}
	got := reachedUnder(t, u, q, confinedTo("a", inA))
	require.Equal(t, idsOf(inA), got)
}
