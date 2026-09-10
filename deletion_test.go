package ranke

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Lawful deletion end to end. Every deletion here removes REAL bytes through the port:
// a faked absence would prove nothing about the one `R-DGAP` exempts, which is why the
// port operation had to come before the exemption.

const past = "2020-01-01T00:00:00.000000000Z"

// datedSource stages a source claim scheduled for deletion at due, and a derivation
// citing it. NewEdge copies the date onto the citing edge (`R-DPLANNED`), which is what
// makes the gap explained wherever the claim is reached.
func datedSource(t *testing.T, u Universe, who Contributor, due string) (Claim, Claim) {
	t.Helper()
	ctx := context.Background()
	src, err := NewClaim(TypeSource("note"), who).
		WithInlineContent([]byte("to be deleted")).
		WithEncoding(EncodingPlain).
		WithField(FieldDeleteBy, due).
		WithHeight(HeightOf(who)).
		Sign()
	require.NoError(t, err)

	e, err := NewEdge(EdgeConfig{Reference: src.ID(), Referenced: src, Type: TypeDerivation("source")})
	require.NoError(t, err)
	require.Equal(t, due, mustField(t, e, FieldDeleteBy), "the citing edge copies the date")

	der, err := NewClaim(TypeDerivation("summary"), who).
		WithEdges(e).
		WithHeight(HeightOf(who, src)).
		Sign()
	require.NoError(t, err)
	require.NoError(t, u.PutClaims(ctx, []Claim{who, src, der}))
	return src, der
}

// citingEdge is the derivation edge that reaches the dated claim.
func citingEdge(t *testing.T, c Claim) Edge {
	t.Helper()
	for _, e := range c.Edges() {
		if e.TypeClass() == EdgeClassDerivation {
			return e
		}
	}
	t.Fatal("no derivation edge on the citing claim")
	return nil
}

// mustField reads a field that has to be there.
func mustField(t *testing.T, e Edge, name string) string {
	t.Helper()
	v, err := e.GetField(name)
	require.NoError(t, err)
	return v
}

// TestDeleteClaimsThroughThePort: a Universe declaring Delete removes a claim's bytes
// and its content, and says so through HasClaims — the operation the capability
// advertised and no method provided.
func TestDeleteClaimsThroughThePort(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	require.True(t, u.Capabilities().Delete)
	who := contributor(t)
	src, _ := datedSource(t, u, who, past)

	has, err := u.HasClaims(ctx, []Id{src.ID()})
	require.NoError(t, err)
	require.True(t, has[0])

	require.NoError(t, u.DeleteClaims(ctx, []Id{src.ID()}))
	has, err = u.HasClaims(ctx, []Id{src.ID()})
	require.NoError(t, err)
	require.False(t, has[0], "the bytes are gone")
	_, err = u.GetClaimsRaw(ctx, []Id{src.ID()})
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, u.DeleteClaims(ctx, []Id{src.ID()}), "idempotent: a second sweep finds nothing")
}

// TestDeleteContentThroughThePort: external content goes the same way, since nothing
// else names those bytes.
func TestDeleteContentThroughThePort(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	body := []byte("external bytes")
	hash, err := hashContent(body)
	require.NoError(t, err)
	require.NoError(t, u.PutContents(ctx, []ContentBlob{{Hash: hash, Content: body}}))

	require.NoError(t, u.DeleteContents(ctx, []Id{hash}))
	has, err := u.HasContents(ctx, []Id{hash})
	require.NoError(t, err)
	require.False(t, has[0])
	require.NoError(t, u.DeleteContents(ctx, []Id{hash}), "idempotent")
}

// TestSweepDeletesWhenDueAndLeavesTheDate: the planned form. The swept claim's bytes
// are gone and the citing edge still carries the date, so the gap stays explained
// wherever the claim is reached.
func TestSweepDeletesWhenDueAndLeavesTheDate(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	who := contributor(t)
	src, der := datedSource(t, u, who, past)

	res, err := DeletePlanned(ctx, u, []Id{der.ID()}, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, []Id{src.ID()}, res.Claims, "the dated claim, and only it")

	has, err := u.HasClaims(ctx, []Id{src.ID(), der.ID(), who.ID()})
	require.NoError(t, err)
	require.False(t, has[0], "the swept claim's bytes are gone")
	require.True(t, has[1], "the citing claim stays")
	require.True(t, has[2], "the contributor stays")

	// The explanation survives the claim it explains. Edges sort canonically by id, so
	// the citing edge is found by type rather than by position.
	reread, err := GetClaim(ctx, u, der.ID())
	require.NoError(t, err)
	require.Equal(t, past, mustField(t, citingEdge(t, reread), FieldDeleteBy))
}

// TestSweepLeavesADateNotYetDue: due is a date, so a schedule in the future is not a
// deletion now.
func TestSweepLeavesADateNotYetDue(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	who := contributor(t)
	src, der := datedSource(t, u, who, "2099-01-01T00:00:00.000000000Z")

	res, err := DeletePlanned(ctx, u, []Id{der.ID()}, time.Now().UTC())
	require.NoError(t, err)
	require.Empty(t, res.Claims)
	has, err := u.HasClaims(ctx, []Id{src.ID()})
	require.NoError(t, err)
	require.True(t, has[0])
}

// TestSweepNeverTouchesTheStructuralSet: `R-DSTRUCT` names four subtypes that may not
// be deleted, each being what another rule reads. A sweep must pass over them whatever
// a field says, so the fixtures carry a due delete_by and must survive anyway.
func TestSweepNeverTouchesTheStructuralSet(t *testing.T) {
	ctx := context.Background()
	who := contributor(t)
	for _, sub := range []string{"contributor", "branches", "delete", "expiry"} {
		t.Run(sub, func(t *testing.T) {
			u := NewMemoryUniverse()
			// The rules refuse such a claim, so it is built through AllowInvalid —
			// sealed like any other, and breaking `R-DSTRUCT` alone.
			bad, err := NewClaim("contribution/"+sub, who).
				WithCreatedAt(who.Node().CreatedAt()).
				WithField(FieldDeleteBy, past).
				WithHeight(HeightOf(who)).
				AllowInvalid().
				Sign()
			require.NoError(t, err)
			require.NoError(t, u.PutClaims(ctx, []Claim{who, bad}))

			_, err = DeletePlanned(ctx, u, []Id{bad.ID()}, time.Now().UTC())
			require.ErrorIs(t, err, ErrStructureNotDeletable, "the sweep refuses rather than deleting")
			has, err := u.HasClaims(ctx, []Id{bad.ID()})
			require.NoError(t, err)
			require.True(t, has[0], "contribution/%s survives a due delete_by", sub)
		})
	}
}

// TestSweepRefusedWithoutTheCapability: a Universe that cannot delete refuses rather
// than reporting a deletion it did not make.
func TestSweepRefusedWithoutTheCapability(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	who := contributor(t)
	_, der := datedSource(t, u, who, past)

	_, err := DeletePlanned(ctx, noDelete{u}, []Id{der.ID()}, time.Now().UTC())
	require.ErrorIs(t, err, ErrUnsupported)
}

// noDelete is a Universe whose medium cannot remove a key — a WORM bucket's shape.
type noDelete struct{ Universe }

func (n noDelete) Capabilities() Capabilities {
	c := n.Universe.Capabilities()
	c.Delete = false
	return c
}

func (n noDelete) DeleteClaims(context.Context, []Id) error   { return ErrUnsupported }
func (n noDelete) DeleteContents(context.Context, []Id) error { return ErrUnsupported }

// TestVerifyPassesOverAPlannedGap: `R-DGAP`'s exempting direction, proven by deleting
// real bytes. The date on the citing edge is the whole explanation.
func TestVerifyPassesOverAPlannedGap(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	who := contributor(t)
	_, der := datedSource(t, u, who, past)

	res, err := DeletePlanned(ctx, u, []Id{der.ID()}, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, res.Claims, 1)

	g, err := NewGraphFromClosure(ctx, der.ID(), u)
	require.NoError(t, err)
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "a copied delete_by explains the gap it left")
}

// TestVerifyFailsOnAnUnexplainedGap is the direction that protects the archive, and it
// matters more than the exemption: a missing claim nothing explains is
// indistinguishable from data loss. Same deletion, through the port, with the
// explanation withheld — so the failure is deliberate rather than incidental.
func TestVerifyFailsOnAnUnexplainedGap(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	who := contributor(t)

	// No delete_by anywhere: the edge copies nothing, so nothing explains the absence.
	src, err := NewClaim(TypeSource("note"), who).WithHeight(HeightOf(who)).Sign()
	require.NoError(t, err)
	e, err := NewEdge(EdgeConfig{Reference: src.ID(), Type: TypeDerivation("source")})
	require.NoError(t, err)
	der, err := NewClaim(TypeDerivation("summary"), who).
		WithEdges(e).WithHeight(HeightOf(who, src)).Sign()
	require.NoError(t, err)
	require.NoError(t, u.PutClaims(ctx, []Claim{who, src, der}))

	require.NoError(t, u.DeleteClaims(ctx, []Id{src.ID()}), "real bytes, really removed")

	g, err := NewGraphFromClosure(ctx, der.ID(), u)
	require.NoError(t, err)
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	fs := run.Failures()
	require.Len(t, fs, 1, "the unexplained gap fails")
	require.True(t, fs[0].ID.Equal(src.ID()), "and names the claim that is missing")
	require.ErrorIs(t, fs[0].Err, ErrUnexplainedGap)
}

// TestVerifyPassesOverARequestedGap: the other explanation `R-DGAP` admits — a
// contribution/delete mark against the id. The mark is found later in the walk than
// the gap it explains, which is why gaps are settled once the closure is exhausted.
func TestVerifyPassesOverARequestedGap(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	who := contributor(t)

	src, err := NewClaim(TypeSource("note"), who).WithHeight(HeightOf(who)).Sign()
	require.NoError(t, err)
	e, err := NewEdge(EdgeConfig{Reference: src.ID(), Type: TypeDerivation("source")})
	require.NoError(t, err)
	der, err := NewClaim(TypeDerivation("summary"), who).
		WithEdges(e).WithHeight(HeightOf(who, src)).Sign()
	require.NoError(t, err)

	// The mark: a contribution/delete claim naming the target through its delete edge.
	mark, err := NewClaim(NodeDelete, who).
		WithEdges(mustEdge(t, EdgeConfig{Reference: src.ID(), Type: EdgeTypeDelete})).
		WithHeight(HeightOf(who, src)).
		Sign()
	require.NoError(t, err)

	// One head reaching both the citing claim and the mark, as a branch would.
	head, err := NewClaim(NodeHead, who).WithEdges(
		mustEdge(t, EdgeConfig{Reference: der.ID(), Type: EdgeTypeHead}),
		mustEdge(t, EdgeConfig{Reference: mark.ID(), Type: EdgeTypeHead}),
	).WithHeight(HeightOf(who, der, mark)).Sign()
	require.NoError(t, err)
	require.NoError(t, u.PutClaims(ctx, []Claim{who, src, der, mark, head}))

	require.NoError(t, u.DeleteClaims(ctx, []Id{src.ID()}))

	g, err := NewGraphFromClosure(ctx, head.ID(), u)
	require.NoError(t, err)
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "a contribution/delete mark explains the gap")
}
