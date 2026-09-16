// package: ranke / codec_envelope
// type:    crypto
// job:     the claim envelope (`V-ENV`) — a COSE_Sign1 over S(v), which the Universe stores
// under id(v) = H(S(env(v)))
// limits:  the record it wraps is codec.go's, and this says nothing about its shape;
// resolves no contributor key (-> verify)
package ranke

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"reflect"
	"strconv"

	cose "github.com/veraison/go-cose"
)

// The payload rides as bytes, so verification reads what is stored: re-encoding it
// would shift as the alias taxonomy grows.

// signEnvelope returns S(env(v)): payload signed under signingKey, as a tagged
// COSE_Sign1. The id is the hash of these bytes (`V-ID`).
func signEnvelope(signingKey crypto.Signer, payload []byte) ([]byte, error) {
	return signCOSE(signingKey, payload, nil)
}

// signCOSE returns payload as a tagged COSE_Sign1 under signingKey, its protected header
// carrying alg plus whatever extra names — nothing for a claim (`V-ENV`), the kid for a
// bookmark (`V-BMENV`). One signer for both, so the key checks are stated once.
func signCOSE(signingKey crypto.Signer, payload []byte, extra cose.ProtectedHeader) ([]byte, error) {
	if signingKey == nil {
		return nil, errEnvelopeNoKey
	}
	alg, err := signatureAlgorithm(signingKey.Public())
	if err != nil {
		return nil, WrapDetail(errSignEnvelope, "signer", err)
	}
	signer, err := cose.NewSigner(alg, signingKey)
	if err != nil {
		return nil, WrapDetail(errSignEnvelope, "signer", err)
	}
	msg := cose.NewSign1Message()
	msg.Payload = payload
	msg.Headers.Protected[cose.HeaderLabelAlgorithm] = alg
	for label, value := range extra {
		msg.Headers.Protected[label] = value
	}
	if err := msg.Sign(nil, nil, signer); err != nil {
		return nil, WrapDetail(errSignEnvelope, "sign", err)
	}
	raw, err := msg.MarshalCBOR()
	if err != nil {
		return nil, WrapDetail(errSignEnvelope, "marshal", err)
	}
	return raw, nil
}

// envelopePayload returns the S(v) an envelope carries. Bytes of another shape fail
// here, which is how content is told from a claim.
func envelopePayload(raw []byte) ([]byte, error) {
	msg, err := decodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if len(msg.Payload) == 0 {
		return nil, Wrap(errDecodeEnvelope, errEnvelopeNoPayload)
	}
	return msg.Payload, nil
}

// verifyEnvelope checks the signature against a multikey pubkey (`V-SIG`). It covers
// the stored payload, so authorship holds for the bytes filed under the id.
func verifyEnvelope(pubkey, raw []byte) error {
	msg, err := decodeEnvelope(raw)
	if err != nil {
		return err
	}
	return verifySign1(pubkey, msg)
}

// verifySign1 checks msg's signature against a multikey pubkey (`V-SIGN`), whatever
// shape of record the message carries. A claim names its scheme twice — in the
// protected header and in the key's framing — and the two must agree, so a signature
// made under one scheme cannot be presented as the other's.
func verifySign1(pubkey []byte, msg *cose.Sign1Message) error {
	if len(pubkey) == 0 {
		return errEnvelopeNoPubkey
	}
	_, pub, err := DecodePublicKey(pubkey)
	if err != nil {
		return WrapDetail(errVerifyEnvelope, "decode pubkey", err)
	}
	keyAlg, err := signatureAlgorithm(pub)
	if err != nil {
		return WrapDetail(errVerifyEnvelope, "pubkey", err)
	}
	headerAlg, err := msg.Headers.Protected.Algorithm()
	if err != nil {
		return WrapDetail(errVerifyEnvelope, "header algorithm", err)
	}
	if headerAlg != keyAlg {
		return WithDetail(ErrEnvelopeScheme, "header names "+headerAlg.String()+", the pubkey is framed for "+keyAlg.String())
	}
	verifier, err := cose.NewVerifier(keyAlg, pub)
	if err != nil {
		return WrapDetail(errVerifyEnvelope, "verifier", err)
	}
	if err := msg.Verify(nil, verifier); err != nil {
		return WrapDetail(errVerifyEnvelope, "verify", err)
	}
	return nil
}

// signatureAlgorithm is the COSE algorithm a key signs and verifies under, the pairing
// `V-SIGN` fixes: Ed25519 signs as `EdDSA`, P-256 as `ES256`.
func signatureAlgorithm(pub crypto.PublicKey) (cose.Algorithm, error) {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return cose.AlgorithmEd25519, nil
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return 0, WithDetail(ErrEnvelopeScheme, "ECDSA over "+k.Curve.Params().Name+", and `V-SIGN` names P-256")
		}
		return cose.AlgorithmES256, nil
	default:
		return 0, WithDetail(ErrEnvelopeScheme, "public key type "+reflect.TypeOf(pub).String())
	}
}

// decodeEnvelope parses the stored bytes as a COSE_Sign1, and holds the headers to alg
// alone (`V-ENV`) — the id hashes these bytes, so a spare header would give one claim a
// second stored form and a second id, both verifying.
func decodeEnvelope(raw []byte) (*cose.Sign1Message, error) {
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(raw); err != nil {
		return nil, Wrap(errDecodeEnvelope, err)
	}
	if len(msg.Headers.Unprotected) != 0 {
		return nil, WithDetail(ErrEnvelopeHeaders, "unprotected header carries "+
			strconv.Itoa(len(msg.Headers.Unprotected))+" parameter(s), want none")
	}
	alg, err := msg.Headers.Protected.Algorithm()
	if err != nil {
		return nil, WrapDetail(ErrEnvelopeHeaders, "protected header algorithm", err)
	}
	if alg != cose.AlgorithmEd25519 && alg != cose.AlgorithmES256 {
		return nil, WithDetail(ErrEnvelopeScheme, "header names "+alg.String())
	}
	if len(msg.Headers.Protected) != 1 {
		return nil, WithDetail(ErrEnvelopeHeaders, "protected header carries "+
			strconv.Itoa(len(msg.Headers.Protected))+" parameters, want alg alone")
	}
	return &msg, nil
}
