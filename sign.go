// package: ranke / sign
// type:    crypto
// job:     multikey pubkey framing (`V-SIGN`) plus Ed25519 key encoding and PEM loading
// limits:  signs and verifies nothing — a claim's signature lives in its envelope
// (-> envelope); supports only Ed25519 today
package ranke

import (
	"crypto"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"os"
	"reflect"
	"strconv"

	"github.com/multiformats/go-multicodec"
	"github.com/youmark/pkcs8"
)

// EncodePublicKey wraps a Go public key as a multikey, the framing `V-SIGN` fixes:
// <multicodec varint naming the scheme><raw key bytes>.
func EncodePublicKey(pub crypto.PublicKey) ([]byte, error) {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return prependCode(multicodec.Ed25519Pub, k), nil
	default:
		return nil, WithDetail(errEncodePubkey, reflect.TypeOf(pub).String())
	}
}

// DecodePublicKey parses a multikey into its scheme code and typed Go key.
func DecodePublicKey(b []byte) (multicodec.Code, crypto.PublicKey, error) {
	code, rest, err := readCode(b)
	if err != nil {
		return 0, nil, Wrap(errDecodePubkey, err)
	}
	switch code {
	case multicodec.Ed25519Pub:
		if len(rest) != ed25519.PublicKeySize {
			return code, nil, WithDetail(errDecodePubkey, "ed25519 pubkey has "+strconv.Itoa(len(rest))+" bytes, want "+strconv.Itoa(ed25519.PublicKeySize))
		}
		return code, ed25519.PublicKey(rest), nil
	default:
		return code, nil, WithDetail(errDecodePubkey, "unsupported multicodec "+code.String()+" (0x"+strconv.FormatUint(uint64(code), 16)+")")
	}
}

// Keypair pairs a private signing key with its multikey-encoded public key,
// the key material a contributor claim needs.
type Keypair struct {
	Private crypto.Signer
	Pubkey  []byte // multikey-encoded (see EncodePublicKey)
}

// KeyOption supplies what reading a key may need beyond its bytes.
type KeyOption func(*keyConfig)

type keyConfig struct{ passphrase func() ([]byte, error) }

// WithPassphrase decrypts an encrypted key with pass.
func WithPassphrase(pass []byte) KeyOption {
	return func(c *keyConfig) { c.passphrase = func() ([]byte, error) { return pass, nil } }
}

// WithPassphraseFrom fetches the passphrase only if the key proves encrypted, so an
// unencrypted one never prompts, never reads an environment and never blocks.
func WithPassphraseFrom(fn func() ([]byte, error)) KeyOption {
	return func(c *keyConfig) { c.passphrase = fn }
}

// IsEncryptedKey reports whether the PEM holds an encrypted private key, which is what
// lets a caller ask for a passphrase only when one is wanted.
func IsEncryptedKey(pemBytes []byte) bool {
	block, _ := pem.Decode(pemBytes)
	return block != nil && encryptedBlock(block)
}

// encryptedBlock reports the two wrappings a passphrase opens: PKCS#8 under PBES2, and
// RFC 1423's legacy header form.
func encryptedBlock(block *pem.Block) bool {
	_, legacy := block.Headers["Proc-Type"]
	return legacy || block.Type == "ENCRYPTED PRIVATE KEY"
}

// ParseKeypair reads an Ed25519 PKCS#8 PEM private key and pre-computes its
// multikey-encoded public key. Bytes rather than a path, a key arriving as readily
// from an environment variable, a pipe or a paste (-> keysource).
func ParseKeypair(pemBytes []byte, opts ...KeyOption) (Keypair, error) {
	priv, err := ParseEd25519PrivateKeyPEM(pemBytes, opts...)
	if err != nil {
		return Keypair{}, err
	}
	pubkey, err := EncodePublicKey(priv.Public())
	if err != nil {
		return Keypair{}, WrapDetail(errLoadKeypair, "encode pubkey", err)
	}
	return Keypair{Private: priv, Pubkey: pubkey}, nil
}

// LoadPrivateKey is ParseKeypair over the file at path.
func LoadPrivateKey(path string, opts ...KeyOption) (Keypair, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Keypair{}, WrapDetail(errLoadKeypair, "read "+path, err)
	}
	kp, err := ParseKeypair(b, opts...)
	if err != nil {
		return Keypair{}, WrapDetail(errLoadKeypair, path, err)
	}
	return kp, nil
}

// ParseEd25519PrivateKeyPEM reads an Ed25519 private key from a PKCS#8 PEM block
// (`openssl genpkey -algorithm ed25519`).
func ParseEd25519PrivateKeyPEM(pemBytes []byte, opts ...KeyOption) (ed25519.PrivateKey, error) {
	cfg := keyConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, WithDetail(errLoadPrivKey, "no PEM block found")
	}
	key, err := decodePKCS8(block, cfg)
	if err != nil {
		return nil, err
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, WithDetail(errLoadPrivKey, "not an Ed25519 key (got "+reflect.TypeOf(key).String()+")")
	}
	return ed, nil
}

// decodePKCS8 turns the block into a key, naming what a passphrase or a conversion
// would fix before parsing: both otherwise fail as an ASN.1 error about tags.
// "OPENSSH PRIVATE KEY" is what ssh-keygen writes, another format rather than another
// wrapping, so no passphrase opens it here.
func decodePKCS8(block *pem.Block, cfg keyConfig) (any, error) {
	switch {
	case encryptedBlock(block):
		if cfg.passphrase == nil {
			return nil, WithDetail(ErrKeyEncrypted, block.Type)
		}
		pass, err := cfg.passphrase()
		if err != nil {
			return nil, WrapDetail(ErrKeyEncrypted, "read the passphrase", err)
		}
		key, err := pkcs8.ParsePKCS8PrivateKey(block.Bytes, pass)
		if err != nil {
			return nil, WrapDetail(errLoadPrivKey, "decrypt PKCS#8", err)
		}
		return key, nil
	case block.Type != "PRIVATE KEY":
		return nil, WithDetail(ErrKeyFormat, block.Type)
	default:
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, WrapDetail(errLoadPrivKey, "parse PKCS#8", err)
		}
		return key, nil
	}
}

// LoadEd25519PrivateKeyPEM is ParseEd25519PrivateKeyPEM over the file at path.
func LoadEd25519PrivateKeyPEM(path string, opts ...KeyOption) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, WrapDetail(errLoadPrivKey, "read "+path, err)
	}
	ed, err := ParseEd25519PrivateKeyPEM(b, opts...)
	if err != nil {
		return nil, WrapDetail(errLoadPrivKey, path, err)
	}
	return ed, nil
}

// ParseEd25519PublicKeyPEM reads an Ed25519 public key from a SubjectPublicKeyInfo
// PEM block (`openssl pkey -pubout`).
func ParseEd25519PublicKeyPEM(pemBytes []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, WithDetail(errLoadPubKey, "no PEM block found")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, WrapDetail(errLoadPubKey, "parse SPKI", err)
	}
	ed, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, WithDetail(errLoadPubKey, "not an Ed25519 key (got "+reflect.TypeOf(key).String()+")")
	}
	return ed, nil
}

// LoadEd25519PublicKeyPEM is ParseEd25519PublicKeyPEM over the file at path.
func LoadEd25519PublicKeyPEM(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, WrapDetail(errLoadPubKey, "read "+path, err)
	}
	ed, err := ParseEd25519PublicKeyPEM(b)
	if err != nil {
		return nil, WrapDetail(errLoadPubKey, path, err)
	}
	return ed, nil
}

// --- multikey/multicodec varint helpers ---

// prependCode emits <varint code><payload>.
func prependCode(code multicodec.Code, payload []byte) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(code))
	out := make([]byte, 0, n+len(payload))
	out = append(out, buf[:n]...)
	out = append(out, payload...)
	return out
}

// splitCode reads the leading varint code and returns it with the payload.
func splitCode(b []byte) (multicodec.Code, []byte, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, nil, errInvalidVarint
	}
	return multicodec.Code(v), b[n:], nil
}

// readCode reads the leading code and returns the rest.
func readCode(b []byte) (multicodec.Code, []byte, error) {
	return splitCode(b)
}
