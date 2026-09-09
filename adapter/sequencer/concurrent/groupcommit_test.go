package concurrent

// Group-commit tests. These reach inside the package on purpose: the batching
// step 6 performs is a race by nature, so driving the queue and the drain
// directly is the only way to assert it deterministically rather than hoping the
// scheduler lines several writers up. Behaviour visible from outside is covered
// black-box in concurrent_test.go.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// tickClock is a plain monotonic clock; these tests drive it from one goroutine.
type tickClock struct{ t time.Time }

func (c *tickClock) Tick() time.Time {
	out := c.t
	c.t = c.t.Add(time.Second)
	return out
}

// newTestSequencer stands up a Sequencer over in-memory storage and returns it
// with its Universe, its bookmark list, and the operator it signs with.
func newTestSequencer(t *testing.T, ctx context.Context) (*Sequencer, ranke.Universe, *ranke.Bookmarks, ranke.Contributor, *tickClock) {
	t.Helper()
	u := ranke.NewMemoryUniverse()
	clk := &tickClock{t: time.Unix(1000, 0).UTC()}

	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	pub, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	cc, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pub).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(clk.Tick()).
		Sign(priv)
	require.NoError(t, err)
	op, err := cc.AsContributor(ctx, nil, priv)
	require.NoError(t, err)

	s, err := NewSequencer(ctx, u, ranke.Seed([]byte(op.ID().String())), op, clk)
	require.NoError(t, err)
	_, err = helpers.Found(ctx, s, "groupcommit")
	require.NoError(t, err)
	return s, u, s.marks, op, clk
}

// persisted drives one contribution through steps 1–5 and returns it ready to
// merge, without merging it — so a test can hold several at once.
func persisted(t *testing.T, ctx context.Context, s *Sequencer, op ranke.Contributor, clk *tickClock, branch, body string) *mergable {
	t.Helper()
	c, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte(body)).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)

	contrib, err := s.NewContribution(ctx, ranke.WithReferencableBranches(ranke.BranchArchive),
		ranke.WithCreatableBranches(ranke.TargetBranches))
	require.NoError(t, err)
	require.NoError(t, contrib.AddClaims(branch, []ranke.Claim{c}))
	v, err := contrib.CompleteAndVerify(ctx)
	require.NoError(t, err)
	m, err := v.Persist(ctx)
	require.NoError(t, err)
	return m.(*mergable)
}

// enqueueAll puts each mergable on the merge queue exactly as Merge does, minus
// the race for the sequencing thread, and returns the slots to read outcomes from.
func enqueueAll(s *Sequencer, ms []*mergable) []*pending {
	ps := make([]*pending, 0, len(ms))
	s.qmu.Lock()
	defer s.qmu.Unlock()
	for _, m := range ms {
		p := &pending{heads: m.heads, ids: m.ids, done: make(chan struct{})}
		ps = append(ps, p)
		s.queue = append(s.queue, p)
	}
	return ps
}

// TestGroupCommitIsOneRevisionPerBatch is the throughput claim made concrete: a
// batch of contributions waiting at step 6 is merged in a SINGLE operation — one
// branch-table claim, one spine entry — however many are queued. That is what
// keeps the sequencing thread from becoming the bottleneck, since the cost of a
// commit is per-batch, not per-contribution.
func TestGroupCommitIsOneRevisionPerBatch(t *testing.T) {
	ctx := context.Background()
	s, u, hist, op, clk := newTestSequencer(t, ctx)

	const batch = 5
	ms := make([]*mergable, 0, batch)
	for i := range batch {
		ms = append(ms, persisted(t, ctx, s, op, clk, "main", fmt.Sprintf("note %d", i)))
	}

	before, err := hist.Len(ctx)
	require.NoError(t, err)

	ps := enqueueAll(s, ms)
	s.drain(ctx)

	after, err := hist.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after,
		"%d contributions merged in one group commit must cost exactly one archive revision", batch)

	head := s.currentHead()
	for i, p := range ps {
		select {
		case <-p.done:
		default:
			t.Fatalf("contribution %d was left in the queue by the drain", i)
		}
		require.NoErrorf(t, p.err, "contribution %d", i)
		require.Truef(t, p.head.Equal(head), "contribution %d must land at the batch's head", i)
	}

	// Every contribution in the batch is reachable from the folded branch head.
	arc, err := ranke.NewArchive(ctx, u, head)
	require.NoError(t, err)
	br, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)
	g, err := ranke.NewGraphFromClosure(ctx, br.Head(), u)
	require.NoError(t, err)
	for _, m := range ms {
		for _, id := range m.ids {
			ok, err := g.ContainsClaim(ctx, id)
			require.NoError(t, err)
			require.Truef(t, ok, "%s must be reachable after the group commit", id)
		}
	}
}

// TestGroupCommitAdvancesSeveralBranchesAtOnce: a branch table is a set of named
// edges, so one commit restates every branch the batch touched. Three branches
// therefore cost three edges on one claim, not three revisions.
func TestGroupCommitAdvancesSeveralBranchesAtOnce(t *testing.T) {
	ctx := context.Background()
	s, u, hist, op, clk := newTestSequencer(t, ctx)

	branches := []string{"main", "alpha", "beta"}
	ms := make([]*mergable, 0, len(branches))
	for _, b := range branches {
		ms = append(ms, persisted(t, ctx, s, op, clk, b, "note on "+b))
	}

	before, err := hist.Len(ctx)
	require.NoError(t, err)

	enqueueAll(s, ms)
	s.drain(ctx)

	after, err := hist.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after, "one commit, whatever the number of branches it advances")

	arc, err := ranke.NewArchive(ctx, u, s.currentHead())
	require.NoError(t, err)
	for i, b := range branches {
		br, err := arc.GetBranch(ctx, b)
		require.NoErrorf(t, err, "branch %q must exist after the commit", b)
		g, err := ranke.NewGraphFromClosure(ctx, br.Head(), u)
		require.NoError(t, err)
		ok, err := g.ContainsClaim(ctx, ms[i].ids[0])
		require.NoError(t, err)
		require.Truef(t, ok, "branch %q must hold its own contribution", b)
	}
}

// TestFoldKeepsThePriorBranchHead is the no-lost-writes mechanism in isolation:
// the fold set is the branch's CURRENT head plus the batch's, so a second commit
// cannot displace the first. Merging against the live head rather than the
// contribution's base is exactly what makes concurrent contributions safe.
func TestFoldKeepsThePriorBranchHead(t *testing.T) {
	ctx := context.Background()
	s, u, _, op, clk := newTestSequencer(t, ctx)

	first := persisted(t, ctx, s, op, clk, "main", "first")
	enqueueAll(s, []*mergable{first})
	s.drain(ctx)
	afterFirst := s.heads["main"]

	second := persisted(t, ctx, s, op, clk, "main", "second")
	enqueueAll(s, []*mergable{second})
	s.drain(ctx)

	require.False(t, afterFirst.Equal(s.heads["main"]), "the branch head advanced")

	g, err := ranke.NewGraphFromClosure(ctx, s.heads["main"], u)
	require.NoError(t, err)
	for _, id := range append(append([]ranke.Id{}, first.ids...), second.ids...) {
		ok, err := g.ContainsClaim(ctx, id)
		require.NoError(t, err)
		require.Truef(t, ok, "%s must survive the second commit", id)
	}
}

// TestDrainOnEmptyQueueIsANoop: the caller who loses the race for the sequencing
// thread still calls drain, finds its entry already committed by the winner, and
// must not mint a spurious empty revision.
func TestDrainOnEmptyQueueIsANoop(t *testing.T) {
	ctx := context.Background()
	s, _, hist, _, _ := newTestSequencer(t, ctx)

	before, err := hist.Len(ctx)
	require.NoError(t, err)
	head := s.currentHead()

	s.drain(ctx)

	after, err := hist.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after, "draining an empty queue records no revision")
	require.True(t, head.Equal(s.currentHead()), "and does not move the head")
}
