package matrix_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/matrix"
	"github.com/rankegraph/ranke-go/tests/rql"
)

// One representative per read shape: scan, anchored traversal, unanchored traversal.

// TestShapeScan: a filter over a branch returns the claims matching it.
func TestShapeScan(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyDiff(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
		})
		require.ElementsMatch(t, idStrings(m.Sources), got,
			"a scan returns exactly the branch's claims matching the filter")
	})
}

// TestShapeScanRejectsPathOutput: a read without Path steps rejects Shape: path.
// A route only exists where steps state one, so backends would each invent their own.
func TestShapeScanRejectsPathOutput(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyDiff(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		arc, err := ranke.NewArchive(context.Background(), u, m.Head)
		require.NoError(t, err)
		_, err = arc.Query(context.Background(), ranke.Query{
			Select: ranke.Select{Branch: rql.Branch},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
			Output: ranke.Output{Shape: ranke.ShapePath},
		})
		require.Error(t, err, "a pathless read has no route to return")
	})
}

// TestShapeAnchoredTraversal: from one claim, derivation edges reach the sources
// it cites.
func TestShapeAnchoredTraversal(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyDiff(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		base := diffPredecessor(t, u, m.DiffChainHead)
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: ranke.Anchors(base),
				Path: []ranke.PathStep{{Edges: []string{"derivation/*"}, Nodes: []string{"source/*"}}}},
		})
		require.ElementsMatch(t, idStrings(m.Sources), got,
			"the derivation edges of this claim reach exactly the sources it cites")
	})
}

// TestShapeUnanchoredTraversal: a pattern without a starting point matches wherever
// it occurs in the scope — here the relation reaches both entities it wires.
func TestShapeUnanchoredTraversal(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyRelation(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch,
				Path: []ranke.PathStep{{Edges: []string{"relation/*"}, Nodes: []string{"entity/person"}}}},
		})
		require.ElementsMatch(t, idStrings(m.Entities), got,
			"the relation's edges reach both entities it wires, from wherever the pattern occurs")
	})
}

// TestShapePathRecrossesAnEdge: a step may re-traverse an edge an earlier step used
// (`R-QFRONTIER`). Two steps in opposing directions is the smallest case, and a lowering
// that spans one pattern over the whole route drops it — only under Shape: path.
func TestShapePathRecrossesAnEdge(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyDiff(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		delta := m.DiffChainHead
		base := diffPredecessor(t, u, delta)

		answer, err := rql.Run(context.Background(), u, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: ranke.Anchors(delta), Path: []ranke.PathStep{
				{Edges: []string{"contribution/diff"}, Min: ranke.Hops(1), Max: 1},
				{Edges: []string{"contribution/diff"}, Dir: ranke.DirUses, Min: ranke.Hops(1), Max: 1},
			}},
			Output: ranke.Output{Shape: ranke.ShapePath, Detail: ranke.DetailID},
		}, m.Head)
		require.NoError(t, err)

		require.Len(t, answer, 1, "one route: out to the base along the diff edge, then back")
		require.Contains(t, answer[0],
			"path["+delta.String()+" "+base.String()+" "+delta.String()+"]",
			"the route re-crosses the edge, which the boundary between steps permits")
	})
}

// idStrings renders ids for comparison against a reached set.
func idStrings(ids []ranke.Id) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}
