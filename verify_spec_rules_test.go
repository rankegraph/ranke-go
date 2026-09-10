package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The two verifiers tightened against ranke-graph 0.16.2. Neither shape occurs in a
// generated archive, which is how both sat unnoticed: the matrix builds only what
// the generator emits, so a rule it never exercises is a rule nothing holds it to.

// verifyOne runs the closure verifier over cs and returns the failures.
func verifyOne(t *testing.T, head Claim, cs ...Claim) []Failure {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	for _, c := range cs {
		require.NoError(t, PutClaim(ctx, u, c))
	}
	g, err := NewGraphFromClosure(ctx, head.ID(), u)
	require.NoError(t, err)
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	return run.Failures()
}

// table builds a contribution/branches claim reaching prev through edge, which is
// how a spine link is made — and, with any other edge type, how `V-TABLEREF` is broken.
func table(t *testing.T, ctr Contributor, prev Claim, edgeType string) Claim {
	t.Helper()
	e, err := NewEdge(EdgeConfig{Reference: prev.ID(), Type: edgeType})
	require.NoError(t, err)
	c, err := NewClaim(NodeBranches, ctr).
		WithEdges(e).
		WithHeight(HeightOf(ctr, prev)).
		Sign()
	require.NoError(t, err)
	return c
}

// TestTableRefThroughLineageEdgePasses is the control: a table reaching its
// predecessor through contribution/diff is the chain `R-C6MERGE` builds.
func TestTableRefThroughLineageEdgePasses(t *testing.T) {
	ctr := contributor(t)
	base, err := NewClaim(NodeBranches, ctr).WithHeight(HeightOf(ctr)).Sign()
	require.NoError(t, err)
	next := table(t, ctr, base, EdgeTypeDiff)

	require.Empty(t, verifyOne(t, next, ctr, base, next))
}

// TestTableRefThroughOtherEdgeFails is the tightening: `V-TABLEREF` permits a table to
// reach a table through its contribution/diff or contribution/branches edge and
// nothing else. Exempting the referencing claim wholesale let any edge type through,
// which took the spine off its own layer.
func TestTableRefThroughOtherEdgeFails(t *testing.T) {
	ctr := contributor(t)
	base, err := NewClaim(NodeBranches, ctr).WithHeight(HeightOf(ctr)).Sign()
	require.NoError(t, err)
	next := table(t, ctr, base, TypeDerivation("source"))

	fs := verifyOne(t, next, ctr, base, next)
	require.Len(t, fs, 1, "a table reaching a table through a derivation edge must fail")
	require.ErrorIs(t, fs[0].Err, ErrRefsBranchTable)
}

// TestOpenContributionSubtypeMayScheduleDeletion is the loosening: `R-DSTRUCT` names
// four subtypes and says any other claim MAY carry delete_by. Refusing by class
// refused an application's own contribution/* claim, whose subtype is open
// vocabulary (`V-TYPE`).
func TestOpenContributionSubtypeMayScheduleDeletion(t *testing.T) {
	ctr := contributor(t)
	c, err := NewClaim("contribution/annotation", ctr).
		WithInlineContent([]byte("an application's own claim")).
		WithEncoding(EncodingPlain).
		WithField(FieldDeleteBy, "2027-05-08T00:00:00.000000000Z").
		WithHeight(HeightOf(ctr)).
		Sign()
	require.NoError(t, err, "an open contribution subtype may schedule its own deletion")
	require.Empty(t, verifyOne(t, c, ctr, c))
}

// TestFirstTableHeightIsFixed is `V-ARCHIVEHEIGHT`: an archive's first branch table
// stands on its contributor edge alone, resolving to the initial claim at height 0, so
// height 1 is the only value it can carry. The rule reads that off the record — no
// seed, no bookmark, no walk — which is why it runs ahead of the re-derivation.
func TestFirstTableHeightIsFixed(t *testing.T) {
	ctr := contributor(t)
	first, err := NewClaim(NodeBranches, ctr).WithHeight(2).Sign()
	require.NoError(t, err, "the builder takes a height as given; the verifier judges it")

	fs := verifyOne(t, first, ctr, first)
	require.Len(t, fs, 1)
	require.ErrorIs(t, fs[0].Err, ErrArchiveFirstTableHeight)
}

// TestFirstTableAtHeightOnePasses is the control, and the shape every Sequencer here
// mints at bootstrap.
func TestFirstTableAtHeightOnePasses(t *testing.T) {
	ctr := contributor(t)
	first, err := NewClaim(NodeBranches, ctr).WithHeight(HeightOf(ctr)).Sign()
	require.NoError(t, err)
	require.Empty(t, verifyOne(t, first, ctr, first))
}

// TestLaterTableHeightIsUnconstrained is the rule's bound: it speaks about the FIRST
// table alone, told apart by carrying no lineage edge (`R-C6MERGE`). A revision over a
// predecessor stands at whatever height its references derive, and 2 here is that.
func TestLaterTableHeightIsUnconstrained(t *testing.T) {
	ctr := contributor(t)
	first, err := NewClaim(NodeBranches, ctr).WithHeight(HeightOf(ctr)).Sign()
	require.NoError(t, err)
	next := table(t, ctr, first, EdgeTypeBranches)

	fs := verifyOne(t, next, ctr, first, next)
	require.Empty(t, fs)
	require.Equal(t, uint64(2), next.Node().Height(), "a second table stands above the first")
}

// TestNamedContributionSubtypesRefuseDeletion is that loosening's bound: the four
// `R-DSTRUCT` names are still refused, each being what another rule reads.
func TestNamedContributionSubtypesRefuseDeletion(t *testing.T) {
	ctr := contributor(t)
	for _, sub := range []string{"contributor", "branches", "delete", "expiry"} {
		t.Run(sub, func(t *testing.T) {
			_, err := NewClaim("contribution/"+sub, ctr).
				WithField(FieldDeleteBy, "2027-05-08T00:00:00.000000000Z").
				WithHeight(HeightOf(ctr)).
				Sign()
			require.ErrorIs(t, err, ErrStructureNotDeletable)
		})
	}
}
