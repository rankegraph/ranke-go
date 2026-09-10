package ranke

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the fluent ClaimBuilder API (NewClaim + the
// With* setters) and the value-semantics that make chaining safe. The
// struct-literal form is exercised throughout the other suites; this pins
// the chained form, which is equal public API.

func mustInline(t *testing.T, n Node) []byte {
	t.Helper()
	b, err := n.GetInlineContent()
	require.NoError(t, err)
	return b
}

// TestBuilderFluentChain: every setter in a chain is reflected in the
// built claim.
func TestBuilderFluentChain(t *testing.T) {
	alice := contributor(t)
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("body")).
		WithEncoding("text/markdown").
		WithField("topic", "news").
		WithCreatedAt(at).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)

	require.Equal(t, "source/note", c.Node().Type())
	require.Equal(t, "text/markdown", c.Node().Encoding())
	v, err := c.Node().GetField("topic")
	require.NoError(t, err)
	require.Equal(t, "news", v)
	require.True(t, c.Node().CreatedAt().Equal(at))
	require.Equal(t, []byte("body"), mustInline(t, c.Node()))
}

// TestBuilderWithTypeOverride: WithType replaces the seeded type.
func TestBuilderWithTypeOverride(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithType(TypeSource("email")).
		WithInlineContent([]byte("x")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)
	require.Equal(t, "source/email", c.Node().Type())
}

// TestBuilderWithExternalContent: the external-content setter records hash
// and size without inline bytes.
func TestBuilderWithExternalContent(t *testing.T) {
	alice := contributor(t)
	hash, err := HashContent([]byte("external blob"))
	require.NoError(t, err)

	c, err := NewClaim(TypeSource("blob"), alice).
		WithExternalContent(hash, 13).
		WithEncoding(EncodingOctetStream).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)
	require.Equal(t, ContentExternal, c.Node().ContentKind())
	require.Equal(t, uint64(13), c.Node().GetContentSize())
	require.True(t, hash.Equal(c.Node().GetContentHash()))
}

// TestBuilderWithEdges: WithEdges attaches provided edges.
func TestBuilderWithEdges(t *testing.T) {
	alice := contributor(t)
	src := srcClaim(t, alice, "s")
	c, err := NewClaim(TypeEntity("person"), alice).
		WithInlineContent([]byte("Alice")).
		WithEncoding(EncodingPlain).
		WithEdges(mustDerivEdge(t, src)).
		WithHeight(HeightOf(alice, src)).
		Sign()
	require.NoError(t, err)
	require.Len(t, c.Edges(EdgeFilterType{Type: "derivation/source"}), 1)
}

// TestBuilderWithFieldImmutable: With* returns a copy — mutating one
// derived builder never touches a sibling built from the same base. This
// is what makes it safe to fan out from a partially-configured builder.
func TestBuilderWithFieldImmutable(t *testing.T) {
	alice := contributor(t)
	base := NewClaim(TypeSource("note"), alice).WithInlineContent([]byte("x")).WithEncoding(EncodingPlain).WithHeight(HeightOf(alice))

	withField := base.WithField("a", "1")

	c1, err := base.Sign()
	require.NoError(t, err)
	c2, err := withField.Sign()
	require.NoError(t, err)

	require.False(t, c1.Node().HasField("a"), "the base builder was not mutated")
	require.True(t, c2.Node().HasField("a"), "the derived builder has the field")
}

// TestBuilderSignedRootContributor: a signed root contributor carries its
// pubkey AS its (inline) content (§5.7) and signs with the matching key.
func TestBuilderSignedRootContributor(t *testing.T) {
	priv, pubkey := ed25519Keys(t)
	c, err := NewClaim(NodeContributor, nil).
		WithInlineContent(pubkey). // the pubkey IS the content
		WithEncoding(EncodingOctetStream).
		WithSigningKey(priv).
		Sign()
	require.NoError(t, err)
	require.True(t, c.IsContributor())
	require.Equal(t, "sha2-256", c.ID().Algorithm(), "an id hashes the envelope")
}

// TestContributorExternalPubkey: a contributor's pubkey IS its content
// (§5.7) and content may be external (§4.4) — inline is not assumed.
// AsContributor resolves the pubkey transparently (inline from the node,
// external streamed from the Universe) and checks the signing key against
// the resolved bytes.
func TestContributorExternalPubkey(t *testing.T) {
	ctx := context.Background()
	priv, pubkey := ed25519Keys(t)
	hash, err := HashContent(pubkey)
	require.NoError(t, err)

	// Declaring an (external) pubkey with NO signing key is rejected: the builder
	// recognises the pubkey (content_hash) and demands the key that matches it.
	_, err = NewClaim(NodeContributor, nil).
		WithExternalContent(hash, uint64(len(pubkey))).
		Sign()
	require.Error(t, err, "an external pubkey with no signing key is rejected")

	// With the matching key it signs: the builder checks the key's pubkey
	// hashes to the declared content_hash, so no Universe is needed at build.
	claim, err := NewClaim(NodeContributor, nil).
		WithExternalContent(hash, uint64(len(pubkey))).
		WithEncoding(EncodingOctetStream).
		Sign(priv)
	require.NoError(t, err, "external pubkey + matching key signs")
	require.Equal(t, "sha2-256", claim.ID().Algorithm(), "an id hashes the envelope")

	u := newStubUniverse()
	require.NoError(t, u.PutContents(ctx, []ContentBlob{{Hash: hash, Content: pubkey}}))

	// Resolves the external pubkey from the Universe and matches the key.
	c, err := claim.AsContributor(ctx, u, priv)
	require.NoError(t, err, "external pubkey resolves from the Universe and matches the key")
	require.True(t, c.IsContributor())

	// A wrong key is rejected against the resolved external pubkey.
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = claim.AsContributor(ctx, u, wrongPriv)
	require.Error(t, err, "the key is checked against the resolved external pubkey")

	// Without the Universe the external pubkey can't be resolved.
	_, err = claim.AsContributor(ctx, nil, priv)
	require.Error(t, err, "external pubkey needs a Universe to resolve")

	// The payoff: a claim ATTRIBUTED to the external-pubkey contributor signs
	// with no Universe — resolveSigningPubkey reads the pubkey cached on the
	// Contributor (Pubkey()) at AsContributor time, agnostic of whether it
	// was inline or external.
	child, err := NewClaim(TypeSource("note"), c).
		WithInlineContent([]byte("body")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(c)).
		Sign() // no explicit key, no Universe — both come from c
	require.NoError(t, err, "child of an external-pubkey contributor signs from the cached pubkey")
	require.Equal(t, "sha2-256", child.ID().Algorithm(), "an id hashes the envelope")
	require.True(t, child.Edges(EdgeFilterType{Type: EdgeTypeContributor})[0].Reference().Equal(c.ID()),
		"attributed to the external-pubkey contributor")
}

// --- claim views --------------------------------------------------------

// TestClaimContributor: a normal claim exposes the contributor it was
// attributed to.
func TestClaimContributor(t *testing.T) {
	alice := contributor(t)
	c := srcClaim(t, alice, "s")
	require.NotNil(t, c.Contributor())
	require.True(t, c.Contributor().ID().Equal(alice.ID()))
}

// TestSignedContributorForwarding: WithSigningKey wraps a Contributor and
// forwards every Claim method to the underlying claim while carrying the
// session key.
func TestSignedContributorForwarding(t *testing.T) {
	alice, priv := newSignedContributor(t)
	wrapped := WithSigningKey(alice, priv)

	require.Equal(t, priv, wrapped.SigningKey(), "carries the session key")
	require.True(t, wrapped.IsContributor())
	require.True(t, wrapped.ID().Equal(alice.ID()), "id forwarded")
	require.Equal(t, "contribution/contributor", wrapped.Node().Type(), "node forwarded")
	require.Empty(t, wrapped.Edges(), "edges forwarded (root has none)")

	enc, err := wrapped.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	require.NotEmpty(t, enc, "Encode forwarded")

	view, err := wrapped.AsContributor(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, view.ID().Equal(alice.ID()), "AsContributor forwarded")
}

// ============================================================
// Construction guarantees — invariants ClaimBuilder.Sign enforces.
// ============================================================

// --- attribution, which is what provenance means here -------------------

// TestDerivationEdgeBuildsOnEveryClass: these classes build carrying a derivation/*
// edge to a source. Once the edge was optional this stopped being a requirement's
// control and became the other bound — citing a source stays legal.
func TestDerivationEdgeBuildsOnEveryClass(t *testing.T) {
	ctr := contributor(t)
	source := srcClaim(t, ctr, "the source")
	for _, typ := range []string{
		TypeDerivation("summary"),
		TypeEntity("person"),
		TypeRelation("likes"),
	} {
		_, err := NewClaim(typ, ctr).
			WithInlineContent([]byte("...")).
			WithEncoding(EncodingPlain).
			WithEdges(mustDerivEdge(t, source)).
			WithHeight(HeightOf(ctr, source)).
			Sign()
		require.NoError(t, err, "%s with a derivation/* edge is valid", typ)
	}
}

// TestEveryClassCarriesItsContributor: this was source/*'s test alone, and it read as
// "source is the class that needs no derivation edge" — a negative control that went
// vacuous the moment every class stopped needing one. What it actually pins survives
// and now covers all of them: `V-ROOT`'s contributor edge is on every claim, so a
// claim citing no source is still attributed to whoever made it.
func TestEveryClassCarriesItsContributor(t *testing.T) {
	ctr := contributor(t)
	for _, typ := range []string{
		TypeSource("note"),
		TypeDerivation("summary"),
		TypeEntity("person"),
		TypeRelation("likes"),
	} {
		t.Run(typ, func(t *testing.T) {
			c, err := NewClaim(typ, ctr).WithInlineContent([]byte("x")).
				WithEncoding(EncodingPlain).WithHeight(HeightOf(ctr)).Sign()
			require.NoError(t, err, "%s builds without a derivation edge", typ)
			require.Len(t, c.Edges(EdgeFilterType{Type: EdgeTypeContributor}), 1,
				"%s still names who made it", typ)
		})
	}
}

// TestInitialClaimMayLackProvenance: a contribution/contributor claim MAY
// have no edges — it is an initial claim, provenance-free and valid (§4.5).
// This is a datatype property, not a uniqueness rule: a closure may hold
// several initial claims (e.g. two merged archives), and all verify. The
// "only the first founding claim" restriction is a write-path concern
// enforced by the Sequencer, not here.
func TestInitialClaimMayLackProvenance(t *testing.T) {
	root := contributor(t)
	require.Empty(t, root.Edges(), "an initial claim (root contributor) has no edges")
	require.True(t, root.IsContributor())
}

// TestContentClaimRequiresContributor: every non-initial claim must carry
// provenance. A content claim (source/*) always gets a contribution/
// contributor edge, and the builder refuses one with no contributor.
func TestContentClaimRequiresContributor(t *testing.T) {
	root := contributor(t)
	child := srcClaim(t, root, "x")
	require.NotEmpty(t, child.Edges(), "a non-initial claim always carries provenance")

	_, err := NewClaim(TypeSource("note"), nil).WithInlineContent([]byte("x")).Sign()
	require.Error(t, err, "only the root contribution/contributor may omit a contributor")
}

// --- single-edge cardinality guarantees --------------------------------

// TestClaimRejectsTwoContributors: a claim is attributed to exactly one
// contributor. Attributing to alice auto-builds one contribution/
// contributor edge; a second one (to bob) must be rejected.
func TestClaimRejectsTwoContributors(t *testing.T) {
	alice := contributor(t)
	bob := contributor(t)
	extra := mustEdge(t, EdgeConfig{Reference: bob.ID(), Type: EdgeTypeContributor})

	_, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("x")).
		WithEdges(extra). // alice via attribution + bob via explicit edge = two contributors
		WithHeight(HeightOf(alice, bob)).
		Sign()
	require.Error(t, err, "a claim may carry only one contribution/contributor edge")
}

// TestClaimRejectsTwoDiffEdges: a claim overlays at most one predecessor,
// so it may carry only one contribution/diff edge.
func TestClaimRejectsTwoDiffEdges(t *testing.T) {
	alice := contributor(t)
	pred1 := srcClaim(t, alice, "v1")
	pred2 := srcClaim(t, alice, "v1-alt")
	extraDiff := mustEdge(t, EdgeConfig{Reference: pred2.ID(), Type: EdgeTypeDiff})

	_, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("v2")).
		WithDiff(pred1.ID()). // adds one contribution/diff edge...
		WithEdges(extraDiff). // ...this adds a second
		WithHeight(HeightOf(alice, pred1, pred2)).
		Sign()
	require.Error(t, err, "a claim may overlay at most one predecessor (one contribution/diff edge)")
}

// --- §5.7 signing guarantees at the builder ----------------------------

// TestSignerMustMatchPubkey: Sign rejects a signer whose public key does
// not match the resolved contributor's pubkey — Bob cannot sign a claim
// attributed to Alice.
func TestSignerMustMatchPubkey(t *testing.T) {
	alice, _ := newSignedContributor(t)
	_, bobPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	_, err = NewClaim(TypeSource("email"), alice).
		WithInlineContent([]byte("hi")).
		WithHeight(HeightOf(alice)).
		Sign(bobPriv) // Bob's key, Alice's contributor
	require.Error(t, err, "signer/contributor pubkey mismatch must be rejected")
}

// TestSignedContributorRequiresSigner: a contributor whose content is a
// pubkey but which is given no signing key is rejected — the content says
// "signed by this key" while the call never provides it.
func TestSignedContributorRequiresSigner(t *testing.T) {
	_, pubkey := ed25519Keys(t)

	_, err := NewClaim(NodeContributor, nil).
		WithInlineContent(pubkey). // content is a pubkey...
		// ...but no Sign(priv) — signing key withheld on purpose
		Sign()
	require.Error(t, err, "a pubkey-as-content with no signer must be rejected")
}

// ============================================================
// Diff overlay — WithDiff builds a claim that overlays a predecessor,
// restating only what differs (node content/title/fields and/or edges) and
// carrying a contribution/diff edge naming it. Full materialisation (the
// loader applying the diff chain) is exercised where that lands.
// ============================================================

// TestClaimDiffEdgeNamesPredecessor: WithDiff adds exactly one
// contribution/diff edge to the predecessor, coexisting with the auto
// contribution/contributor edge.
func TestClaimDiffEdgeNamesPredecessor(t *testing.T) {
	alice := contributor(t)
	pred := srcClaim(t, alice, "v1")

	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("v2")).
		WithEncoding(EncodingPlain).
		WithDiff(pred.ID()).
		WithHeight(HeightOf(alice, pred)).
		Sign()
	require.NoError(t, err)

	diffs := c.Edges(EdgeFilterType{Type: EdgeTypeDiff})
	require.Len(t, diffs, 1, "exactly one contribution/diff edge")
	require.True(t, diffs[0].Reference().Equal(pred.ID()), "it names the predecessor")
	require.Len(t, c.Edges(EdgeFilterType{Type: EdgeTypeContributor}), 1,
		"the diff edge coexists with the contributor edge")
}

// TestClaimDiffRestatesNodeChanges: a diff restates changed node content
// and fields — the built claim carries the new values verbatim.
func TestClaimDiffRestatesNodeChanges(t *testing.T) {
	alice := contributor(t)
	pred := srcClaim(t, alice, "old body")

	c, err := NewClaim(TypeSource("note"), alice).
		WithDiff(pred.ID()).
		WithInlineContent([]byte("new body")).
		WithEncoding(EncodingPlain).
		WithField("status", "revised").
		WithHeight(HeightOf(alice, pred)).
		Sign()
	require.NoError(t, err)

	require.Equal(t, []byte("new body"), mustInline(t, c.Node()))
	v, err := c.Node().GetField("status")
	require.NoError(t, err)
	require.Equal(t, "revised", v)
}

// TestClaimDiffAddsEdges: a diff adds new edges alongside the diff edge —
// edges participate in the overlay just as node content does. The added
// edge is NAMED, since a diff claim's edges are overlaid by name (see
// TestClaimDiffRejectsUnnamedEdge).
func TestClaimDiffAddsEdges(t *testing.T) {
	alice := contributor(t)
	pred := srcClaim(t, alice, "v1")
	src := srcClaim(t, alice, "cited source")
	named := mustEdge(t, EdgeConfig{
		Reference: src.ID(),
		Type:      TypeDerivation("source"),
		Fields:    map[string]string{FieldName: "primary-source"},
	})

	c, err := NewClaim(TypeEntity("person"), alice).
		WithDiff(pred.ID()).
		WithInlineContent([]byte("Alice v2")).
		WithEncoding(EncodingPlain).
		WithEdges(named).
		WithHeight(HeightOf(alice, pred, src)).
		Sign()
	require.NoError(t, err)

	require.Len(t, c.Edges(EdgeFilterType{Type: EdgeTypeDiff}), 1, "diff edge present")
	require.Len(t, c.Edges(EdgeFilterType{Type: "derivation/source"}), 1, "added edge present")
}

// TestClaimDiffRejectsUnnamedEdge: a diff claim overlays its predecessor by
// re-stating edges, addressed by name. So every edge a diff claim carries
// must be named — EXCEPT contribution/diff and contribution/contributor,
// the singletons (limited to one each, so unambiguous without a name). A
// diff claim carrying an unnamed derivation edge must be rejected.
func TestClaimDiffRejectsUnnamedEdge(t *testing.T) {
	alice := contributor(t)
	pred := srcClaim(t, alice, "v1")
	src := srcClaim(t, alice, "cited source")
	unnamed := mustEdge(t, EdgeConfig{Reference: src.ID(), Type: TypeDerivation("source")}) // no name field

	_, err := NewClaim(TypeEntity("person"), alice).
		WithDiff(pred.ID()).
		WithInlineContent([]byte("Alice v2")).
		WithEdges(unnamed).
		WithHeight(HeightOf(alice, pred, src)).
		Sign()
	require.Error(t, err, "a diff claim may not carry an unnamed (non-singleton) edge")
}

// TestClaimDiffAllowsNamedEdge: the positive control — the same diff claim
// with the edge NAMED builds fine, since a named edge is overlay-addressable.
func TestClaimDiffAllowsNamedEdge(t *testing.T) {
	alice := contributor(t)
	pred := srcClaim(t, alice, "v1")
	src := srcClaim(t, alice, "cited source")
	named := mustEdge(t, EdgeConfig{
		Reference: src.ID(),
		Type:      TypeDerivation("source"),
		Fields:    map[string]string{FieldName: "primary-source"},
	})

	_, err := NewClaim(TypeEntity("person"), alice).
		WithDiff(pred.ID()).
		WithInlineContent([]byte("Alice v2")).
		WithEncoding(EncodingPlain).
		WithEdges(named).
		WithHeight(HeightOf(alice, pred, src)).
		Sign()
	require.NoError(t, err, "a named edge on a diff claim is allowed")
}

// TestClaimDiffIsPartOfId: a diff claim's contribution/diff edge
// participates in its id — the same content diffed over a different
// predecessor is a different claim (content-addressing, §5.2).
func TestClaimDiffIsPartOfId(t *testing.T) {
	alice := contributor(t)
	pred1 := srcClaim(t, alice, "p1")
	pred2 := srcClaim(t, alice, "p2")
	at := pred1.Node().CreatedAt() // pin created_at so only the diff edge differs

	mk := func(pred Claim) Claim {
		c, err := NewClaim(TypeSource("note"), alice).
			WithInlineContent([]byte("same body")).
			WithEncoding(EncodingPlain).
			WithDiff(pred.ID()).
			WithCreatedAt(at).
			WithHeight(HeightOf(alice, pred)).
			Sign()
		require.NoError(t, err)
		return c
	}
	require.False(t, mk(pred1).ID().Equal(mk(pred2).ID()),
		"diffing a different predecessor yields a different claim id")
}

// TestClaimDiffIsSmallerThanFull: the happy-path payoff — a diff claim
// inherits its predecessor's content instead of re-stating it, so it
// serializes smaller. Same materialised content, fewer bytes on the wire —
// the storage optimisation §4.3 promises ("a storage optimisation carrying
// full provenance").
func TestClaimDiffIsSmallerThanFull(t *testing.T) {
	alice := contributor(t)
	big := bytes.Repeat([]byte("a repeated payload block — "), 200)

	// Predecessor holds the large content.
	pred, err := NewClaim(TypeSource("note"), alice).WithInlineContent(big).WithEncoding(EncodingPlain).WithHeight(HeightOf(alice)).Sign()
	require.NoError(t, err)

	// Full: re-states the whole content. Diff: inherits it, restating nothing.
	full, err := NewClaim(TypeSource("note"), alice).WithInlineContent(big).WithEncoding(EncodingPlain).WithHeight(HeightOf(alice)).Sign()
	require.NoError(t, err)
	diff, err := NewClaim(TypeSource("note"), alice).WithDiff(pred.ID()).WithHeight(HeightOf(alice, pred)).Sign()
	require.NoError(t, err)

	fullBytes, err := full.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	diffBytes, err := diff.EncodeCBOR(FormOriginal)
	require.NoError(t, err)

	require.Less(t, len(diffBytes), len(fullBytes),
		"a diff inheriting content serializes smaller than re-stating it")
}

// --- height (§4.1) ------------------------------------------------------

// TestBuilderHeightRequiredWhenReferencing: a claim with references (here the
// auto-added contributor edge) must declare a height — Sign fails otherwise.
func TestBuilderHeightRequiredWhenReferencing(t *testing.T) {
	alice := contributor(t)
	_, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("body")).
		WithEncoding(EncodingPlain).
		Sign()
	require.ErrorIs(t, err, errHeightRequired)
}

// TestBuilderHeightRejectedOnInitialClaim: an initial claim (no edges) may not
// carry a nonzero height.
func TestBuilderHeightRejectedOnInitialClaim(t *testing.T) {
	_, err := NewClaim(NodeContributor, nil).
		WithInlineContent([]byte("pubkey-ish")).
		WithEncoding(EncodingOctetStream).
		WithHeight(1).
		Sign()
	require.ErrorIs(t, err, errHeightOnInitial)
}

// TestBuilderHeightLastSetterWins: Height is one slot holding a resolver, so the
// last setter answers — WithHeight being FixedHeight under another name.
func TestBuilderHeightLastSetterWins(t *testing.T) {
	ctx := context.Background()
	alice := contributor(t)
	build := func(b ClaimBuilder) uint64 {
		c, err := b.WithInlineContent([]byte("body")).WithEncoding(EncodingPlain).Sign()
		require.NoError(t, err)
		return c.Node().Height()
	}
	require.Equal(t, uint64(3), build(NewClaim(TypeSource("note"), alice).
		WithHeight(7).WithHeightResolver(ctx, FixedHeight(3))), "the resolver set last")
	require.Equal(t, uint64(7), build(NewClaim(TypeSource("note"), alice).
		WithHeightResolver(ctx, FixedHeight(3)).WithHeight(7)), "the value set last")
}

// TestBuilderWithAutoHeight: WithAutoHeight reads each referenced claim's
// committed height from the Universe and sets 1 + max.
func TestBuilderWithAutoHeight(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	a := srcClaim(t, root, "a") // height 1
	require.NoError(t, PutClaim(ctx, u, root))
	require.NoError(t, PutClaim(ctx, u, a))

	b, err := NewClaim(TypeEntity("person"), root).
		WithInlineContent([]byte("b")).
		WithEncoding(EncodingPlain).
		WithEdges(mustDerivEdge(t, a)).
		WithAutoHeight(ctx, u).
		Sign()
	require.NoError(t, err)
	require.Equal(t, uint64(2), b.Node().Height(), "1 + max(contributor 0, source 1)")
}

// TestBuilderWithHeightResolver: the resolver is asked about every reference the
// closed claim carries — the contributor edge included — because the assembled edge
// set is its input, not anything the caller restates.
func TestBuilderWithHeightResolver(t *testing.T) {
	ctx := context.Background()
	root := contributor(t)      // height 0
	a := srcClaim(t, root, "a") // height 1

	var asked []string
	resolve := func(c context.Context, refs []Id) (uint64, error) {
		for _, r := range refs {
			asked = append(asked, r.String())
		}
		return HeightsFrom(root, a)(c, refs)
	}

	b, err := NewClaim(TypeEntity("person"), root).
		WithInlineContent([]byte("b")).
		WithEncoding(EncodingPlain).
		WithEdges(mustDerivEdge(t, a)).
		WithHeightResolver(ctx, resolve).
		Sign()
	require.NoError(t, err)
	require.Equal(t, uint64(2), b.Node().Height(), "1 + max(contributor 0, source 1)")
	require.ElementsMatch(t, []string{root.ID().String(), a.ID().String()}, asked,
		"the whole edge set reaches the resolver, contributor included")
}

// TestBuilderHeightsFromReportsAnAbsentReference: a claim citing what the resolver
// was not given fails to build, rather than taking a height over the rest.
func TestBuilderHeightsFromReportsAnAbsentReference(t *testing.T) {
	root := contributor(t)
	a := srcClaim(t, root, "a")

	_, err := NewClaim(TypeEntity("person"), root).
		WithInlineContent([]byte("b")).
		WithEncoding(EncodingPlain).
		WithEdges(mustDerivEdge(t, a)).
		WithHeightResolver(context.Background(), HeightsFrom(root)). // a withheld
		Sign()
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, err, errHeightResolve)
}

// TestBuilderHeightResolverFailureReaches: a resolver's own error surfaces as the
// build's, matchable through errHeightResolve.
func TestBuilderHeightResolverFailureReaches(t *testing.T) {
	_, err := NewClaim(TypeSource("note"), contributor(t)).
		WithInlineContent([]byte("body")).
		WithEncoding(EncodingPlain).
		WithHeightResolver(context.Background(),
			func(context.Context, []Id) (uint64, error) { return 0, ErrUnsupported }).
		Sign()
	require.ErrorIs(t, err, errHeightResolve)
	require.ErrorIs(t, err, ErrUnsupported)
}

// TestBuilderHeightResolverIsSugarForAutoHeight: WithAutoHeight is HeightsIn over the
// Universe, so the two setters agree on the same archive.
func TestBuilderHeightResolverIsSugarForAutoHeight(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	a := srcClaim(t, root, "a")
	require.NoError(t, PutClaim(ctx, u, root))
	require.NoError(t, PutClaim(ctx, u, a))

	build := func(b ClaimBuilder) uint64 {
		c, err := b.WithInlineContent([]byte("b")).WithEncoding(EncodingPlain).
			WithEdges(mustDerivEdge(t, a)).WithCreatedAt(time.Unix(1, 0).UTC()).Sign()
		require.NoError(t, err)
		return c.Node().Height()
	}
	viaUniverse := build(NewClaim(TypeEntity("person"), root).WithAutoHeight(ctx, u))
	viaResolver := build(NewClaim(TypeEntity("person"), root).
		WithHeightResolver(ctx, HeightsIn(u)))
	require.Equal(t, viaUniverse, viaResolver)
}

// TestHeightOf: the construction helper is 1 + max, or 0 for no references.
func TestHeightOf(t *testing.T) {
	root := contributor(t)                      // height 0
	a := srcClaim(t, root, "a")                 // height 1
	b := entityClaim(t, root, "person", "b", a) // height 2
	require.Equal(t, uint64(0), HeightOf(), "no references → 0")
	require.Equal(t, uint64(1), HeightOf(root), "over an initial claim → 1")
	require.Equal(t, uint64(3), HeightOf(a, b), "1 + max(1, 2)")
}

// TestClaimBuilderWithContributor covers the fluent contributor setter: a
// claim built through it is attributed to — and signed by — that contributor,
// equivalently to passing the contributor to NewClaim.
func TestClaimBuilderWithContributor(t *testing.T) {
	root := contributor(t)
	c, err := NewClaim(TypeSource("note"), nil).
		WithContributor(root).
		WithInlineContent([]byte("attributed via WithContributor")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(root)).
		Sign()
	require.NoError(t, err)
	require.NotNil(t, c.Contributor(), "claim carries a contributor")
	require.True(t, c.Contributor().ID().Equal(root.ID()), "attributed to the given contributor")
}
