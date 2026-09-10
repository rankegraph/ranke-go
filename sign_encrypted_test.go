package ranke

// An encrypted key used to fail as an ASN.1 error about tags, which names the wrong
// thing to fix. These hold the passphrase path and, above all, that an unencrypted key
// asks for nothing: a tool signing a hundred claims prompts once or never, never twice.

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/youmark/pkcs8"
)

const testPass = "hunter2"

// keyPEMs returns one Ed25519 key in both wrappings, plaintext and encrypted.
func keyPEMs(t *testing.T) (plain, encrypted []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	plain = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	encDER, err := pkcs8.MarshalPrivateKey(priv, []byte(testPass), nil)
	require.NoError(t, err)
	encrypted = pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: encDER})
	return plain, encrypted
}

// TestEncryptedKeyOpensWithItsPassphrase, and yields the same key as the plaintext
// wrapping of it — the encryption being how it rests, not what it is.
func TestEncryptedKeyOpensWithItsPassphrase(t *testing.T) {
	plain, encrypted := keyPEMs(t)

	want, err := ParseKeypair(plain)
	require.NoError(t, err)

	got, err := ParseKeypair(encrypted, WithPassphrase([]byte(testPass)))
	require.NoError(t, err)
	require.Equal(t, want.Pubkey, got.Pubkey, "one key, two wrappings")

	lazy, err := ParseKeypair(encrypted, WithPassphraseFrom(func() ([]byte, error) {
		return []byte(testPass), nil
	}))
	require.NoError(t, err)
	require.Equal(t, want.Pubkey, lazy.Pubkey)
}

// TestUnencryptedKeyAsksForNothing is the point of the lazy option: a tool configured
// with a passphrase source and handed a plaintext key must not prompt, read an
// environment or block. Asking for a passphrase no key needs is the failure.
func TestUnencryptedKeyAsksForNothing(t *testing.T) {
	plain, _ := keyPEMs(t)

	asked := 0
	_, err := ParseKeypair(plain, WithPassphraseFrom(func() ([]byte, error) {
		asked++
		return []byte(testPass), nil
	}))
	require.NoError(t, err)
	require.Zero(t, asked, "a plaintext key must not reach for a passphrase")

	require.False(t, IsEncryptedKey(plain))
}

// TestEncryptedKeyAsksExactlyOnce: the material is read at load, never at signing, so
// a hundred claims cost one passphrase.
func TestEncryptedKeyAsksExactlyOnce(t *testing.T) {
	_, encrypted := keyPEMs(t)

	asked := 0
	kp, err := ParseKeypair(encrypted, WithPassphraseFrom(func() ([]byte, error) {
		asked++
		return []byte(testPass), nil
	}))
	require.NoError(t, err)
	require.Equal(t, 1, asked)

	// The key is in hand now, so signing reaches for nothing further.
	for range 100 {
		_, err := kp.Private.Sign(rand.Reader, []byte("a claim's bytes"), crypto.Hash(0))
		require.NoError(t, err)
	}
	require.Equal(t, 1, asked, "signing must not re-read the passphrase")
}

// TestEncryptedKeyWithoutAPassphraseSaysSo, rather than failing as an ASN.1 error about
// tags — which is what the operator got before and could act on not at all.
func TestEncryptedKeyWithoutAPassphraseSaysSo(t *testing.T) {
	_, encrypted := keyPEMs(t)
	require.True(t, IsEncryptedKey(encrypted))

	_, err := ParseKeypair(encrypted)
	require.ErrorIs(t, err, ErrKeyEncrypted)
}

// TestWrongPassphraseFails, and a failure fetching one is reported rather than read as
// an absent passphrase.
func TestWrongPassphraseFails(t *testing.T) {
	_, encrypted := keyPEMs(t)

	_, err := ParseKeypair(encrypted, WithPassphrase([]byte("not it")))
	require.Error(t, err)

	sentinel := errors.New("no terminal to prompt on")
	_, err = ParseKeypair(encrypted, WithPassphraseFrom(func() ([]byte, error) {
		return nil, sentinel
	}))
	require.ErrorIs(t, err, sentinel)
	require.ErrorIs(t, err, ErrKeyEncrypted, "and says which stage wanted it")
}

// TestAnotherKeyFormatSaysWhichOne: ssh-keygen writes OPENSSH PRIVATE KEY, which no
// passphrase converts — a reader will meet it, so the error names the format.
func TestAnotherKeyFormatSaysWhichOne(t *testing.T) {
	openssh := pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: []byte("not really")})

	_, err := ParseKeypair(openssh)
	require.ErrorIs(t, err, ErrKeyFormat)
	require.False(t, IsEncryptedKey(openssh), "another format, not another wrapping")
}

// TestLegacyEncryptionIsRecognised: RFC 1423 marks encryption in a header rather than
// the block type, so a key from an older openssl still reports what it needs.
func TestLegacyEncryptionIsRecognised(t *testing.T) {
	legacy := pem.EncodeToMemory(&pem.Block{
		Type:    "PRIVATE KEY",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,0000"},
		Bytes:   []byte("ciphertext"),
	})
	require.True(t, IsEncryptedKey(legacy))

	_, err := ParseKeypair(legacy)
	require.ErrorIs(t, err, ErrKeyEncrypted)
}
