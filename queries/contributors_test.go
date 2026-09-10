package queries_test

// The helpers answer against a real archive rather than a stub: what they are worth
// is that the RQL they build is the RQL that works, which only a live read shows.

import (
	"context"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/adapter/storage/mem"
	"github.com/rankegraph/ranke-go/queries"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// archiveKey is the label whose key the first contributor carries, so a test can hold
// the private half the way a deployment's first user does.
const archiveKey = "queries"

// fixture is a founded archive and what a test needs to write to it further.
type fixture struct {
	seq    *devseq.Sequencer
	u      ranke.Universe
	clock  *generator.Clock
	first  ranke.Contributor
	pubkey []byte
}

// founded stands up an archive holding its first contributor — the state a deployment
// restarts into.
func founded(t *testing.T, ctx context.Context) *fixture {
	t.Helper()
	clock := generator.NewClock(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Second)
	u := mem.New()

	priv := helpers.FoundedKey("queries-sequencer")
	opPub, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	self, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(opPub).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(clock.Tick()).
		Sign(priv)
	require.NoError(t, err)
	op, err := self.AsContributor(ctx, nil, priv)
	require.NoError(t, err)

	seq, err := devseq.NewSequencer(ctx, u, ranke.Seed([]byte("queries-list")), op, clock)
	require.NoError(t, err)
	first, err := helpers.Found(ctx, seq, archiveKey)
	require.NoError(t, err)

	pubkey, err := ranke.EncodePublicKey(helpers.FoundedKey(archiveKey).Public())
	require.NoError(t, err)
	return &fixture{seq: seq, u: u, clock: clock, first: first, pubkey: pubkey}
}

// archive is the fixture's current snapshot.
func (f *fixture) archive(t *testing.T, ctx context.Context) ranke.Archive {
	t.Helper()
	arc, err := f.seq.GetArchive(ctx)
	require.NoError(t, err)
	return arc
}

// register contributes a contributor for label's key into branch, signed by the first
// contributor — an ordinary registration, the way one user vouches for another.
func (f *fixture) register(t *testing.T, ctx context.Context, branch, label string) ranke.Claim {
	t.Helper()
	holder, err := f.first.AsContributor(ctx, nil, helpers.FoundedKey(archiveKey))
	require.NoError(t, err)
	pubkey, err := ranke.EncodePublicKey(helpers.FoundedKey(label).Public())
	require.NoError(t, err)
	c, err := ranke.NewClaim(ranke.NodeContributor, holder).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(f.clock.Tick()).
		WithAutoHeight(ctx, f.u).
		Sign()
	require.NoError(t, err)
	_, err = helpers.Contribute(ctx, f.seq, branch, []ranke.Claim{c})
	require.NoError(t, err)
	return c
}

// ids names each claim, for an order-free comparison.
func ids(claims []ranke.Claim) []string {
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.ID().String())
	}
	return out
}

// TestContributorsReachesBothIdentities: an archive fresh from Found holds two, the
// Sequencer's own initial claim and the first contributor beneath it.
func TestContributorsReachesBothIdentities(t *testing.T) {
	ctx := context.Background()
	f := founded(t, ctx)

	all, err := queries.Contributors(ctx, f.archive(t, ctx), ranke.BranchArchive)
	require.NoError(t, err)
	require.Len(t, all, 2, "the Sequencer's initial claim and the contributor it registered")
	require.Contains(t, ids(all), f.first.ID().String())
}

// TestContributorsIsBranchScoped: a named branch answers for what it reaches, which
// after founding is the first contributor and the Sequencer claim below it.
func TestContributorsIsBranchScoped(t *testing.T) {
	ctx := context.Background()
	f := founded(t, ctx)

	all, err := queries.Contributors(ctx, f.archive(t, ctx), helpers.FoundBranch)
	require.NoError(t, err)
	require.Contains(t, ids(all), f.first.ID().String(), "the branch heads on it")
}

// TestContributorsByKeyMatchesTheHolder: the question a client actually asks — which
// claims in the branch I write to carry my key, since one of those ids is what it must
// reference to sign (`V-SIG`). A freshly founded archive registered it once.
func TestContributorsByKeyMatchesTheHolder(t *testing.T) {
	ctx := context.Background()
	f := founded(t, ctx)

	got, err := queries.ContributorsByKey(ctx, f.archive(t, ctx), helpers.FoundBranch, f.pubkey)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, f.first.ID().Equal(got[0].ID()))
}

// TestContributorsByKeyIsBoundedByTheBranch: a contributor registered in one branch is
// no identity in another until a reference pulls it there. Asking a branch answers
// what may sign in it; asking $archive answers what the archive holds anywhere, which
// is the question a caller means when it intends to pull one in.
func TestContributorsByKeyIsBoundedByTheBranch(t *testing.T) {
	ctx := context.Background()
	f := founded(t, ctx)

	elsewhere := f.register(t, ctx, "elsewhere", "outsider")
	outsider, err := ranke.EncodePublicKey(helpers.FoundedKey("outsider").Public())
	require.NoError(t, err)

	got, err := queries.ContributorsByKey(ctx, f.archive(t, ctx), helpers.FoundBranch, outsider)
	require.NoError(t, err)
	require.Empty(t, got, "registered in another branch, so it may not sign here")

	got, err = queries.ContributorsByKey(ctx, f.archive(t, ctx), ranke.BranchArchive, outsider)
	require.NoError(t, err)
	require.Equal(t, []string{elsewhere.ID().String()}, ids(got), "the archive holds it")
}

// TestContributorsByKeyFindsEveryRegistration: no rule makes a `pubkey` unique, so one
// key registered twice is two identities — distinct claims carrying distinct
// provenance. Answering with one would pick between them silently.
func TestContributorsByKeyFindsEveryRegistration(t *testing.T) {
	ctx := context.Background()
	f := founded(t, ctx)

	// The same key registered again into the same branch, under the first contributor
	// rather than under the Sequencer, so the claim differs where the key does not.
	again := f.register(t, ctx, helpers.FoundBranch, archiveKey)
	require.False(t, f.first.ID().Equal(again.ID()), "same key, another claim")

	got, err := queries.ContributorsByKey(ctx, f.archive(t, ctx), helpers.FoundBranch, f.pubkey)
	require.NoError(t, err)
	require.Len(t, got, 2, "both registrations answer for the key")
	require.ElementsMatch(t, []string{f.first.ID().String(), again.ID().String()}, ids(got))
}

// TestContributorsByKeyIsEmptyForAStranger: a key the archive never registered matches
// nothing, which is an answer rather than a failure.
func TestContributorsByKeyIsEmptyForAStranger(t *testing.T) {
	ctx := context.Background()
	f := founded(t, ctx)

	stranger, err := ranke.EncodePublicKey(helpers.FoundedKey("never-registered").Public())
	require.NoError(t, err)
	got, err := queries.ContributorsByKey(ctx, f.archive(t, ctx), ranke.BranchArchive, stranger)
	require.NoError(t, err)
	require.Empty(t, got)
}
