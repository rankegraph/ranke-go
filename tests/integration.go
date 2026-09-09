// package: tests / conformance
// type:    test
// job:     public IntegrationTest entry point + a per-scenario fixture that drives the reference
// dev Sequencer over a pluggable storage backend
// limits:  no scenario bodies here; those live alongside (-> tests/scenarios.go)
//
// Package tests is the public conformance suite: a backend (a Universe) proves it
// conforms to the Ranke-Graph ADT by running IntegrationTest, which stands the dev
// Sequencer over it (its bookmark list in an in-process 𝒰_hist) and validates every
// closure it reads back.
package tests

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/adapter/storage/mem"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// Backend opens a fresh, isolated storage tier for one scenario: a Universe for
// claims + content, over the shared clock.
type Backend func(t *testing.T, clock *generator.Clock) (ranke.Universe, error)

// IntegrationTest runs the full Ranke-Graph integration suite against the storage
// backend, giving each scenario its own fixture: a fresh backend and Sequencer.
func IntegrationTest(t *testing.T, ctx context.Context, backend Backend) {
	t.Helper()
	t.Run("AliceEmail", func(t *testing.T) {
		runAliceEmail(t, newFixture(t, ctx, backend))
	})
	t.Run("MultiHeadAutoConsolidates", func(t *testing.T) {
		runMultiHeadAutoConsolidates(t, newFixture(t, ctx, backend))
	})
	t.Run("SuccessiveAddsAccumulate", func(t *testing.T) {
		runSuccessiveAddsAccumulate(t, newFixture(t, ctx, backend))
	})
}

// fixture is one scenario's write path, on the clock that makes created_at reproduce.
type fixture struct {
	ctx   context.Context
	u     ranke.Universe
	seq   ranke.Sequencer // the contract, so a scenario exercises what any Sequencer offers
	clock *generator.Clock
	self  ranke.Contributor // signs branch tables and authors content: contributor 0, the operator
}

// fixtureBase is the fixed instant the shared clock starts at, so a run reproduces byte-identically.
var fixtureBase = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func newFixture(t *testing.T, ctx context.Context, backend Backend) *fixture {
	t.Helper()
	clock := generator.NewClock(fixtureBase, time.Second)
	self := keyedContributor(t, ctx, clock, "sequencer")
	u, err := backend(t, clock)
	require.NoError(t, err, "open backend")
	seq, err := devseq.NewSequencer(ctx, u, ranke.Seed([]byte(self.ID().String())), self, clock)
	require.NoError(t, err, "stand up dev Sequencer")
	require.True(t, seq.InGenesis(), "a fresh 𝒰_hist is an archive not yet founded")
	_, err = helpers.Found(ctx, seq, "integration")
	require.NoError(t, err, "found the archive")
	return &fixture{ctx: ctx, u: u, seq: seq, clock: clock, self: self}
}

// write advances the "main" branch with claims in topological order, returning
// head k′.
func (f *fixture) write(t *testing.T, claims ...ranke.Claim) ranke.Id {
	t.Helper()
	head, err := helpers.Contribute(f.ctx, f.seq, "main", claims)
	require.NoError(t, err, "contribute to main")
	return head
}

// snapshot returns a fresh immutable read view at the current head.
func (f *fixture) snapshot(t *testing.T) ranke.Archive {
	t.Helper()
	arc, err := f.seq.GetArchive(f.ctx)
	require.NoError(t, err, "GetArchive")
	return arc
}

// head is the archive head k a fresh snapshot sits at.
func (f *fixture) head(t *testing.T) ranke.Id {
	t.Helper()
	return f.snapshot(t).Head()
}

// mainHead returns the root of branch "main" in a fresh snapshot.
func (f *fixture) mainHead(t *testing.T, arc ranke.Archive) ranke.Id {
	t.Helper()
	require.True(t, must(arc.HasBranch(f.ctx, "main")), "branch main exists")
	b, err := arc.GetBranch(f.ctx, "main")
	require.NoError(t, err, "GetBranch main")
	return b.Head()
}

// validate walks the closure from head and verifies every claim (§5.10).
func (f *fixture) validate(t *testing.T, head ranke.Id) {
	t.Helper()
	g, err := ranke.NewGraphFromClosure(f.ctx, head, f.u)
	require.NoError(t, err, "open closure")
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err(), "closure verification")
	require.Empty(t, run.Failures(), "closure has no verification failures")
}

// keyedContributor builds a root contributor from seedTag, its pubkey its content (§5.7).
func keyedContributor(t *testing.T, ctx context.Context, clock *generator.Clock, seedTag string) ranke.Contributor {
	t.Helper()
	seed := sha256.Sum256([]byte(seedTag))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err, "encode pubkey")
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(clock.Tick()).
		Sign(priv)
	require.NoError(t, err, "sign contributor")
	ctr, err := c.AsContributor(ctx, nil, priv)
	require.NoError(t, err, "as contributor")
	return ctr
}

// email builds a source/email claim attributed to ctr, stamped from the
// shared clock and sitting one above its contributor (§4.1).
func (f *fixture) email(t *testing.T, ctr ranke.Contributor, from, to, body string) ranke.Claim {
	t.Helper()
	c, err := ranke.NewClaim(ranke.TypeSource("email"), ctr).
		WithInlineContent([]byte("From: " + from + "\r\nTo: " + to + "\r\n\r\n" + body)).
		WithEncoding(ranke.EncodingMessage("rfc822")).
		WithCreatedAt(f.clock.Tick()).
		WithHeight(ranke.HeightOf(ctr)).
		Sign()
	require.NoError(t, err, "sign email")
	return c
}

// entity builds an entity/<sub> claim with a derivation/source edge back to source —
// the extraction-pipeline shape, which cites what it read.
func (f *fixture) entity(t *testing.T, ctr ranke.Contributor, sub, label string, source ranke.Claim) ranke.Claim {
	t.Helper()
	de, err := ranke.NewEdge(ranke.EdgeConfig{Reference: source.ID(), Type: ranke.TypeDerivation("source")})
	require.NoError(t, err, "derivation edge")
	c, err := ranke.NewClaim(ranke.TypeEntity(sub), ctr).
		WithInlineContent([]byte(label)).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(de).
		WithCreatedAt(f.clock.Tick()).
		WithHeight(ranke.HeightOf(ctr, source)).
		Sign()
	require.NoError(t, err, "sign entity")
	return c
}

// memFixture builds a fixture over an in-memory Universe — the backend the
// sequencer unit tests drive directly.
func memFixture(t *testing.T, ctx context.Context) *fixture {
	t.Helper()
	return newFixture(t, ctx, func(_ *testing.T, _ *generator.Clock) (ranke.Universe, error) {
		return mem.New(), nil
	})
}
