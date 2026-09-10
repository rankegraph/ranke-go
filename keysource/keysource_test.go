package keysource_test

// The refusals are the point of this package, so each is held by a case: a key others
// can read, material passed where a source belongs, and a prompt without the opt-in.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/keysource"
	"github.com/stretchr/testify/require"
)

// keyPEM is a fresh Ed25519 private key in the PKCS#8 PEM the library reads.
func keyPEM(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// keyFile writes material at mode, returning its path.
func keyFile(t *testing.T, body []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contributor.pem")
	require.NoError(t, os.WriteFile(path, body, mode))
	return path
}

// TestEverySpellingYieldsTheKey: the four sources are one grammar, so each answers with
// the same material and an app writes one call whichever a deployment chose.
func TestEverySpellingYieldsTheKey(t *testing.T) {
	body := keyPEM(t)
	path := keyFile(t, body, 0o600)

	t.Setenv("RANKE_TEST_SIGNING_KEY", string(body))

	for _, tc := range []struct {
		spec string
		in   string
		opts []keysource.Option
	}{
		{spec: path},
		{spec: "file:" + path},
		{spec: "env:RANKE_TEST_SIGNING_KEY"},
		{spec: "stdin", in: string(body)},
		{spec: "prompt", in: string(body), opts: []keysource.Option{keysource.WithTTY()}},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := keysource.Load(tc.spec, strings.NewReader(tc.in), tc.opts...)
			require.NoError(t, err)
			// What matters is that the material parses as the key it was, which is the
			// whole point of yielding bytes rather than a string.
			kp, err := ranke.ParseKeypair(got)
			require.NoError(t, err, "the source's bytes must be a keypair")
			require.NotEmpty(t, kp.Pubkey)
		})
	}
}

// TestRefusesAKeyOthersCanRead is ssh's rule, for ssh's reason: the material outlives
// the mistake, so a wide mode is refused rather than warned about.
func TestRefusesAKeyOthersCanRead(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o666} {
		t.Run(mode.String(), func(t *testing.T) {
			path := keyFile(t, keyPEM(t), mode)
			_, err := keysource.Load(path, nil)
			require.ErrorIs(t, err, keysource.ErrPermission)
		})
	}

	// And admits the modes only the owner can read.
	for _, mode := range []os.FileMode{0o600, 0o400} {
		t.Run(mode.String(), func(t *testing.T) {
			path := keyFile(t, keyPEM(t), mode)
			_, err := keysource.Load(path, nil)
			require.NoError(t, err)
		})
	}
}

// TestRefusesInlineMaterialAsCompromised: a key on a command line has reached the
// process table, the shell history and any CI log. The refusal says to rotate it, since
// fixing the flag leaves the key spent.
func TestRefusesInlineMaterialAsCompromised(t *testing.T) {
	body := keyPEM(t)
	for _, spec := range []string{
		string(body),                     // the whole PEM
		"-----BEGIN PRIVATE KEY-----",    // its opening alone
		"file:" + string(body),           // and dressed as a source
		"env:NAME\nMIGTAgEAMBUGByqGSM49", // a newline smuggling a second line
	} {
		_, err := keysource.Load(spec, nil)
		require.ErrorIs(t, err, keysource.ErrInline, "spec %.24q", spec)
	}

	require.Contains(t, keysource.ErrInline.Error(), "rotate",
		"the error must say the key is spent, not that the flag was wrong")
}

// TestPromptNeedsTheOptIn: a server must never block on a terminal read, which an opt-in
// makes impossible where a convention would only make it unusual.
func TestPromptNeedsTheOptIn(t *testing.T) {
	_, err := keysource.Load("prompt", strings.NewReader(string(keyPEM(t))))
	require.ErrorIs(t, err, keysource.ErrNoTTY)

	_, err = keysource.Parse("prompt")
	require.ErrorIs(t, err, keysource.ErrNoTTY, "and Parse refuses it before any read")
}

// TestParseTouchesNothing: an app validates its argument at startup and reads the
// material where it signs, so a rotating secret is fetched when it is needed.
func TestParseTouchesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-written-yet.pem")

	s, err := keysource.Parse(path)
	require.NoError(t, err, "the spec is well formed whether or not the file exists")
	require.Equal(t, keysource.KindFile, s.Kind)
	require.Equal(t, path, s.Name)

	_, err = s.Read(nil)
	require.Error(t, err, "and the read is where the absence shows")

	require.NoError(t, os.WriteFile(path, keyPEM(t), 0o600))
	got, err := s.Read(nil)
	require.NoError(t, err, "the same spec answers once the material is there")
	require.NotEmpty(t, got)
}

// TestRejectsWhatItCannotServe: an unrecognised spelling fails at Parse, where a caller
// can still say which argument was wrong. A scheme-shaped spec is refused rather than
// read as a filename, which would name the wrong mistake — "no such file: evn:KEY"
// hides a typo that "unrecognised key source" shows.
func TestRejectsWhatItCannotServe(t *testing.T) {
	for _, spec := range []string{"", "env:", "file:", "vault:secret/key", "evn:RANKE_KEY"} {
		_, err := keysource.Parse(spec)
		require.ErrorIsf(t, err, keysource.ErrSpec, "spec %q", spec)
	}

	// A path carrying a colon says so with file:, which is what the prefix is for.
	s, err := keysource.Parse("file:./odd:name.pem")
	require.NoError(t, err)
	require.Equal(t, "./odd:name.pem", s.Name)

	// And a colon after a path separator is a filename, not a scheme.
	s, err = keysource.Parse("./keys/odd:name.pem")
	require.NoError(t, err)
	require.Equal(t, "./keys/odd:name.pem", s.Name)
}

// TestEmptySourceIsRefused: an unset variable and an empty pipe are both "no key", and
// either would otherwise reach the parser as a confusing PEM error.
func TestEmptySourceIsRefused(t *testing.T) {
	_, err := keysource.Load("stdin", strings.NewReader("   \n"))
	require.ErrorIs(t, err, keysource.ErrEmpty)

	t.Setenv("RANKE_TEST_EMPTY_KEY", "")
	_, err = keysource.Load("env:RANKE_TEST_EMPTY_KEY", nil)
	require.ErrorIs(t, err, keysource.ErrEmpty)

	_, err = keysource.Load("env:RANKE_TEST_UNSET_KEY", nil)
	require.Error(t, err, "an unset variable is named rather than read as empty")
}
