package ranke

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/multiformats/go-multicodec"
	"github.com/stretchr/testify/require"
	cose "github.com/veraison/go-cose"
)

// Foundation unit tests for the signing primitives exercised DIRECTLY — no Graph, no
// claim. (The end-to-end story lives a layer up in tests/sign_test.go.) These pin
// multikey pubkey encoding, and the envelope: seal then verify accepts, while
// tampering, a wrong key and a missing key are each refused. Authenticity (D3) and
// verifiability (D5) reduce to these.

func ed25519Keys(t *testing.T) (ed25519.PrivateKey, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err := EncodePublicKey(priv.Public())
	require.NoError(t, err)
	return priv, pubkey
}

// p256Keys is ed25519Keys for the second scheme `V-SIGN` names.
func p256Keys(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	pubkey, err := EncodePublicKey(priv.Public())
	require.NoError(t, err)
	return priv, pubkey
}

// --- public-key encoding ------------------------------------------------

// TestEncodeDecodePublicKeyRoundTrip: a multikey-encoded pubkey decodes
// back to the same Ed25519 key, naming its scheme (self-describing, §4.1).
func TestEncodeDecodePublicKeyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_ = priv

	encoded, err := EncodePublicKey(pub)
	require.NoError(t, err)

	code, decoded, err := DecodePublicKey(encoded)
	require.NoError(t, err)
	require.Equal(t, multicodec.Ed25519Pub, code, "scheme is named in the encoding")
	edPub, ok := decoded.(ed25519.PublicKey)
	require.True(t, ok, "decodes to an ed25519 public key")
	require.Equal(t, pub, edPub, "the key bytes survive the round-trip")
}

// TestEncodePublicKeyRejectsUnsupported: a key type the scheme table
// doesn't know is refused, not silently mis-encoded. `V-SIGN` names ECDSA over P-256
// alone, so another curve is as foreign as another algorithm.
func TestEncodePublicKeyRejectsUnsupported(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	_, err = EncodePublicKey(ec.Public())
	require.Error(t, err, "P-384 is not a curve `V-SIGN` names")

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	_, err = EncodePublicKey(rsaKey.Public())
	require.Error(t, err, "RSA is not a scheme `V-SIGN` names")
}

// TestEncodeDecodeP256RoundTrip: a P-256 key frames as the compressed point under
// `p256-pub` and decodes back to the same key (`V-SIGN`).
func TestEncodeDecodeP256RoundTrip(t *testing.T) {
	priv, pubkey := p256Keys(t)

	require.Len(t, pubkey, 2+p256PubSize, "the varint 0x1200 is two bytes, the point 33")
	code, decoded, err := DecodePublicKey(pubkey)
	require.NoError(t, err)
	require.Equal(t, multicodec.P256Pub, code, "scheme is named in the encoding")
	ecPub, ok := decoded.(*ecdsa.PublicKey)
	require.True(t, ok, "decodes to an ECDSA public key")
	require.True(t, priv.PublicKey.Equal(ecPub), "the key survives the round-trip")
}

// TestDecodeP256RejectsBadPoint: a p256-pub framing of the right length whose bytes are
// no point on the curve is refused — a key nothing could ever verify under.
func TestDecodeP256RejectsBadPoint(t *testing.T) {
	_, pubkey := p256Keys(t)
	// An x above the field prime names no point at all, whichever parity claims it.
	offCurve := make([]byte, p256PubSize)
	for i := range offCurve {
		offCurve[i] = 0xff
	}
	offCurve[0] = 0x02

	_, _, err := DecodePublicKey(prependCode(multicodec.P256Pub, offCurve))
	require.Error(t, err, "a point off the curve is not a key")

	short := append([]byte(nil), pubkey[:len(pubkey)-1]...)
	_, _, err = DecodePublicKey(short)
	require.Error(t, err, "a compressed P-256 point is 33 bytes")
}

// TestDecodePublicKeyRejectsWrongLength: a well-framed ed25519 multikey
// with the wrong number of key bytes is rejected.
func TestDecodePublicKeyRejectsWrongLength(t *testing.T) {
	_, pubkey := ed25519Keys(t)
	truncated := pubkey[:len(pubkey)-1] // drop one key byte, keep the varint
	_, _, err := DecodePublicKey(truncated)
	require.Error(t, err)
}

// --- sign / verify ------------------------------------------------------

// TestSignVerifyRoundTrip: an envelope sealed under a key verifies against that key's
// pubkey.
func TestSignVerifyRoundTrip(t *testing.T) {
	priv, pubkey := ed25519Keys(t)

	env, err := signEnvelope(priv, []byte("the record bytes"))
	require.NoError(t, err)
	require.NoError(t, verifyEnvelope(pubkey, env), "an honest envelope must verify")
}

// TestSignVerifyP256RoundTrip: the second scheme `V-SIGN` names seals and verifies the
// same way, its envelope naming ES256 where Ed25519's names EdDSA.
func TestSignVerifyP256RoundTrip(t *testing.T) {
	priv, pubkey := p256Keys(t)

	env, err := signEnvelope(priv, []byte("the record bytes"))
	require.NoError(t, err)
	require.NoError(t, verifyEnvelope(pubkey, env), "an honest envelope must verify")

	msg, err := decodeEnvelope(env)
	require.NoError(t, err)
	alg, err := msg.Headers.Protected.Algorithm()
	require.NoError(t, err)
	require.Equal(t, cose.AlgorithmES256, alg, "a P-256 key signs under ES256")
}

// TestVerifyRejectsSchemeDisagreement: a claim names its scheme twice, and the two MUST
// agree (`V-SIGN`) — so an EdDSA envelope presented with a p256-pub key is refused, and
// an ES256 one presented with an ed25519-pub key likewise, before any curve math runs.
func TestVerifyRejectsSchemeDisagreement(t *testing.T) {
	edPriv, edPubkey := ed25519Keys(t)
	ecPriv, ecPubkey := p256Keys(t)

	edEnv, err := signEnvelope(edPriv, []byte("signed under EdDSA"))
	require.NoError(t, err)
	require.ErrorIs(t, verifyEnvelope(ecPubkey, edEnv), ErrEnvelopeScheme,
		"an EdDSA header does not answer for a key framed as p256-pub")

	ecEnv, err := signEnvelope(ecPriv, []byte("signed under ES256"))
	require.NoError(t, err)
	require.ErrorIs(t, verifyEnvelope(edPubkey, ecEnv), ErrEnvelopeScheme,
		"an ES256 header does not answer for a key framed as ed25519-pub")
}

// TestDecodeEnvelopeRejectsForeignAlgorithm: `V-SIGN` names two schemes, so an envelope
// under a third is refused as it is read, before a key is ever resolved.
func TestDecodeEnvelopeRejectsForeignAlgorithm(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	signer, err := cose.NewSigner(cose.AlgorithmES384, priv)
	require.NoError(t, err)

	msg := cose.NewSign1Message()
	msg.Payload = []byte("signed under a scheme the spec does not name")
	msg.Headers.Protected[cose.HeaderLabelAlgorithm] = cose.AlgorithmES384
	require.NoError(t, msg.Sign(rand.Reader, nil, signer))
	raw, err := msg.MarshalCBOR()
	require.NoError(t, err)

	_, err = decodeEnvelope(raw)
	require.ErrorIs(t, err, ErrEnvelopeScheme, "ES384 is not a scheme `V-SIGN` names")
}

// TestSignRejectsForeignScheme: a key outside the two is refused at sealing, so no
// envelope nothing can verify is ever produced.
func TestSignRejectsForeignScheme(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)

	_, err = signEnvelope(priv, []byte("x"))
	require.Error(t, err, "P-384 is not a curve `V-SIGN` names")
}

// TestVerifyRejectsTamperedPayload: the signature covers the payload, so swapping it
// inside an otherwise intact envelope is caught.
func TestVerifyRejectsTamperedPayload(t *testing.T) {
	priv, pubkey := ed25519Keys(t)

	env, err := signEnvelope(priv, []byte("original"))
	require.NoError(t, err)

	msg, err := decodeEnvelope(env)
	require.NoError(t, err)
	msg.Payload = []byte("modified")
	tampered, err := msg.MarshalCBOR()
	require.NoError(t, err)

	require.Error(t, verifyEnvelope(pubkey, tampered),
		"a signature over different bytes must not verify")
}

// TestVerifyRejectsWrongKey: a signature by key A does not verify against key B's
// pubkey — Bob cannot pass off a claim as Alice's (`V-SIG`).
func TestVerifyRejectsWrongKey(t *testing.T) {
	alicePriv, _ := ed25519Keys(t)
	_, bobPubkey := ed25519Keys(t)

	env, err := signEnvelope(alicePriv, []byte("attributed to alice"))
	require.NoError(t, err)
	require.Error(t, verifyEnvelope(bobPubkey, env),
		"an envelope must not verify against a different key")
}

// TestEnvelopeNeedsKeyAndPubkey: every claim is signed (`V-SIG`), so sealing without a
// key and verifying without a pubkey are both refused rather than waved through.
func TestEnvelopeNeedsKeyAndPubkey(t *testing.T) {
	priv, _ := ed25519Keys(t)

	_, err := signEnvelope(nil, []byte("unsigned claim"))
	require.ErrorIs(t, err, errEnvelopeNoKey, "a claim cannot be sealed without a key")

	env, err := signEnvelope(priv, []byte("a signed claim"))
	require.NoError(t, err)
	require.ErrorIs(t, verifyEnvelope(nil, env), errEnvelopeNoPubkey,
		"a contributor with no pubkey answers for nothing")
}

// TestVerifyRejectsForeignScheme: a pubkey framed under another multicodec is refused
// before any curve math runs (`V-SIGN`).
func TestVerifyRejectsForeignScheme(t *testing.T) {
	priv, _ := ed25519Keys(t)
	env, err := signEnvelope(priv, []byte("x"))
	require.NoError(t, err)

	foreign := prependCode(multicodec.Sha2_256, make([]byte, ed25519.PublicKeySize))
	require.Error(t, verifyEnvelope(foreign, env),
		"only the schemes `V-SIGN` names may answer for a signature")
}

// --- PEM key loading ----------------------------------------------------

// TestLoadEd25519PEM: the PEM loaders round-trip a key from disk —
// LoadEd25519PrivateKeyPEM (PKCS#8), LoadPrivateKey (also precomputes the
// multikey pubkey), and LoadEd25519PublicKeyPEM (SPKI).
func TestLoadEd25519PEM(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	privPath := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(privPath,
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))

	loaded, err := LoadEd25519PrivateKeyPEM(privPath)
	require.NoError(t, err)
	require.Equal(t, priv, loaded, "private key round-trips through PEM")

	kp, err := LoadPrivateKey(privPath)
	require.NoError(t, err)
	wantPub, err := EncodePublicKey(priv.Public())
	require.NoError(t, err)
	require.Equal(t, wantPub, kp.Pubkey, "LoadPrivateKey precomputes the multikey pubkey")

	pubDer, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	pubPath := filepath.Join(dir, "pub.pem")
	require.NoError(t, os.WriteFile(pubPath,
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDer}), 0o600))
	loadedPub, err := LoadEd25519PublicKeyPEM(pubPath)
	require.NoError(t, err)
	require.Equal(t, pub, loadedPub, "public key round-trips through PEM")
}

// TestParseKeypairP256PEM: a P-256 PKCS#8 PEM is a keypair too, framed as p256-pub,
// and it signs an envelope that verifies under the pubkey it precomputed.
func TestParseKeypairP256PEM(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	kp, err := ParseKeypair(pemBytes)
	require.NoError(t, err)
	code, _, err := DecodePublicKey(kp.Pubkey)
	require.NoError(t, err)
	require.Equal(t, multicodec.P256Pub, code, "the framing names the scheme the key is under")

	env, err := signEnvelope(kp.Private, []byte("sealed by the loaded key"))
	require.NoError(t, err)
	require.NoError(t, verifyEnvelope(kp.Pubkey, env))
}

// TestParsePublicKeyPEMBothSchemes: a public key arrives as a PEM under either scheme,
// and EncodePublicKey frames what comes back — which is how a contributor's pubkey is
// read from `openssl pkey -pubout`.
func TestParsePublicKeyPEMBothSchemes(t *testing.T) {
	dir := t.TempDir()
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		pub  crypto.PublicKey
		code multicodec.Code
	}{
		"ed25519": {edPub, multicodec.Ed25519Pub},
		"p256":    {ecPriv.Public(), multicodec.P256Pub},
	} {
		t.Run(name, func(t *testing.T) {
			der, err := x509.MarshalPKIXPublicKey(tc.pub)
			require.NoError(t, err)
			path := filepath.Join(dir, name+".pem")
			require.NoError(t, os.WriteFile(path,
				pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600))

			loaded, err := LoadPublicKeyPEM(path)
			require.NoError(t, err)
			framed, err := EncodePublicKey(loaded)
			require.NoError(t, err)
			code, _, err := DecodePublicKey(framed)
			require.NoError(t, err)
			require.Equal(t, tc.code, code, "the framing names the scheme the PEM held")
		})
	}
}

// TestParsePublicKeyPEMRejectsForeignScheme: a SPKI PEM outside the two schemes is
// refused at the loader.
func TestParsePublicKeyPEMRejectsForeignScheme(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(priv.Public())
	require.NoError(t, err)

	_, err = ParsePublicKeyPEM(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	require.Error(t, err, "P-384 is not a curve `V-SIGN` names")
}

// TestParsePrivateKeyPEMRejectsForeignScheme: a PKCS#8 PEM holding a key outside the
// two schemes is refused at the loader, not at the first signature.
func TestParsePrivateKeyPEMRejectsForeignScheme(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)

	_, err = ParseKeypair(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	require.Error(t, err, "P-384 is not a curve `V-SIGN` names")
}

// TestLoadEd25519PEMErrors: a missing file and a non-PEM file are rejected.
func TestLoadEd25519PEMErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := LoadEd25519PrivateKeyPEM(filepath.Join(dir, "nope.pem"))
	require.Error(t, err, "missing file")

	bad := filepath.Join(dir, "bad.pem")
	require.NoError(t, os.WriteFile(bad, []byte("not a pem block"), 0o600))
	_, err = LoadEd25519PrivateKeyPEM(bad)
	require.Error(t, err, "not a PEM block (private)")
	_, err = LoadEd25519PublicKeyPEM(bad)
	require.Error(t, err, "not a PEM block (public)")
	_, err = LoadPrivateKey(bad)
	require.Error(t, err, "LoadPrivateKey propagates the load error")
}

// TestEveryIdIsAMultihash: with the signature moved into the envelope, one framing
// serves every id — a claim's, an edge's, and a content address (`V-ID`, `V-HASH`).
func TestEveryIdIsAMultihash(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("a note")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)

	hash, err := HashContent([]byte("some bytes"))
	require.NoError(t, err)

	require.Equal(t, "sha2-256", c.ID().Algorithm(), "a claim id hashes its envelope")
	require.Equal(t, "sha2-256", hash.Algorithm(), "a content address hashes its bytes")
	for _, e := range c.Edges() {
		require.Equal(t, "sha2-256", e.ID().Algorithm(), "an edge id hashes its record")
	}
}
