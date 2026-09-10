package ranke

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the closure verifier (verify.go): Graph/Archive
// Verify and the VerifyOption knobs (WithMaxDepth, WithTrusted, WithStopAfter,
// WithOnError, WithExternalContent). Verify runs asynchronously and returns a
// VerificationRun handle — Wait for completion, then read Verified/Failures/Err.

// corruptNode overwrites a claim's STORED bytes in the graph's Universe with a
// re-encoded, mutated node (created_at bumped) filed under its ORIGINAL id.
// The bytes still decode, but their node preimage no longer hashes to id(v),
// so verification fails. Verification now hashes the stored bytes (not a
// re-encode of the live claim), so the corruption must live in the store —
// white-box (package ranke); the public API can't build such a claim, which is
// the whole point of §5.10.
func corruptNode(t *testing.T, g Graph, c Claim) {
	t.Helper()
	mu, ok := g.(*graph).u.(*memoryUniverse)
	require.True(t, ok, "the test graph is backed by the in-memory Universe")
	cc, ok := c.(*claim)
	require.True(t, ok, "claim is the package's concrete type")
	cc.node.createdAt = cc.node.createdAt.Add(time.Hour) // id stays as-signed
	bad, err := cc.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	mu.mu.Lock()
	mu.claims[c.ID().String()] = bad
	mu.mu.Unlock()
}

// windowedContributor builds a signed contributor dated at, whose key is valid over the
// closed window the fields describe — an empty bound being left unset. It is dated with
// the claims it signs rather than now, since `V-MONO` dates a claim no earlier than the
// contributor it references, and a window is declared rather than sat in.
func windowedContributor(t *testing.T, from, until string, at time.Time) (Contributor, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err := EncodePublicKey(priv.Public())
	require.NoError(t, err)

	b := NewClaim(NodeContributor, nil).WithInlineContent(pubkey).WithEncoding(EncodingOctetStream).WithCreatedAt(at)
	if from != "" {
		b = b.WithField(FieldPubkeyValidFrom, from)
	}
	if until != "" {
		b = b.WithField(FieldPubkeyExpiresAfter, until)
	}
	c, err := b.Sign(priv)
	require.NoError(t, err)
	view, err := c.AsContributor(context.Background(), nil, priv)
	require.NoError(t, err)
	return view, priv
}

// signedAt builds a claim attributed to who and dated at.
func signedAt(t *testing.T, who Contributor, at time.Time) Claim {
	t.Helper()
	c, err := NewClaim(TypeSource("note"), who).
		WithInlineContent([]byte("a note")).
		WithEncoding(EncodingPlain).
		WithHeight(1).
		WithCreatedAt(at).
		Sign()
	require.NoError(t, err)
	return c
}

// TestVerifyKeyWindow: a signature proves who signed, and the window proves they still
// could — so a claim dated outside its contributor key's validity fails verification
// however sound its signature is.
func TestVerifyKeyWindow(t *testing.T) {
	const from, until = "2026-01-01T00:00:00.000000000Z", "2026-06-30T00:00:00.000000000Z"
	stamp := func(s string) time.Time {
		at, err := parseRFC3339Nano(s)
		require.NoError(t, err)
		return at
	}

	for name, tc := range map[string]struct {
		from, until string
		at          time.Time
		wantErr     error
	}{
		"inside the window":    {from, until, stamp("2026-03-01T00:00:00.000000000Z"), nil},
		"on the lower bound":   {from, until, stamp(from), nil},
		"on the upper bound":   {from, until, stamp(until), nil},
		"before it opens":      {from, until, stamp("2025-12-31T23:59:59.000000000Z"), ErrKeyNotYetValid},
		"after it closes":      {from, until, stamp("2026-07-01T00:00:00.000000000Z"), ErrKeyExpired},
		"open-ended upward":    {from, "", stamp("2030-01-01T00:00:00.000000000Z"), nil},
		"open-ended downward":  {"", until, stamp("2000-01-01T00:00:00.000000000Z"), nil},
		"expired, no lower":    {"", until, stamp("2026-07-01T00:00:00.000000000Z"), ErrKeyExpired},
		"no window ever fails": {"", "", stamp("2099-01-01T00:00:00.000000000Z"), nil},
	} {
		t.Run(name, func(t *testing.T) {
			who, _ := windowedContributor(t, tc.from, tc.until, tc.at)
			g := newGraph(t, who)
			require.NoError(t, g.AddClaims(context.Background(), signedAt(t, who, tc.at)))

			run := g.Verify()
			run.Wait()
			require.NoError(t, run.Err())
			if tc.wantErr == nil {
				require.Empty(t, run.Failures(), "a claim inside the window verifies")
				return
			}
			require.Len(t, run.Failures(), 1)
			require.ErrorIs(t, run.Failures()[0].Err, tc.wantErr)
		})
	}
}

// An unparsable bound is `V-TIME`'s subject rather than the window's, so
// TestVerifyKeyWindowRejectsUnparsableBound lives in verify_time_test.go.

// deletableSource stages a source claim scheduled for deletion, so a later claim has
// something whose schedule it must carry.
func deletableSource(t *testing.T, g Graph, who Contributor, due string) Claim {
	t.Helper()
	c, err := NewClaim(TypeSource("note"), who).
		WithInlineContent([]byte("bytes with a shelf life")).
		WithEncoding(EncodingPlain).
		WithField(FieldDeleteBy, due).
		WithHeight(1).
		Sign()
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), c))
	return c
}

// TestVerifyDeleteByMustBeCarried: deletion removes a claim's bytes, so the schedule has
// to live on the edges referencing it — a citing claim that omits it would leave an
// unexplained gap, and verification refuses it.
func TestVerifyDeleteByMustBeCarried(t *testing.T) {
	const due = "2030-01-01T00:00:00.000000000Z"
	ctx := context.Background()
	root := contributor(t)
	g := newGraph(t, root)
	src := deletableSource(t, g, root, due)

	// Cited without the copy: refused.
	bare, err := NewEdge(EdgeConfig{Reference: src.ID(), Type: TypeDerivation("note")})
	require.NoError(t, err)
	silent, err := NewClaim(TypeDerivation("note"), root).
		WithInlineContent([]byte("cites it, says nothing")).
		WithEncoding(EncodingPlain).
		WithEdges(bare).
		WithHeight(2).
		Sign()
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(ctx, silent))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Len(t, run.Failures(), 1)
	require.ErrorIs(t, run.Failures()[0].Err, ErrDeleteByNotCopied)
}

// TestVerifyDeleteByCarriedPasses: the same citation carrying the date verifies, so the
// rule refuses omission rather than citation.
func TestVerifyDeleteByCarriedPasses(t *testing.T) {
	const due = "2030-01-01T00:00:00.000000000Z"
	ctx := context.Background()
	root := contributor(t)
	g := newGraph(t, root)
	src := deletableSource(t, g, root, due)

	carried, err := NewEdge(EdgeConfig{
		Reference: src.ID(),
		Type:      TypeDerivation("note"),
		Fields:    map[string]string{FieldDeleteBy: due},
	})
	require.NoError(t, err)
	honest, err := NewClaim(TypeDerivation("note"), root).
		WithInlineContent([]byte("cites it and says so")).
		WithEncoding(EncodingPlain).
		WithEdges(carried).
		WithHeight(2).
		Sign()
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(ctx, honest))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "the schedule travels with the reference")
}

// TestEdgeCarriesTargetDeleteBy: naming the target claim in the config is enough — the
// edge is built carrying its schedule, so a contributor need not track it by hand.
func TestEdgeCarriesTargetDeleteBy(t *testing.T) {
	const due = "2030-01-01T00:00:00.000000000Z"
	ctx := context.Background()
	root := contributor(t)
	g := newGraph(t, root)
	src := deletableSource(t, g, root, due)

	e, err := NewEdge(EdgeConfig{
		Reference:  src.ID(),
		Referenced: src,
		Type:       TypeDerivation("note"),
	})
	require.NoError(t, err)
	got, err := e.GetField(FieldDeleteBy)
	require.NoError(t, err, "the edge carries its target's schedule")
	require.Equal(t, due, got)

	built, err := NewClaim(TypeDerivation("note"), root).
		WithInlineContent([]byte("cites a claim with a shelf life")).
		WithEncoding(EncodingPlain).
		WithEdges(e).
		WithHeight(2).
		Sign()
	require.NoError(t, err)

	require.NoError(t, g.AddClaims(ctx, built))
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "what the builder produces is what the rule accepts")
}

// --- honest graphs verify ----------------------------------------------

// TestVerifyGraphSignedByItsContributor: a claim signs under the key its contributor
// carries, with no key passed at the call — the other half of TestVerifySignedGraph,
// which hands one over explicitly.
func TestVerifyGraphSignedByItsContributor(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	require.NoError(t, g.AddClaims(context.Background(), srcClaim(t, root, "hello")))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err(), "no terminal error")
	require.Empty(t, run.Failures(), "the closure verifies")
}

// TestVerifySignedGraph: an honestly-built signed graph verifies — the
// per-claim signature check passes across the closure.
func TestVerifySignedGraph(t *testing.T) {
	alice, alicePriv := newSignedContributor(t)
	g := newGraph(t, alice)
	src, err := NewClaim(TypeSource("email"), alice).
		WithInlineContent([]byte("From: alice\r\n\r\nhi")).
		WithEncoding(EncodingMessage("rfc822")).
		WithHeight(HeightOf(alice)).
		Sign(alicePriv)
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), src))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "signed closure verifies")
}

// TestVerifyCountsEveryClaim: the run reports every claim in the closure as
// verified — here the root contributor plus the source.
func TestVerifyCountsEveryClaim(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	require.NoError(t, g.AddClaims(context.Background(), srcClaim(t, root, "hello")))

	run := g.Verify()
	run.Wait()
	require.Empty(t, run.Failures())
	require.Equal(t, 2, run.Verified(), "root + source both verified")
}

// chainGraph builds root ← a(source) ← b(entity) ← c(entity), a 4-claim
// closure two levels deep, for the depth/trusted options.
func chainGraph(t *testing.T) (Graph, Contributor, Claim, Claim, Claim) {
	t.Helper()
	root := contributor(t)
	g := newGraph(t, root)
	a := srcClaim(t, root, "a")
	require.NoError(t, g.AddClaims(context.Background(), a))
	b := entityClaim(t, root, "person", "b", a)
	require.NoError(t, g.AddClaims(context.Background(), b))
	c := entityClaim(t, root, "object", "c", b)
	require.NoError(t, g.AddClaims(context.Background(), c))
	return g, root, a, b, c
}

// --- WithMaxDepth -------------------------------------------------------

// TestVerifyWithMaxDepth: bounding the depth prunes the deepest claims, so
// fewer are verified than an unbounded walk.
func TestVerifyWithMaxDepth(t *testing.T) {
	g, _, _, _, _ := chainGraph(t)

	full := g.Verify()
	full.Wait()
	require.Empty(t, full.Failures())
	require.Equal(t, 4, full.Verified(), "unbounded walk reaches root, a, b, c")

	shallow := g.Verify(WithMaxDepth(1))
	shallow.Wait()
	require.Empty(t, shallow.Failures())
	require.Less(t, shallow.Verified(), full.Verified(),
		"maxDepth stops the walk short of the deepest claim")
}

// --- WithTrusted --------------------------------------------------------

// TestVerifyWithTrusted: a claim reported trusted is pruned — skipped, not
// re-verified — so the verified count drops.
func TestVerifyWithTrusted(t *testing.T) {
	g, _, a, _, _ := chainGraph(t)

	full := g.Verify()
	full.Wait()

	trusted := g.Verify(WithTrusted(func(id Id) bool { return id.Equal(a.ID()) }))
	trusted.Wait()
	require.Empty(t, trusted.Failures())
	require.Less(t, trusted.Verified(), full.Verified(),
		"a trusted claim (and its pruned subtree) is not re-verified")
}

// --- WithMaxClaims ------------------------------------------------------

// TestVerifyWithMaxClaims: a hard work cap stops the walk after n claims
// have been processed, independent of depth.
func TestVerifyWithMaxClaims(t *testing.T) {
	g, _, _, _, _ := chainGraph(t) // 4 claims: root, a, b, c

	full := g.Verify()
	full.Wait()
	require.Equal(t, 4, full.Verified())

	capped := g.Verify(WithMaxClaims(2))
	capped.Wait()
	require.Equal(t, 2, capped.Verified(), "the walk stops after 2 claims are processed")
}

// --- WithCreatedAfter ---------------------------------------------------

// TestVerifyWithCreatedAfter: claims older than the bound are pruned. The
// walk descends toward older references, so this bounds verification to a
// recent window — here the older root contributor is skipped.
func TestVerifyWithCreatedAfter(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	root, _ := windowedContributor(t, "", "", old)

	g := newGraph(t, root)
	em, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("recent")).
		WithEncoding(EncodingPlain).
		WithCreatedAt(recent).
		WithHeight(HeightOf(root)).
		Sign()
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), em))

	full := g.Verify()
	full.Wait()
	require.Equal(t, 2, full.Verified())

	bounded := g.Verify(WithCreatedAfter(recent))
	bounded.Wait()
	require.Empty(t, bounded.Failures())
	require.Less(t, bounded.Verified(), full.Verified(),
		"the older root is pruned by the created_at bound")
}

// --- WithOnError --------------------------------------------------------

// TestVerifyWithOnError: the callback fires for each failing claim, carrying
// the offending id.
func TestVerifyWithOnError(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	bad := srcClaim(t, root, "will be corrupted")
	require.NoError(t, g.AddClaims(context.Background(), bad))
	corruptNode(t, g, bad)

	var seen []Failure
	run := g.Verify(WithOnError(func(f Failure) { seen = append(seen, f) }))
	run.Wait()

	require.NotEmpty(t, seen, "WithOnError fires on the corrupted claim")
	require.NotEmpty(t, run.Failures(), "the failure is also recorded on the run")
	var found bool
	for _, f := range seen {
		if f.ID.Equal(bad.ID()) {
			found = true
		}
	}
	require.True(t, found, "the reported failure names the corrupted claim")
}

// --- WithStopAfter ------------------------------------------------------

// TestVerifyWithStopAfter: the walk halts once the failure budget is hit,
// so no more than that many failures are recorded.
func TestVerifyWithStopAfter(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	em := srcClaim(t, root, "seed")
	require.NoError(t, g.AddClaims(context.Background(), em))
	e1 := entityClaim(t, root, "person", "a", em)
	e2 := entityClaim(t, root, "object", "b", em)
	require.NoError(t, g.AddClaims(context.Background(), e1, e2))
	corruptNode(t, g, e1) // both open heads fail
	corruptNode(t, g, e2)

	run := g.Verify(WithStopAfter(1))
	run.Wait()
	require.Len(t, run.Failures(), 1, "the walk stops after the first failure")
	require.True(t, run.Done())
}

// --- WithExternalContent (Archive) -------------------------------------

// TestVerifyExternalContentToggle: external content is skipped by default
// (may be huge) and verified only under WithExternalContent — so a corrupt
// external blob passes the default walk but fails when external content is on.
func TestVerifyExternalContentToggle(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)

	realBlob := []byte("the real external payload bytes")
	hash, err := HashContent(realBlob)
	require.NoError(t, err)
	ext, err := NewClaim(TypeSource("blob"), root).
		WithExternalContent(hash, uint64(len(realBlob))).
		WithEncoding(EncodingOctetStream).
		WithHeight(HeightOf(root)).
		Sign()
	require.NoError(t, err)
	bth := branchTable(t, root, []Claim{ext}, branchEdge(t, "main", ext.ID()))
	putClaims(t, u, root, ext, bth)

	// Store a CORRUPT blob under the hash — same length, one byte flipped.
	corruptBlob := make([]byte, len(realBlob))
	copy(corruptBlob, realBlob)
	corruptBlob[0] ^= 0xFF
	require.NoError(t, u.PutContents(ctx, []ContentBlob{{Hash: hash, Content: corruptBlob}}))

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	// Default: external content not fetched → the corruption is not seen.
	def, err := arc.Verify(ctx)
	require.NoError(t, err)
	def.Wait()
	require.Empty(t, def.Failures(), "external content skipped by default")

	// With external content: the verifying reader catches the mismatch.
	ext2, err := arc.Verify(ctx, WithExternalContent())
	require.NoError(t, err)
	ext2.Wait()
	require.NotEmpty(t, ext2.Failures(), "corrupt external content is detected when verified")
}

// --- Branch.Verify ------------------------------------------------------

// TestVerifyBranch: a branch verifies its subgraph from its head claim.
func TestVerifyBranch(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	bth := branchTable(t, root, []Claim{em}, branchEdge(t, "main", em.ID()))
	putClaims(t, u, root, em, bth)

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)
	b, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)

	run, err := b.Verify(ctx)
	require.NoError(t, err)
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "the branch subgraph verifies from its head")
}

// --- height invariant (§4.1) -------------------------------------------

// TestVerifyRejectsWrongHeight: the verifier re-derives height == 1 + max(refs)
// and rejects a claim whose stored height disagrees — even though its id is
// internally consistent (Sign accepts any nonzero height on a referencing
// claim). The gate lives in the verifier, not the builder.
func TestVerifyRejectsWrongHeight(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	bad, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("body")).
		WithEncoding(EncodingPlain).
		WithHeight(99). // correct is 1 — the only reference is the height-0 contributor
		Sign()
	require.NoError(t, err, "Sign does not itself check height against refs")
	require.NoError(t, g.AddClaims(context.Background(), bad))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	var found bool
	for _, f := range run.Failures() {
		if f.ID.Equal(bad.ID()) {
			require.ErrorIs(t, f.Err, ErrHeightMismatch)
			found = true
		}
	}
	require.True(t, found, "the wrong-height claim is reported")
}

// --- created_at monotonicity (`V-MONO`) --------------------------------

// TestVerifyCreatedAtMonotone: a claim dated before the contributor it references
// fails, one dated after it passes, and one dated at the same instant passes too —
// the rule is ≥, and a contribution commits its claims at a single instant.
func TestVerifyCreatedAtMonotone(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		at      time.Time
		wantErr error
	}{
		"after its reference":  {base.Add(24 * time.Hour), nil},
		"at the same instant":  {base, nil},
		"before its reference": {base.Add(-24 * time.Hour), ErrCreatedAtNotMonotone},
	} {
		t.Run(name, func(t *testing.T) {
			who, _ := windowedContributor(t, "", "", base)
			g := newGraph(t, who)
			c := signedAt(t, who, tc.at)
			require.NoError(t, g.AddClaims(context.Background(), c), "the builder dates a claim as told")

			run := g.Verify()
			run.Wait()
			require.NoError(t, run.Err())
			if tc.wantErr == nil {
				require.Empty(t, run.Failures())
				return
			}
			require.Len(t, run.Failures(), 1)
			require.True(t, run.Failures()[0].ID.Equal(c.ID()), "the failure names the back-dated claim")
			require.ErrorIs(t, run.Failures()[0].Err, tc.wantErr)
		})
	}
}

// TestVerifyCreatedAtMonotoneAcrossDerivation: every reference is compared, so a
// derivation dated after its contributor and before the source it cites still fails.
func TestVerifyCreatedAtMonotoneAcrossDerivation(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	who, _ := windowedContributor(t, "", "", base)
	g := newGraph(t, who)

	src := signedAt(t, who, base.Add(2*time.Hour))
	require.NoError(t, g.AddClaims(ctx, src))

	bad, err := NewClaim(TypeEntity("person"), who).
		WithInlineContent([]byte("dated between its two references")).
		WithEncoding(EncodingPlain).
		WithEdges(mustDerivEdge(t, src)).
		WithHeight(HeightOf(who, src)).
		WithCreatedAt(base.Add(time.Hour)).
		Sign()
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(ctx, bad))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Len(t, run.Failures(), 1)
	require.True(t, run.Failures()[0].ID.Equal(bad.ID()))
	require.ErrorIs(t, run.Failures()[0].Err, ErrCreatedAtNotMonotone)
}

// TestVerifyCreatedAtMonotoneInitialClaim: an initial claim references nothing, so the
// rule has nothing to compare it against and it verifies alone.
func TestVerifyCreatedAtMonotoneInitialClaim(t *testing.T) {
	who, _ := windowedContributor(t, "", "", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	g := newGraph(t, who)

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures())
	require.Equal(t, 1, run.Verified(), "the initial claim is the whole closure")
}

// TestVerifyRejectsBranchTableReference: a contribution/branches (branch-table)
// claim may be referenced only by another branch table. An ordinary claim that
// references one fails verification — the branch-table-reference rule keeps the
// archive-management lineage out of the content graph.
func TestVerifyRejectsBranchTableReference(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)

	// A branch table (the archive-management lineage layer).
	bt := branchTable(t, root, []Claim{root}, branchEdge(t, "main", root.ID()))
	require.NoError(t, g.AddClaims(context.Background(), bt))

	// An ordinary derivation claim that illegally references the branch table
	// via its (otherwise valid) derivation edge.
	de, err := NewEdge(EdgeConfig{Reference: bt.ID(), Type: TypeDerivation("source")})
	require.NoError(t, err)
	bad, err := NewClaim(TypeDerivation("summary"), root).
		WithEdges(de).
		WithInlineContent([]byte("illegal branch-table reference")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(root, bt)).
		Sign()
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), bad))

	run := g.Verify()
	run.Wait()
	require.NotEmpty(t, run.Failures(), "a non-branch-table referencing a branch table must fail verification")
	var found bool
	for _, f := range run.Failures() {
		if f.ID.Equal(bad.ID()) {
			found = true
		}
	}
	require.True(t, found, "the failure names the offending claim")
}

// TestVerifyWithSkipRules confirms a named rule can be dropped from a run: the
// branch-table-reference violation TestVerifyRejectsBranchTableReference
// catches passes cleanly once that rule is skipped by name.
func TestVerifyWithSkipRules(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)

	bt := branchTable(t, root, []Claim{root}, branchEdge(t, "main", root.ID()))
	require.NoError(t, g.AddClaims(context.Background(), bt))

	de, err := NewEdge(EdgeConfig{Reference: bt.ID(), Type: TypeDerivation("source")})
	require.NoError(t, err)
	bad, err := NewClaim(TypeDerivation("summary"), root).
		WithEdges(de).
		WithInlineContent([]byte("illegal branch-table reference")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(root, bt)).
		Sign()
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), bad))

	// With the rule skipped, the otherwise-valid claim no longer fails.
	run := g.Verify(WithSkipRules("branch-table reference"))
	run.Wait()
	require.NoError(t, run.Err())
	for _, f := range run.Failures() {
		require.False(t, f.ID.Equal(bad.ID()), "a skipped rule must not fire")
	}
}

// TestVerifyRuleSet confirms the rule registry is introspectable — the names
// and statements a caller enumerates to choose what to skip (WithSkipRules).
func TestVerifyRuleSet(t *testing.T) {
	rules := VerifyRuleSet()
	require.NotEmpty(t, rules)
	names := make(map[string]string, len(rules))
	for _, r := range rules {
		require.NotEmpty(t, r.Name, "rule carries a name")
		require.NotEmpty(t, r.Rule, "rule carries a statement")
		names[r.Name] = r.Rule
	}
	require.Contains(t, names, "branch-table reference")
	require.Contains(t, names, "id")
	require.Contains(t, names, "signature")
	require.Contains(t, names["id"], "`V-ID`", "the id check stands on its own, needing no key")
	require.Contains(t, names, "created_at monotonicity", "a FORCED rule is skippable like the rest")
	require.Contains(t, names["created_at monotonicity"], "`V-MONO`", "and states the rule it enforces")
}
