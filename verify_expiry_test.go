package ranke

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// `R-DEXPIRY`'s second sentence: a contribution/expiry edge against a contributor
// carries an earlier pubkey_expires_after and moves the end of the window to it.
// Without this the first sentence stands alone and a revocation has no effect — a
// claim signed after a compromised key was retired would verify clean.

const (
	expiryDeclared = "2026-12-31T00:00:00.000000000Z" // what the contributor claim declares
	expiryRevoked  = "2026-09-01T00:00:00.000000000Z" // what the expiry edge shortens it to
)

// stamp parses one of the fixed instants above.
func stamp(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := parseRFC3339Nano(s)
	require.NoError(t, err)
	return at
}

// expiryEdge is the contribution/expiry edge naming who, carrying the earlier end.
// The date rides on the EDGE: a claim's own pubkey_expires_after states its own key's
// window, so a successor contributor could not carry a predecessor's revocation there.
func expiryEdge(t *testing.T, who Claim, until string) Edge {
	t.Helper()
	e, err := NewEdge(EdgeConfig{
		Reference: who.ID(),
		Type:      EdgeTypeExpiry,
		Fields:    map[string]string{FieldPubkeyExpiresAfter: until},
	})
	require.NoError(t, err)
	return e
}

// revokedGraph stages a contributor whose key runs to expiryDeclared, a claim dated at,
// and a revocation carried by carry — either a contribution/expiry claim or the
// contribution/contributor claim introducing a successor key. It returns the failures.
func revokedGraph(t *testing.T, at time.Time, carry func(*testing.T, Contributor, Claim) Claim) []Failure {
	t.Helper()
	ctx := context.Background()
	who, _ := windowedContributor(t, "", expiryDeclared, stamp(t, "2026-06-01T00:00:00.000000000Z"))
	g := newGraph(t, who)

	signed := signedAt(t, who, at)
	require.NoError(t, g.AddClaims(ctx, signed))
	require.NoError(t, g.AddClaims(ctx, carry(t, who, signed)))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	return run.Failures()
}

// byExpiryClaim carries the revocation on a contribution/expiry claim, the first of the
// two carriers `R-DEXPIRY` names.
func byExpiryClaim(t *testing.T, who Contributor, _ Claim) Claim {
	t.Helper()
	c, err := NewClaim(NodeExpiry, who).
		WithEdges(expiryEdge(t, who, expiryRevoked)).
		WithHeight(HeightOf(who)).
		WithCreatedAt(stamp(t, "2026-08-01T00:00:00.000000000Z")).
		Sign()
	require.NoError(t, err)
	return c
}

// TestExpiryEdgeShortensTheWindow: a claim dated after the revoked end fails, where the
// contributor's own declared end would have admitted it.
func TestExpiryEdgeShortensTheWindow(t *testing.T) {
	fs := revokedGraph(t, stamp(t, "2026-12-01T00:00:00.000000000Z"), byExpiryClaim)

	require.NotEmpty(t, fs, "a claim signed after the revocation must fail")
	var found bool
	for _, f := range fs {
		if errors.Is(f.Err, ErrKeyExpired) {
			found = true
		}
	}
	require.True(t, found, "the failure is the key window, not something else")
}

// TestExpiryEdgeAdmitsWhatPrecedesIt is the control: the same graph, a claim dated before
// the revoked end. Without it the rule could pass by refusing everything.
func TestExpiryEdgeAdmitsWhatPrecedesIt(t *testing.T) {
	fs := revokedGraph(t, stamp(t, "2026-08-15T00:00:00.000000000Z"), byExpiryClaim)
	require.Empty(t, fs, "a claim signed before the revocation still verifies")
}

// TestExpiryOnSuccessorContributorHasTheSameEffect: `R-DEXPIRY` names two carriers, and
// the contribution/contributor claim introducing the successor key is the second. It
// carries its own window in its fields and the predecessor's revocation on its edge, so
// only an edge-borne date can express both at once.
func TestExpiryOnSuccessorContributorHasTheSameEffect(t *testing.T) {
	successor := func(t *testing.T, who Contributor, _ Claim) Claim {
		t.Helper()
		// The successor's key is this claim's CONTENT; the predecessor still signs it,
		// which is how a rotation is attested by the key it retires.
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		pub, err := EncodePublicKey(priv.Public())
		require.NoError(t, err)
		c, err := NewClaim(NodeContributor, who).
			WithInlineContent(pub).
			WithEncoding(EncodingOctetStream).
			WithField(FieldPubkeyValidFrom, expiryRevoked). // the successor's own window
			WithEdges(expiryEdge(t, who, expiryRevoked)).   // the predecessor's revocation
			WithHeight(HeightOf(who)).
			WithCreatedAt(stamp(t, "2026-08-01T00:00:00.000000000Z")).
			Sign()
		require.NoError(t, err)
		return c
	}

	after := revokedGraph(t, stamp(t, "2026-12-01T00:00:00.000000000Z"), successor)
	require.NotEmpty(t, after, "a successor's expiry edge revokes the predecessor too")

	before := revokedGraph(t, stamp(t, "2026-08-15T00:00:00.000000000Z"), successor)
	require.Empty(t, before, "and admits what precedes it, as the other carrier does")
}

// TestDeclaredWindowStandsWithoutRevocation: with no expiry edge anywhere, the window is
// the one the contributor declared — the first sentence, unchanged.
func TestDeclaredWindowStandsWithoutRevocation(t *testing.T) {
	none := func(t *testing.T, who Contributor, signed Claim) Claim {
		t.Helper()
		return srcClaim(t, who, "carries no revocation")
	}

	inside := revokedGraph(t, stamp(t, "2026-06-01T00:00:00.000000000Z"), none)
	require.Empty(t, inside, "inside the declared window and unrevoked: still valid")
}
