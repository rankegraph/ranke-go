package ranke

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// An honestly-built closure verifies, under either scheme `V-SIGN` names: the
// signature check that verify.go runs per claim, read from the graph rather than
// from the envelope primitives (-> sign_test.go for those).

// TestVerifyGraphSignedByItsContributor: a claim signs under the key its contributor
// carries, with no key passed at the call — the other half of TestVerifySignedGraph,
// which hands one over explicitly.
func TestVerifyGraphSignedByItsContributor(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	require.NoError(t, g.AddClaims(context.Background(), srcClaim(t, root, "hello")))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err(), "no terminal error")
	require.Empty(t, run.Failures(), "the closure verifies")
}

// TestVerifySignedGraph: an honestly-built signed graph verifies — the
// per-claim signature check passes across the closure.
func TestVerifySignedGraph(t *testing.T) {
	alice, alicePriv := newSignedContributor(t)
	g := newGraph(t, alice)
	src, err := NewClaim(TypeSource("email"), alice).
		WithInlineContent([]byte("From: alice\r\n\r\nhi")).
		WithEncoding(EncodingMessage("rfc822")).
		WithHeight(HeightOf(alice)).
		Sign(alicePriv)
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), src))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "signed closure verifies")
}

// TestVerifySignedGraphUnderP256: the whole story under the second scheme `V-SIGN`
// names — a contributor whose content is a p256-pub key, a source signed by it, and a
// closure that verifies. A key held in a vault publishing no Ed25519 is what this is for.
func TestVerifySignedGraphUnderP256(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	pubkey, err := EncodePublicKey(priv.Public())
	require.NoError(t, err)
	c, err := NewClaim(NodeContributor, nil).
		WithInlineContent(pubkey).WithEncoding(EncodingOctetStream).Sign(priv)
	require.NoError(t, err)
	alice, err := c.AsContributor(context.Background(), nil, priv)
	require.NoError(t, err)

	g := newGraph(t, alice)
	src, err := NewClaim(TypeSource("email"), alice).
		WithInlineContent([]byte("From: alice\r\n\r\nhi")).
		WithEncoding(EncodingMessage("rfc822")).
		WithHeight(HeightOf(alice)).
		Sign(priv)
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), src))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "a closure signed under ES256 verifies")
}
