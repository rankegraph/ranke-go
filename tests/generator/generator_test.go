package generator

import (
	"context"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/stretchr/testify/require"
)

// TestDatedFormsAreSortable: every corner in datedForms must project to a temporal
// midpoint (`R-QTEMPORAL`). One that failed to parse would sort with the claims carrying
// no `dated` at all, quietly emptying the fixtures built on it.
func TestDatedFormsAreSortable(t *testing.T) {
	for _, form := range datedForms {
		_, ok := ranke.TemporalMidpointMs(form)
		require.True(t, ok, "%q must parse as a dated value", form)
	}
}

// The determinism foundation: a (seed, index) fixes an Ed25519 key via a
// spec'd byte recipe, so a contributor built from it has a reproducible id —
// the property the whole generator (and the cross-impl conformance promise)
// rests on.

var genBase = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func TestContributorDeterministic(t *testing.T) {
	ctx := context.Background()

	a, err := contributor(ctx, 42, 0, genBase)
	require.NoError(t, err)
	b, err := contributor(ctx, 42, 0, genBase)
	require.NoError(t, err)
	require.True(t, a.ID().Equal(b.ID()), "same seed+index+time → identical contributor id")
}

func TestContributorDistinctByIndexAndSeed(t *testing.T) {
	ctx := context.Background()

	base, err := contributor(ctx, 42, 0, genBase)
	require.NoError(t, err)

	otherIndex, err := contributor(ctx, 42, 1, genBase)
	require.NoError(t, err)
	require.False(t, base.ID().Equal(otherIndex.ID()), "a different index → different key → different id")

	otherSeed, err := contributor(ctx, 43, 0, genBase)
	require.NoError(t, err)
	require.False(t, base.ID().Equal(otherSeed.ID()), "a different master seed → different id")
}

// TestKeyDerivationStable pins the low-level recipe: same (master,index) →
// identical key bytes; the two axes are independent.
func TestKeyDerivationStable(t *testing.T) {
	require.Equal(t, keyForIndex(7, 3), keyForIndex(7, 3), "deterministic")
	require.NotEqual(t, keyForIndex(7, 3), keyForIndex(7, 4), "index varies the key")
	require.NotEqual(t, keyForIndex(7, 3), keyForIndex(8, 3), "seed varies the key")
}

// TestClockMonotonic: the clock advances deterministically from its base.
func TestClockMonotonic(t *testing.T) {
	c := NewClock(genBase, time.Second)
	require.Equal(t, genBase, c.Now(), "Now peeks without advancing")
	first := c.Tick()
	second := c.Tick()
	require.Equal(t, genBase, first)
	require.True(t, second.After(first), "each tick advances")
}
