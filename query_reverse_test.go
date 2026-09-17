package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// derivClaim builds a derivation claim referencing refs (derivation/source
// edges), height resolved from its contributor + references.
func derivClaim(t *testing.T, root Contributor, name string, refs ...Claim) Claim {
	t.Helper()
	edges := make([]Edge, len(refs))
	for i, r := range refs {
		edges[i] = mustDerivEdge(t, r)
	}
	c, err := NewClaim(TypeDerivation(name), root).
		WithInlineContent([]byte(name)).
		WithEncoding(EncodingPlain).
		WithEdges(edges...).
		WithHeight(HeightOf(append([]Claim{root}, refs...)...)).
		Sign()
	require.NoError(t, err)
	return c
}

// reverseFixture builds a graph with a clear reverse shape:
//
//	root ← a,b (sources) ← x,y (derive from a) ← head (derives from x,y,b)
//
// plus bt, a claim ABOVE head (it references head) but OUTSIDE head's closure,
// so a reverse walk scoped at head must never reach it.
func reverseFixture(t *testing.T) (Universe, map[string]Claim) {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	a := srcClaim(t, root, "a")
	b := srcClaim(t, root, "b")
	x := derivClaim(t, root, "x", a)
	y := derivClaim(t, root, "y", a)
	head := derivClaim(t, root, "head", x, y, b)
	bt := derivClaim(t, root, "bt", head) // above head; not in closure(head)
	all := map[string]Claim{"root": root, "a": a, "b": b, "x": x, "y": y, "head": head, "bt": bt}
	for _, c := range []Claim{root, a, b, x, y, head, bt} {
		require.NoError(t, PutClaim(ctx, u, c))
	}
	return u, all
}

// TestQueryReverseConnectionsConfined: DirConnections from the scope head walks
// both ways but stays within the head's closure — it reaches every claim below
// the head and never bt (which references the head from outside the scope).
func TestQueryReverseConnectionsConfined(t *testing.T) {
	u, a := reverseFixture(t)
	q := Query{Select: Select{
		Branch: BranchUniverse, Head: a["head"].ID(), Claim: Anchors(a["head"].ID()),
		Path: []PathStep{{Min: Hops(0), Dir: DirConnections}},
	}}
	got := queryIDs(t, u, q)
	require.Equal(t, idsOf(a["head"], a["x"], a["y"], a["a"], a["b"], a["root"]), got)
	require.NotContains(t, got, a["bt"].ID().String(), "reverse must not climb above the scope root")
}

// TestQueryReverseUsesFindsReferrers: reach the sources (forward), then walk
// DirUses to the derivations that USE them — x, y (use a) and head (uses b).
// These are unreachable going forward from a source, so only reverse finds them.
func TestQueryReverseUsesFindsReferrers(t *testing.T) {
	u, a := reverseFixture(t)
	q := Query{Select: Select{
		Branch: BranchUniverse, Head: a["head"].ID(), Claim: Anchors(a["head"].ID()),
		Path: []PathStep{
			{Edges: []string{"derivation/*"}, Nodes: []string{"source/*"}},                   // forward → {a, b}
			{Dir: DirUses, Edges: []string{"derivation/*"}, Nodes: []string{"derivation/*"}}, // reverse → their users
		},
	}}
	require.Equal(t, idsOf(a["x"], a["y"], a["head"]), queryIDs(t, u, q))
}

// TestQueryForwardFromSourcesEmpty is the contrast: the same path with a forward
// (provenance) second step collects nothing — a source has no outgoing
// derivation edges, so only the reverse step above reaches its users.
func TestQueryForwardFromSourcesEmpty(t *testing.T) {
	u, a := reverseFixture(t)
	q := Query{Select: Select{
		Branch: BranchUniverse, Head: a["head"].ID(), Claim: Anchors(a["head"].ID()),
		Path: []PathStep{
			{Edges: []string{"derivation/*"}, Nodes: []string{"source/*"}},
			{Edges: []string{"derivation/*"}, Nodes: []string{"derivation/*"}}, // forward (default)
		},
	}}
	require.Empty(t, queryIDs(t, u, q))
}
