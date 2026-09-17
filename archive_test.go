package ranke

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the Archive — the immutable read-only snapshot
// RA_k = (𝒰, k) obtained from the Sequencer. Because an Archive only
// READS, it can be exercised in /* against a minimal struct-built Universe;
// the write path (Sequencer) lives in the adapters world and is out of unit
// scope. These pin claim reads, content reads (inline + external), and
// branch-table materialisation including the contribution/diff chain.

// putClaims stores cs in u, failing the test on error — the variadic test
// convenience the reference Universe (ranke.NewMemoryUniverse) lacks. Archive
// and closure read-tests build a claim set, put it, then read RA_k back.
func putClaims(t *testing.T, u Universe, cs ...Claim) {
	t.Helper()
	require.NoError(t, u.PutClaims(context.Background(), cs))
}

// branchEdge builds a contribution/branch edge naming a branch and pointing
// at its head.
func branchEdge(t *testing.T, name string, head Id) Edge {
	t.Helper()
	e, err := NewEdge(EdgeConfig{
		Reference: head,
		Type:      EdgeTypeBranch,
		Fields:    map[string]string{FieldName: name},
	})
	require.NoError(t, err)
	return e
}

// branchTable builds a contribution/branches claim (a branch table head)
// carrying the given branch edges, attributed to root.
func branchTable(t *testing.T, root Contributor, heads []Claim, edges ...Edge) Claim {
	t.Helper()
	c, err := NewClaim(NodeBranches, root).WithEdges(edges...).
		WithHeight(HeightOf(append([]Claim{root}, heads...)...)).Sign()
	require.NoError(t, err)
	return c
}

// --- NewArchive validation ---------------------------------------------

func TestNewArchiveValidation(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	putClaims(t, u, root)

	_, err := NewArchive(ctx, nil, root.ID())
	require.Error(t, err, "nil Universe rejected")
	_, err = NewArchive(ctx, u, nil)
	require.Error(t, err, "nil head id rejected")

	missing, _ := HashContent([]byte("absent"))
	_, err = NewArchive(ctx, u, missing)
	require.Error(t, err, "head not in the Universe rejected")
}

// --- claim & content reads ---------------------------------------------

func TestArchiveClaimAndContentReads(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "the body")
	bth := branchTable(t, root, []Claim{em}, branchEdge(t, "main", em.ID()))
	putClaims(t, u, root, em, bth)

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	// Membership across the head's closure.
	has, err := arc.HasClaim(ctx, em.ID())
	require.NoError(t, err)
	require.True(t, has, "email is in the head's closure")
	has, err = arc.HasClaim(ctx, root.ID())
	require.NoError(t, err)
	require.True(t, has, "contributor is in the closure")
	absent, _ := HashContent([]byte("nope"))
	has, err = arc.HasClaim(ctx, absent)
	require.NoError(t, err)
	require.False(t, has, "an unrelated id is not in the closure")

	// Lookup.
	got, err := arc.GetClaim(ctx, em.ID())
	require.NoError(t, err)
	require.True(t, got.ID().Equal(em.ID()))

	// Inline content served without touching content storage.
	r, err := arc.GetClaimContent(ctx, em.ID())
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, []byte("the body"), b)
}

func TestArchiveExternalContentRead(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)

	blob := []byte("external archive payload")
	hash, err := HashContent(blob)
	require.NoError(t, err)
	ext, err := NewClaim(TypeSource("blob"), root).WithExternalContent(hash, uint64(len(blob))).WithEncoding(EncodingOctetStream).WithHeight(HeightOf(root)).Sign()
	require.NoError(t, err)

	bth := branchTable(t, root, []Claim{ext}, branchEdge(t, "main", ext.ID()))
	putClaims(t, u, root, ext, bth)
	require.NoError(t, u.PutContents(ctx, []ContentBlob{{Hash: hash, Content: blob}}))

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	r, err := arc.GetClaimContent(ctx, ext.ID())
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, blob, b, "external content streams from the Universe through the Archive")
}

// --- diff materialisation on read --------------------------------------

// TestArchiveMaterializesDiff: reading a diff claim through the archive
// materialises the overlay (the universe applies DefaultMaterialize), so a
// delta that restates only one field inherits the predecessor's other
// fields AND its content. WithNotDiffMaterialized returns the bare delta.
func TestArchiveMaterializesDiff(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)

	base, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("the full base content")).
		WithEncoding(EncodingPlain).
		WithField("author", "alice").
		WithHeight(HeightOf(root)).
		Sign()
	require.NoError(t, err)
	// A revision restating only "rev" — content and "author" are inherited.
	delta, err := NewClaim(TypeSource("note"), root).
		WithDiff(base.ID()).
		WithField("rev", "2").
		WithHeight(HeightOf(root, base)).
		Sign()
	require.NoError(t, err)

	bth := branchTable(t, root, []Claim{delta}, branchEdge(t, "main", delta.ID()))
	putClaims(t, u, root, base, delta, bth)

	// The stored delta itself carries neither the inherited field nor the base
	// content — that's the space saving. Assert this BEFORE any materialised
	// read: the reference Universe holds live claims and materialises in place,
	// so a materialised read would stickily mutate this very object.
	raw, err := u.GetClaims(ctx, []Id{delta.ID()}, WithNotDiffMaterialized())
	require.NoError(t, err)
	_, err = raw[0].Node().GetField("author")
	require.Error(t, err, "the raw delta does not carry the inherited field")

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	got, err := arc.GetClaim(ctx, delta.ID())
	require.NoError(t, err)
	rev, err := got.Node().GetField("rev")
	require.NoError(t, err)
	require.Equal(t, "2", rev, "the delta's own field")
	author, err := got.Node().GetField("author")
	require.NoError(t, err)
	require.Equal(t, "alice", author, "field inherited from the predecessor")

	r, err := arc.GetClaimContent(ctx, delta.ID())
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, []byte("the full base content"), b, "content inherited from the predecessor")
}

// --- branch reads -------------------------------------------------------

func TestArchiveBranchReads(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	bth := branchTable(t, root, []Claim{em}, branchEdge(t, "main", em.ID()))
	putClaims(t, u, root, em, bth)

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	has, err := arc.HasBranch(ctx, "main")
	require.NoError(t, err)
	require.True(t, has)
	has, err = arc.HasBranch(ctx, "dev")
	require.NoError(t, err)
	require.False(t, has, "an unknown branch is absent")

	b, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, "main", b.Name())
	require.True(t, b.Head().Equal(em.ID()), "branch head points at the seed claim")

	_, err = arc.GetBranch(ctx, "dev")
	require.Error(t, err, "GetBranch on an unknown name errors")

	all, err := arc.GetBranches(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "main", all[0].Name())
}

// TestArchiveQueryResolvesBranch: Archive.Query resolves Select.Branch to its
// head and confines the read to that closure.
func TestArchiveQueryResolvesBranch(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	bth := branchTable(t, root, []Claim{em}, branchEdge(t, "main", em.ID()))
	putClaims(t, u, root, em, bth)

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	// "main" → head em; closure is {em, root}.
	rs, err := arc.Query(ctx, Query{Select: Select{Branch: "main"}})
	require.NoError(t, err)
	require.Equal(t, idsOf(em, root), idSet(drain(t, rs)))

	// $archive → head bth; closure adds the branch table itself.
	rs, err = arc.Query(ctx, Query{Select: Select{Branch: BranchArchive}})
	require.NoError(t, err)
	require.Equal(t, idsOf(bth, em, root), idSet(drain(t, rs)))
}

// A read serves the content Output.Content asks for (`R-QCONTENT`), through the archive
// rather than only in the codec — the rule was stated for a whole query.
func TestArchiveQueryServesTheContentCap(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	const body = "seed content, long enough to cut"
	em := srcClaim(t, root, body)
	bth := branchTable(t, root, []Claim{em}, branchEdge(t, "main", em.ID()))
	putClaims(t, u, root, em, bth)

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	// The claim under test carries inline content; the branch table alongside it does
	// not, so a run covers both a record with content and one without.
	read := func(t *testing.T, oc *OutputContent) []byte {
		t.Helper()
		q := Query{Select: Select{Branch: "main"}, Output: Output{
			Detail: DetailClaims, Form: FormOriginal, Encoding: ResultCBOR, Content: oc,
		}}
		require.NoError(t, ValidateQuery(q))
		rs, err := arc.Query(ctx, q)
		require.NoError(t, err)
		for _, r := range drain(t, rs) {
			if r.ClaimNative.ID().String() == em.ID().String() {
				return r.ClaimEncoded
			}
		}
		t.Fatal("the claim under test is missing from the results")
		return nil
	}

	stored, err := em.EncodeCBOR(FormOriginal)
	require.NoError(t, err)

	// `R-QCANON`: content in full is the stored bytes, so this form and only this form
	// hashes back to the id.
	t.Run("in full is S(v)", func(t *testing.T) {
		require.Equal(t, stored, read(t, &OutputContent{Max: 0, Overflow: OverflowOmit}))
	})

	t.Run("absent inlines nothing", func(t *testing.T) {
		got := read(t, nil)
		require.NotEqual(t, stored, got)
		c, err := decodeSerializedClaim(nil, got)
		require.NoError(t, err)
		inline, err := c.Node().GetInlineContent()
		require.NoError(t, err)
		require.Empty(t, inline, "no content came with it")
		require.Equal(t, uint64(len(body)), c.Node().GetContentSize(),
			"the size still declares what the claim holds")
	})

	t.Run("cutoff inlines up to the cap", func(t *testing.T) {
		c, err := decodeSerializedClaim(nil, read(t, &OutputContent{Max: 4, Overflow: OverflowCutoff}))
		require.NoError(t, err)
		inline, err := c.Node().GetInlineContent()
		require.NoError(t, err)
		require.Equal(t, []byte(body[:4]), inline)
		require.Equal(t, uint64(len(body)), c.Node().GetContentSize())
	})

	t.Run("omit inlines none of it", func(t *testing.T) {
		c, err := decodeSerializedClaim(nil, read(t, &OutputContent{Max: 4, Overflow: OverflowOmit}))
		require.NoError(t, err)
		inline, err := c.Node().GetInlineContent()
		require.NoError(t, err)
		require.Empty(t, inline)
	})

	// Content within the cap never overflows, so overflow has nothing to apply to.
	t.Run("content within the cap arrives whole", func(t *testing.T) {
		for _, ov := range []Overflow{OverflowCutoff, OverflowOmit} {
			c, err := decodeSerializedClaim(nil, read(t, &OutputContent{Max: len(body), Overflow: ov}))
			require.NoError(t, err)
			inline, err := c.Node().GetInlineContent()
			require.NoError(t, err)
			require.Equal(t, []byte(body), inline, string(ov))
		}
	})
}

// TestQueryClaimDoesNotScope: Select.Claim starts a traversal and scopes nothing,
// so it cannot stand in for the Select.Head that $universe requires.
func TestQueryClaimDoesNotScope(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	bth := branchTable(t, root, []Claim{em}, branchEdge(t, "main", em.ID()))
	putClaims(t, u, root, em, bth)

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	step := []PathStep{{Min: Hops(0)}}
	_, err = arc.Query(ctx, Query{Select: Select{Branch: BranchUniverse, Claim: Anchors(em.ID()), Path: step}})
	require.ErrorIs(t, err, ErrQueryNoHead, "$universe needs a Head; a Claim is not one")

	// Head bounds the read, Claim starts it — orthogonal, so both are honoured.
	rs, err := arc.Query(ctx, Query{Select: Select{
		Branch: BranchUniverse, Head: bth.ID(), Claim: Anchors(em.ID()), Path: step}})
	require.NoError(t, err)
	require.Equal(t, idsOf(em, root), idSet(drain(t, rs)),
		"the walk starts at em, so bth is in scope but never reached")
}

// TestArchiveBranchDiffChain: a branch table that is a contribution/diff
// over a predecessor inherits the predecessor's branches and adds its own —
// the materialisation the Archive performs newest-over-oldest.
func TestArchiveBranchDiffChain(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	emMain := srcClaim(t, root, "main head")
	emDev := srcClaim(t, root, "dev head")

	// table1: just main.
	table1 := branchTable(t, root, []Claim{emMain}, branchEdge(t, "main", emMain.ID()))
	// table2: a diff over table1 that adds dev.
	table2, err := NewClaim(NodeBranches, root).
		WithDiff(table1.ID()).
		WithEdges(branchEdge(t, "dev", emDev.ID())).
		WithHeight(HeightOf(root, table1, emDev)).
		Sign()
	require.NoError(t, err)

	putClaims(t, u, root, emMain, emDev, table1, table2)

	arc, err := NewArchive(ctx, u, table2.ID())
	require.NoError(t, err)

	all, err := arc.GetBranches(ctx)
	require.NoError(t, err)
	names := []string{}
	for _, b := range all {
		names = append(names, b.Name())
	}
	require.ElementsMatch(t, []string{"main", "dev"}, names,
		"diff table inherits main and adds dev")

	main, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)
	require.True(t, main.Head().Equal(emMain.ID()), "inherited main head preserved")
	dev, err := arc.GetBranch(ctx, "dev")
	require.NoError(t, err)
	require.True(t, dev.Head().Equal(emDev.ID()), "added dev head resolved")
}
