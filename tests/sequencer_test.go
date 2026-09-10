// Sequencer tests — the write path through the ranke.Sequencer contract:
// bootstrap at an empty branch table, a head that advances per commit, and
// deterministic head ids from a fixed clock.
package tests

import (
	"context"
	"testing"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// TestSequencerBootstrap: a fresh archive opens on one branch, headed on the first
// contributor. The empty table k₀ is still there, one table below — `V-ARCHIVEHEIGHT`
// allows it a single reference, so the branch that reaches the contributor has to sit
// above it, and that is the table the bookmark names.
func TestSequencerBootstrap(t *testing.T) {
	ctx := context.Background()
	f := memFixture(t, ctx)

	arc := f.snapshot(t)
	brs, err := arc.GetBranches(ctx)
	require.NoError(t, err, "GetBranches")
	require.Len(t, brs, 1, "founding binds the first contributor to one branch")
	require.Equal(t, helpers.FoundBranch, brs[0].Name())
	require.True(t, f.self.ID().Equal(brs[0].Head()),
		"which heads on the contributor, so the archive alone yields its id")

	has, err := arc.HasBranch(ctx, "unfounded")
	require.NoError(t, err, "HasBranch")
	require.False(t, has, "and on no other")
}

// TestSequencerAdvancesHead: each merge advances the head, and the receipt names
// the same k the next snapshot sits at.
func TestSequencerAdvancesHead(t *testing.T) {
	ctx := context.Background()
	f := memFixture(t, ctx)

	h0 := f.head(t)
	h1 := f.write(t, f.email(t, f.self, "a@example.com", "b@example.com", "one\r\n"))
	require.False(t, h1.Equal(h0), "the first commit advanced the head")
	require.True(t, h1.Equal(f.head(t)), "the receipt names the snapshot's k")

	h2 := f.write(t, f.email(t, f.self, "a@example.com", "b@example.com", "two\r\n"))
	require.False(t, h2.Equal(h1), "the second commit advanced it again")
}

// TestSequencerDeterministic: same clock, self, and claims → identical head
// ids, the property the cross-implementation conformance vectors rely on.
func TestSequencerDeterministic(t *testing.T) {
	ctx := context.Background()
	run := func() ranke.Id {
		f := memFixture(t, ctx)
		return f.write(t, f.email(t, f.self, "a@example.com", "b@example.com", "same\r\n"))
	}
	require.True(t, run().Equal(run()), "identical inputs yield identical head ids")
}
