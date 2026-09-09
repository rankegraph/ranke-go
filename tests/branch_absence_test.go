// A missing branch has to be classifiable by the caller that asked for it. This
// package imports ranke as any consumer does, so it sees only the exported API —
// which is the point: were the sentinel unexported again, this file would not build.
package tests

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

// TestGetBranchAbsenceIsMatchable: the not-found answers to both readings —
// ErrBranchNotFound to tell a missing branch from a missing claim, ErrNotFound to treat
// every absence alike. Unmatchable, a caller had to ask the store a second time.
func TestGetBranchAbsenceIsMatchable(t *testing.T) {
	ctx := context.Background()
	clock := generator.NewClock(fixtureBase, time.Second)
	self := keyedContributor(t, ctx, clock, "sequencer")

	u := mem.New()
	seq, err := devseq.NewSequencer(ctx, u, ranke.Seed([]byte(self.ID().String())), self, clock)
	require.NoError(t, err, "NewSequencer")
	_, err = helpers.Found(ctx, seq, "branch-absence")
	require.NoError(t, err, "Found")

	em, err := ranke.NewClaim(ranke.TypeSource("note"), self).
		WithInlineContent([]byte("a note")).
		WithEncoding(ranke.EncodingPlain).
		WithCreatedAt(clock.Tick()).
		WithHeight(ranke.HeightOf(self)).
		Sign()
	require.NoError(t, err, "sign note")
	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{em})
	require.NoError(t, err, "contribute to main")

	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err, "GetArchive")

	b, err := arc.GetBranch(ctx, "no-such-branch")
	require.Error(t, err, "an absent branch is an error")
	require.Nil(t, b)
	require.ErrorIs(t, err, ranke.ErrBranchNotFound, "the specific reading")
	require.ErrorIs(t, err, ranke.ErrNotFound, "the general reading")

	// The absence is a distinct condition, so it must not read as any other failure a
	// caller switches on — that conflation is what made a second query necessary.
	for _, other := range []error{ranke.ErrIntegrity, ranke.ErrClosed, ranke.ErrUnsupported} {
		require.NotErrorIs(t, err, other)
	}
	require.NotErrorIs(t, err, context.Canceled)

	// Matching two sentinels leaves the message the specific one, where an errors.Join
	// would have glued both together.
	require.Equal(t, ranke.ErrBranchNotFound.Error(), err.Error())

	// A branch that is there answers cleanly, so the sentinel marks absence alone.
	present, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)
	require.NotNil(t, present)
}

// The two in-package callers that switch on this sentinel go through the contribution
// path, so a constraint naming an absent branch must still be refused rather than
// mistaken for a storage failure.
func TestAbsentBranchStillRefusesAContribution(t *testing.T) {
	ctx := context.Background()
	clock := generator.NewClock(fixtureBase, time.Second)
	self := keyedContributor(t, ctx, clock, "sequencer")

	u := mem.New()
	seq, err := devseq.NewSequencer(ctx, u, ranke.Seed([]byte(self.ID().String())), self, clock)
	require.NoError(t, err, "NewSequencer")
	_, err = helpers.Found(ctx, seq, "branch-absence")
	require.NoError(t, err, "Found")

	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err, "GetArchive")

	_, err = arc.GetBranch(ctx, "absent")
	require.ErrorIs(t, err, ranke.ErrBranchNotFound)
	require.ErrorIs(t, err, ranke.ErrNotFound)

	has, err := arc.HasBranch(ctx, "absent")
	require.NoError(t, err)
	require.False(t, has, "HasBranch agrees, so the probe was only ever a classifier")
}
