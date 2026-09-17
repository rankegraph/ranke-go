package ranke

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInlineContentInID: inline content is committed by the claim id (§Content),
// so two claims identical but for a single content byte have different ids —
// and an inline claim carries no content_hash.
func TestInlineContentInID(t *testing.T) {
	alice := contributor(t)
	at := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // same timestamp both builds → only content differs
	build := func(body []byte) Claim {
		c, err := NewClaim(TypeSource("note"), alice).
			WithInlineContent(body).
			WithEncoding(EncodingPlain).
			WithCreatedAt(at).
			WithHeight(HeightOf(alice)).
			Sign()
		require.NoError(t, err)
		return c
	}
	a := build([]byte("the quick brown fox"))
	b := build([]byte("the quick brown fpx")) // one byte differs
	require.False(t, a.ID().Equal(b.ID()), "a one-byte content change must change the id")
	require.Nil(t, a.Node().GetContentHash(), "inline content carries no content_hash")
}

// TestInlinePreimageConsistency: the bytes the id is signed over (encodeNode)
// equal the node preimage verification extracts from the stored claim, so an
// inline-content claim round-trips and verifies against its id.
func TestInlinePreimageConsistency(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("hello world")).
		WithEncoding(EncodingPlain).
		WithCreatedAt(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)

	env, err := c.Envelope()
	require.NoError(t, err)
	pre, err := envelopePayload(env)
	require.NoError(t, err)
	enc, err := encodeNode(c.unwrap().node, c.unwrap().edges, nil)
	require.NoError(t, err)
	require.True(t, bytes.Equal(pre, enc), "the envelope's payload must equal encodeNode (the signed bytes)")

	// The serialized-claim path unwraps rather than rebuilds, so it lands on the
	// same bytes (`R-QDETAIL`).
	raw, err := c.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	require.True(t, bytes.Equal(raw, enc), "EncodeCBOR(original) must be the payload the envelope carries")
}
