package tests

// Genesis: an archive comes into being through Found and nothing else, and a
// Sequencer standing up over a list that already records one reopens it. Before
// sd-4396c4 the constructor bootstrapped unconditionally, so a restart appended a
// second empty head above the archive and orphaned it.

import (
	"context"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/adapter/storage/mem"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// genesisFixture stands up a Sequencer over u without founding it, so a test starts
// from the state a fresh deployment is in.
func genesisFixture(t *testing.T, ctx context.Context, u ranke.Universe,
	clock *generator.Clock) (*devseq.Sequencer, ranke.BookmarkLocator) {
	t.Helper()
	self := keyedContributor(t, ctx, clock, "genesis-sequencer")
	loc := ranke.Seed([]byte("genesis-list"))
	seq, err := devseq.NewSequencer(ctx, u, loc, self, clock)
	require.NoError(t, err, "NewSequencer")
	return seq, loc
}

// TestPreGenesisRefusesEveryOperation: with no bookmark there is no head, so an
// archive to read from or contribute to would be a fiction. InGenesis is how a caller
// learns that without driving control flow off an error.
func TestPreGenesisRefusesEveryOperation(t *testing.T) {
	ctx := context.Background()
	clock := generator.NewClock(fixtureBase, time.Second)
	seq, _ := genesisFixture(t, ctx, mem.New(), clock)

	require.True(t, seq.InGenesis(), "an empty 𝒰_hist is an archive not yet founded")

	_, err := seq.GetArchive(ctx)
	require.ErrorIs(t, err, ranke.ErrSequencerGenesis, "there is no head to snapshot")

	_, err = seq.NewContribution(ctx)
	require.ErrorIs(t, err, ranke.ErrSequencerGenesis, "a contribution needs a base (k, t)")

	// BookmarkId answers throughout: it names the slot the first bookmark will take,
	// which is what a caller persists once Found has written it.
	require.NotNil(t, seq.BookmarkId())
	require.NotNil(t, seq.GetContributor(), "the Sequencer's own identity exists before the archive")
}

// TestFoundCreatesTheArchive: Found is one operation writing the Sequencer's own
// claim, the first contributor under it, k₀ and the bookmark that publishes it.
func TestFoundCreatesTheArchive(t *testing.T) {
	ctx := context.Background()
	clock := generator.NewClock(fixtureBase, time.Second)
	u := mem.New()
	seq, _ := genesisFixture(t, ctx, u, clock)

	first, err := helpers.Found(ctx, seq, "found-creates")
	require.NoError(t, err)
	require.False(t, seq.InGenesis(), "the archive exists once its first bookmark is written")

	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err)
	require.NotNil(t, arc.Head(), "the head k₀ the bookmark records")

	// The first contributor resolves against the Sequencer's own claim (`V-SIG`),
	// which is what makes the Sequencer key the only one able to sign it in. Verified
	// over its own closure, since it sits outside the archive's.
	verifyClosure(t, ctx, u, first.ID())
	verifyClosure(t, ctx, u, arc.Head())
}

// verifyClosure holds every claim in id's closure to the rule set.
func verifyClosure(t *testing.T, ctx context.Context, u ranke.Universe, id ranke.Id) {
	t.Helper()
	g, err := ranke.NewGraphFromClosure(ctx, id, u)
	require.NoError(t, err, "open the closure at %s", id)
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err(), "verify the closure at %s", id)
	require.Empty(t, run.Failures(), "the closure at %s holds no failure", id)
}

// TestFoundRefusesASecondGenesis: genesis happens once in an archive's lifetime, and
// the list already holding an entry is what says it has happened — so the guarantee
// is held here rather than by the caller remembering.
func TestFoundRefusesASecondGenesis(t *testing.T) {
	ctx := context.Background()
	clock := generator.NewClock(fixtureBase, time.Second)
	u := mem.New()
	seq, loc := genesisFixture(t, ctx, u, clock)

	_, err := helpers.Found(ctx, seq, "refuses-second")
	require.NoError(t, err)

	_, err = helpers.Found(ctx, seq, "refuses-second")
	require.Error(t, err, "a second Found on the same Sequencer is refused")

	// And on a fresh Sequencer over the same list, which is the restart case.
	self := keyedContributor(t, ctx, clock, "genesis-sequencer")
	restarted, err := devseq.NewSequencer(ctx, u, loc, self, clock)
	require.NoError(t, err)
	_, err = helpers.Found(ctx, restarted, "refuses-second")
	require.Error(t, err, "a restart may not found a second archive over the same list")
}

// TestRestartReopensTheArchive is the defect ranke-db reported: the constructor used
// to mint k₀ and append it unconditionally, so a restart published a fresh empty head
// at the next free slot and left the real archive unreachable behind it.
func TestRestartReopensTheArchive(t *testing.T) {
	ctx := context.Background()
	clock := generator.NewClock(fixtureBase, time.Second)
	u := mem.New()
	seq, loc := genesisFixture(t, ctx, u, clock)
	self := seq.GetContributor()

	priv := helpers.FoundedKey("restart")
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	firstClaim, err := seq.Found(ctx, pubkey)
	require.NoError(t, err)
	firstUser, err := firstClaim.AsContributor(ctx, nil, priv)
	require.NoError(t, err)

	note, err := ranke.NewClaim(ranke.TypeSource("note"), firstUser).
		WithInlineContent([]byte("written before the restart")).
		WithEncoding(ranke.EncodingPlain).
		WithCreatedAt(clock.Tick()).
		WithAutoHeight(ctx, u).
		Sign()
	require.NoError(t, err)
	// The first contributor's claim rides in with the first contribution that cites
	// it: Found leaves it outside any closure, which is what lets a crash mid-genesis
	// retry, so nothing in the archive can reference it until a branch holds it.
	head, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{firstClaim, note})
	require.NoError(t, err)

	// The restart: a new Sequencer over the same list, as a process relaunch builds.
	restarted, err := devseq.NewSequencer(ctx, u, loc, self, clock)
	require.NoError(t, err)
	require.False(t, restarted.InGenesis(), "the list records an archive, so this is no genesis")

	arc, err := restarted.GetArchive(ctx)
	require.NoError(t, err)
	require.True(t, head.Equal(arc.Head()),
		"a restart reopens the head the list records, rather than founding another")

	// And the branch it reopened still reaches what was written before the restart —
	// the branch heads come back with the head, so the next merge folds onto them
	// instead of publishing the branch as the new claims alone.
	after, err := ranke.NewClaim(ranke.TypeSource("note"), firstUser).
		WithInlineContent([]byte("written after the restart")).
		WithEncoding(ranke.EncodingPlain).
		WithCreatedAt(clock.Tick()).
		WithAutoHeight(ctx, u).
		Sign()
	require.NoError(t, err)
	_, err = helpers.Contribute(ctx, restarted, "main", []ranke.Claim{after})
	require.NoError(t, err)

	arc, err = restarted.GetArchive(ctx)
	require.NoError(t, err)
	br, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)
	g, err := ranke.NewGraphFromClosure(ctx, br.Head(), u)
	require.NoError(t, err)
	for _, want := range []ranke.Claim{note, after} {
		ok, err := g.ContainsClaim(ctx, want.ID())
		require.NoError(t, err)
		require.Truef(t, ok, "%s must stay reachable across the restart", want.ID())
	}
}
