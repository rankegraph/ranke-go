package dev_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// clock is a deterministic monotonic time source satisfying devseq.Clock.
type clock struct{ t time.Time }

func (c *clock) Tick() time.Time {
	out := c.t
	c.t = c.t.Add(time.Second)
	return out
}

// operator builds a signed root contributor carrying its own signing key, so
// the Sequencer can attribute and sign with it. It is an initial claim (height 0).
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

func newSequencer(t *testing.T, ctx context.Context) (*devseq.Sequencer, ranke.Contributor, *clock) {
	t.Helper()
	u := ranke.NewMemoryUniverse()
	clk := &clock{t: time.Unix(1000, 0).UTC()}
	op := operator(t, ctx, clk.Tick())
	seq, err := devseq.NewSequencer(ctx, u, ranke.Seed([]byte(op.ID().String())), op, clk)
	require.NoError(t, err)
	require.True(t, seq.InGenesis(), "an empty list is an archive not yet founded")
	_, err = helpers.Found(ctx, seq, "dev")
	require.NoError(t, err)
	return seq, op, clk
}

// head resolves the archive head k through a fresh snapshot.
func head(t *testing.T, ctx context.Context, seq ranke.Sequencer) ranke.Id {
	t.Helper()
	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err)
	return arc.Head()
}

// TestInvalidContributionDenied is the guarantee the Sequencer must uphold:
// valid(A) → valid(A'). A contribution referencing a claim that does not exist
// is structurally invalid; the Sequencer must verify (paper 2 §Sequencer step 4)
// and reject it, leaving the archive head unchanged so no invalid state is ever
// published.
func TestInvalidContributionDenied(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)
	headBefore := head(t, ctx, seq)

	// A well-formed, signed claim whose one derivation edge references an id
	// that was never contributed — so its closure cannot be verified.
	bogus, err := ranke.HashContent([]byte("no-such-claim"))
	require.NoError(t, err)
	de, err := ranke.NewEdge(ranke.EdgeConfig{Reference: bogus, Type: ranke.TypeDerivation("note")})
	require.NoError(t, err)
	bad, err := ranke.NewClaim(ranke.TypeDerivation("note"), op).
		WithInlineContent([]byte("derived from nothing")).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(de).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err, "the claim itself is well-formed; only its reference dangles")

	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{bad})
	require.Error(t, err, "the Sequencer must deny a contribution that references a non-existent claim")

	require.True(t, headBefore.Equal(head(t, ctx, seq)),
		"a denied contribution must not advance the archive head")
}

// TestValidContributionAccepted is the positive control: a well-formed
// contribution whose closure resolves is verified and merged, and the head
// advances. Without this, TestInvalidContributionDenied could pass by denying
// everything.
func TestValidContributionAccepted(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)
	headBefore := head(t, ctx, seq)

	// A source note attributed to the operator: its only reference is the
	// auto-added contribution/contributor edge to the operator (height 0, already
	// in the Universe), so the claim is height 1 and its closure resolves.
	good, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("a real source note")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)

	merged, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{good})
	require.NoError(t, err, "a valid contribution must be accepted")
	require.False(t, headBefore.Equal(merged), "a merged contribution must advance the archive head")
	require.True(t, merged.Equal(head(t, ctx, seq)), "the receipt names the snapshot's k")
}

// TestDeletionCannotStrandAClaim: deleting a claim removes the record its edges live in,
// so a claim reachable only through one scheduled for deletion would stop being
// reachable while remaining present. The head cites it directly, so the branch still
// reaches it once the scheduled claim is gone.
func TestDeletionCannotStrandAClaim(t *testing.T) {
	const due = "2030-01-01T00:00:00.000000000Z"
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	// keeper is cited only by doomed, which is due for deletion.
	keeper, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("outlives the claim that cites it")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)

	// The edge carries nothing: keeper is not scheduled, so claiming a date for it
	// would announce a gap that never comes.
	e, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: keeper.ID(),
		Type:      ranke.TypeDerivation("note"),
	})
	require.NoError(t, err)
	doomed, err := ranke.NewClaim(ranke.TypeDerivation("note"), op).
		WithInlineContent([]byte("bytes with a shelf life")).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(e).
		WithField(ranke.FieldDeleteBy, due).
		WithHeight(2).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)

	head, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{keeper, doomed})
	require.NoError(t, err)

	// Walking the branch while everything is present reaches both.
	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err)
	br, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)
	for _, c := range []ranke.Claim{keeper, doomed} {
		ok, err := br.HasClaim(ctx, c.ID())
		require.NoError(t, err)
		require.True(t, ok)
	}

	// The property that matters: a walk from the branch head reaches keeper without
	// passing through anything scheduled for deletion. Asserting only that the head
	// cites keeper would hold even unprotected, since the head would then be doomed
	// itself, whose own edge cites keeper.
	require.True(t, reachableAfterDeletion(t, ctx, arc, br.Head(), keeper.ID()),
		"keeper survives the deletion of the only claim that cited it")
	require.False(t, reachableAfterDeletion(t, ctx, arc, br.Head(), doomed.ID()),
		"the deleted claim itself goes, which is what leaves the gap to explain")
	require.NotNil(t, head)
}

// reachableAfterDeletion walks from head as a reader would once every claim carrying
// delete_by has had its bytes removed: such a claim is a gap, and the record its edges
// lived in is gone, so the walk stops there.
func reachableAfterDeletion(t *testing.T, ctx context.Context, arc ranke.Archive, head, want ranke.Id) bool {
	t.Helper()
	seen := map[string]bool{}
	queue := []ranke.Id{head}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == nil || seen[id.String()] {
			continue
		}
		seen[id.String()] = true

		c, err := arc.GetClaim(ctx, id)
		require.NoError(t, err)
		if _, err := c.Node().GetField(ranke.FieldDeleteBy); err == nil {
			continue // deleted: a gap, its edges gone with its bytes
		}
		if id.Equal(want) {
			return true
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}
	return false
}

// TestNewBranchNeedsTheRight: a branch the base does not carry is brought into being by
// the merge naming it, so creating one is a right of its own (§Access, C over
// $branches) rather than a side effect of writing.
func TestNewBranchNeedsTheRight(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	note := func() ranke.Claim {
		c, err := ranke.NewClaim(ranke.TypeSource("note"), op).
			WithInlineContent([]byte("on a branch that does not exist yet")).
			WithEncoding(ranke.EncodingPlain).
			WithHeight(1).
			WithCreatedAt(clk.Tick()).
			Sign()
		require.NoError(t, err)
		return c
	}

	write := func(t *testing.T, branch string, opts ...ranke.ContributionOption) error {
		t.Helper()
		// The clock ticks for the claim before the contribution takes its base, so the
		// claim is dated at or before it.
		n := note()
		c, err := seq.NewContribution(ctx, append(opts,
			ranke.WithReferencableBranches(ranke.BranchArchive))...)
		require.NoError(t, err)
		require.NoError(t, c.AddClaims(branch, []ranke.Claim{n}))
		_, err = c.CompleteAndVerify(ctx)
		return err
	}

	require.ErrorIs(t, write(t, "fresh"), ranke.ErrBranchNotCreatable,
		"writing to an absent branch would create it")
	require.NoError(t, write(t, "fresh", ranke.WithCreatableBranches("fresh")),
		"naming it creatable admits exactly that branch")
	require.ErrorIs(t, write(t, "other", ranke.WithCreatableBranches("fresh")),
		ranke.ErrBranchNotCreatable, "and only that branch")
	require.NoError(t, write(t, "any", ranke.WithCreatableBranches(ranke.TargetBranches)),
		"$branches admits any, the surface a creation grant is held against")
}

// TestMissingBranchesTellsWhatWouldBeCreated: a caller weighs whether a contribution
// needs creation rights before opening it, so the archive answers which are absent.
func TestMissingBranchesTellsWhatWouldBeCreated(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	good, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("on main")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)
	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{good})
	require.NoError(t, err)

	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err)

	missing, err := arc.MissingBranches(ctx, []string{"main", "fresh", "other", "fresh"})
	require.NoError(t, err)
	require.Equal(t, []string{"fresh", "other"}, missing, "absent once each, in the order asked")

	missing, err = arc.MissingBranches(ctx, []string{"main"})
	require.NoError(t, err)
	require.Empty(t, missing, "nothing to create, so no right is needed")
}

// TestFutureDatedClaimRefused: a contribution is stamped with the Sequencer's time t at
// step 1, and step 2 admits a claim only if it is dated at or before that.
func TestFutureDatedClaimRefused(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)
	headBefore := head(t, ctx, seq)

	// The clock is monotone, so a later tick is ahead of any base a contribution
	// opened against.
	ahead := clk.Tick().Add(time.Hour)
	future, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("dated after the base")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(ahead).
		Sign()
	require.NoError(t, err)

	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{future})
	require.ErrorIs(t, err, ranke.ErrFutureDated, "a claim may not be dated after its base")
	require.True(t, headBefore.Equal(head(t, ctx, seq)), "a denied contribution must not advance the head")
}

// limiting builds a claim of a type reserved to the Sequencer: it names the target it
// restricts, which is what makes it limiting (paper 2 §Deletion).
func limiting(t *testing.T, op ranke.Contributor, target ranke.Claim, at time.Time) ranke.Claim {
	t.Helper()
	e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: target.ID(), Type: ranke.EdgeTypeDelete})
	require.NoError(t, err)
	c, err := ranke.NewClaim(ranke.NodeDelete, op).
		WithEdges(e).
		WithHeight(2).
		WithCreatedAt(at).
		Sign()
	require.NoError(t, err)
	return c
}

// TestReservedTypeDenied: limiting and branch-table claims are the Sequencer's alone
// (step 2), so a contribution carrying one is refused however well-formed it is.
func TestReservedTypeDenied(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	note, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("the claim a delete would name")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)
	headBefore := head(t, ctx, seq)

	_, err = helpers.Contribute(ctx, seq, "main",
		[]ranke.Claim{note, limiting(t, op, note, clk.Tick())})
	require.ErrorIs(t, err, ranke.ErrReservedType, "a client may not create a limiting claim")
	require.True(t, headBefore.Equal(head(t, ctx, seq)), "a denied contribution must not advance the head")
}

// TestReservedTypeLifted is the control: the rule is a constraint, not a prohibition,
// so a caller holding the Sequencer can admit the type — which is how a fixture
// builder stands in for the Sequencer that would mint it.
func TestReservedTypeLifted(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	note, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("the claim the delete names")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)

	merged, err := helpers.Contribute(ctx, seq, "main",
		[]ranke.Claim{note, limiting(t, op, note, clk.Tick())},
		ranke.WithLiftedTypes(ranke.NodeDelete))
	require.NoError(t, err, "the lifted type is admitted")
	require.True(t, merged.Equal(head(t, ctx, seq)), "the contribution merged")
}

// TestLiftIsPerType: lifting one reserved type says nothing about the others, so a
// contribution cannot smuggle a branch table in behind a delete.
func TestLiftIsPerType(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	table, err := ranke.NewClaim(ranke.NodeBranches, op).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)

	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{table},
		ranke.WithLiftedTypes(ranke.NodeDelete))
	require.ErrorIs(t, err, ranke.ErrReservedType, "the branch table is still the Sequencer's")
}

// TestReferenceOutsideDeclaredBranchesDenied: a claim on another branch may be cited
// only where the contribution declared that branch readable (step 3).
func TestReferenceOutsideDeclaredBranchesDenied(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	note := func(body string) ranke.Claim {
		c, err := ranke.NewClaim(ranke.TypeSource("note"), op).
			WithInlineContent([]byte(body)).
			WithEncoding(ranke.EncodingPlain).
			WithHeight(1).
			WithCreatedAt(clk.Tick()).
			Sign()
		require.NoError(t, err)
		return c
	}

	// A claim lands on "private", and "public" gets one of its own — so it exists at
	// the base and its closure reaches the operator, leaving the cited claim as the
	// only thing a refusal can be about.
	secret := note("on the private branch")
	_, err := helpers.Contribute(ctx, seq, "private", []ranke.Claim{secret})
	require.NoError(t, err)
	_, err = helpers.Contribute(ctx, seq, "public", []ranke.Claim{note("on the public branch")})
	require.NoError(t, err)

	cite := func(t *testing.T, readable ...string) error {
		t.Helper()
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: secret.ID(),
			Type:      ranke.TypeDerivation("note"),
		})
		require.NoError(t, err)
		derived, err := ranke.NewClaim(ranke.TypeDerivation("note"), op).
			WithInlineContent([]byte("cites the private note")).
			WithEncoding(ranke.EncodingPlain).
			WithEdges(e).
			WithHeight(2).
			WithCreatedAt(clk.Tick()).
			Sign()
		require.NoError(t, err)

		c, err := seq.NewContribution(ctx, ranke.WithReferencableBranches(readable...))
		require.NoError(t, err)
		if err := c.AddClaims("public", []ranke.Claim{derived}); err != nil {
			return err
		}
		_, err = c.CompleteAndVerify(ctx)
		return err
	}

	// "private" undeclared: the reference is refused.
	require.ErrorIs(t, cite(t), ranke.ErrUnreadableReference,
		"an undeclared branch's claim may not be cited")

	// Declared: the same reference is admitted.
	require.NoError(t, cite(t, "private"), "the declared branch's claim may be cited")
}

// TestAddWireAdvancesDeclaredBranches drives a contribution the way a server does:
// read the declaration, check it, then hand the same reader on for one advance.
func TestAddWireAdvancesDeclaredBranches(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	note := func(body string) ranke.Claim {
		c, err := ranke.NewClaim(ranke.TypeSource("note"), op).
			WithInlineContent([]byte(body)).
			WithEncoding(ranke.EncodingPlain).
			WithHeight(1).
			WithCreatedAt(clk.Tick()).
			Sign()
		require.NoError(t, err)
		return c
	}
	onAlpha, onBeta := note("for alpha"), note("for beta")

	var buf bytes.Buffer
	w := ranke.NewWireWriter(&buf, ranke.WireConstraints{
		Branches:     []string{"alpha", "beta"},
		Referencable: []string{ranke.BranchArchive},
		Creatable:    []string{"alpha", "beta"},
	})
	require.NoError(t, w.WriteClaim("alpha", onAlpha))
	require.NoError(t, w.WriteClaim("beta", onBeta))

	// What a server does before accepting: read the declarations from the head of the
	// stream, check them against the caller's grants, open under them, then drain with
	// the same reader.
	wr := ranke.NewWireReader(bytes.NewReader(buf.Bytes()))
	cons, err := wr.Constraints()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"alpha", "beta"}, cons.Branches)
	require.Empty(t, cons.Lifted, "a stream asking for no lift says so")

	c, err := seq.NewContribution(ctx, cons.Options()...)
	require.NoError(t, err)
	require.NoError(t, c.AddWire(ctx, wr), "the reader goes on from where the check left it")

	v, err := c.CompleteAndVerify(ctx)
	require.NoError(t, err)
	m, err := v.Persist(ctx)
	require.NoError(t, err)
	require.Len(t, m.Heads(), 2, "one head per branch the stream named")
	_, err = seq.Merge(ctx, m)
	require.NoError(t, err)

	// Both branches exist at the merged head, each reaching its own claim.
	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err)
	for branch, want := range map[string]ranke.Claim{"alpha": onAlpha, "beta": onBeta} {
		br, err := arc.GetBranch(ctx, branch)
		require.NoErrorf(t, err, "branch %q exists", branch)
		ok, err := br.HasClaim(ctx, want.ID())
		require.NoError(t, err)
		require.Truef(t, ok, "branch %q holds the claim the stream named for it", branch)
	}
}

// TestAddWireAbortsOnViolation: a stream that breaks its own rules stops the fill
// and stages nothing, so a server cannot half-apply a bad contribution.
func TestAddWireAbortsOnViolation(t *testing.T) {
	ctx := context.Background()
	seq, _, _ := newSequencer(t, ctx)

	// The blob does not hash to the key it is filed under.
	hash, err := ranke.HashContent([]byte("the real bytes"))
	require.NoError(t, err)
	var buf bytes.Buffer
	w := ranke.NewWireWriter(&buf, ranke.WireConstraints{Branches: []string{"alpha"}})
	require.NoError(t, w.WriteContent(ranke.ContentBlob{Hash: hash, Content: []byte("tampered")}))

	c, err := seq.NewContribution(ctx, ranke.WithReferencableBranches(ranke.BranchArchive))
	require.NoError(t, err)
	require.ErrorIs(t, c.AddWire(ctx, ranke.NewWireReader(&buf)), ranke.ErrIntegrity,
		"AddWire surfaces the stream's own violation")

	_, err = c.CompleteAndVerify(ctx)
	require.Error(t, err, "the aborted fill staged nothing")
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

	_, err := devseq.NewSequencer(ctx, noBookmarks{ranke.NewMemoryUniverse()},
		ranke.Seed([]byte("no-hist")), op, clk)
	require.ErrorIs(t, err, ranke.ErrUnsupported)
}
