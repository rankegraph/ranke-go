package ranke

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// What `V-MONO` asks of created_at, in its two halves: that a claim states the time it
// was added at all, and that the time runs forward along every reference.

// TestZeroCreatedAtIsRefusedAtEveryDoor: a claim carries the time it was added, and the
// zero instant is what an unset field reads as — year 1 where Go writes one, 1970 where
// seconds are counted from the epoch. Neither states a time, so every door refuses it:
// the builder where a caller states one, AssembleClaim where a projection rebuilds a
// claim, and verification wherever a record arrives already made.
func TestZeroCreatedAtIsRefusedAtEveryDoor(t *testing.T) {
	who, priv := newSignedContributor(t)

	for name, at := range map[string]time.Time{
		"the unix epoch": time.Unix(0, 0),
		"year one":       time.Time{}.Add(0),
	} {
		t.Run(name+"/assemble", func(t *testing.T) {
			_, err := AssembleClaim(ClaimParts{
				ID: who.ID(), Type: "source/note", Height: 1, CreatedAt: at,
			})
			require.ErrorIs(t, err, ErrCreatedAtZero,
				"parts describe a record, so an unset created_at is one a projection lost")
		})
	}

	// The builder defaults an UNSET created_at to now, so only a stated zero is the
	// caller's mistake — and the epoch is the one that arrives stated.
	_, err := NewClaim(TypeSource("note"), who).
		WithInlineContent([]byte("x")).WithEncoding(EncodingPlain).
		WithHeight(1).WithCreatedAt(time.Unix(0, 0)).Sign(priv)
	require.ErrorIs(t, err, ErrCreatedAtZero, "the builder refuses to sign a claim dated 1970")

	dated, err := NewClaim(TypeSource("note"), who).
		WithInlineContent([]byte("x")).WithEncoding(EncodingPlain).
		WithHeight(1).Sign(priv)
	require.NoError(t, err, "an unset created_at still takes the clock")
	require.False(t, NamesNoInstant(dated.Node().CreatedAt()))
}

// TestVerifyRefusesZeroCreatedAt: a real record dated at the epoch — sealed bytes, a
// sound id and signature — is refused by the closure verifier, which is the only door
// left once such bytes exist elsewhere (`V-MONO`).
func TestVerifyRefusesZeroCreatedAt(t *testing.T) {
	who, priv := newSignedContributor(t)
	c, err := NewClaim(TypeSource("note"), who).
		WithInlineContent([]byte("dated by a field nobody set")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(who)).
		WithCreatedAt(time.Unix(0, 0)).
		AllowInvalid().
		Sign(priv)
	require.NoError(t, err, "AllowInvalid still seals, so the record is real")

	g := newGraph(t, who)
	require.NoError(t, g.AddClaims(context.Background(), c))
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Len(t, run.Failures(), 1)
	require.True(t, run.Failures()[0].ID.Equal(c.ID()), "the failure names the epoch-dated claim")
	require.ErrorIs(t, run.Failures()[0].Err, ErrCreatedAtZero)
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
