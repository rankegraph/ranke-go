// package: tests/generator / testkit
// type:    tool
// job:     deterministic "kitchen-sink" Ranke-Graph generator for integration + conformance testing
// — one size knob, every corner baked in
// limits:  builds via the public ranke API; the caller supplies the Universe to store into
// (-> adapter); does not assert (-> tests)
package generator

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/rankegraph/ranke-go"
)

// Deterministic within the Go reference implementation: a given (seed, size)
// yields the same graph on every run and backend, so perf hashes stay stable.

// --- deterministic keys -------------------------------------------------

// keySeed derives contributor #index's 32-byte Ed25519 seed from the master
// seed: SHA-256( little-endian uint64(master) ‖ uvarint(index) ).
func keySeed(master int64, index int) [sha256.Size]byte {
	var buf [8 + binary.MaxVarintLen64]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(master))
	n := binary.PutUvarint(buf[8:], uint64(index))
	return sha256.Sum256(buf[:8+n])
}

// keyForIndex derives contributor #index's deterministic Ed25519 private key.
func keyForIndex(master int64, index int) ed25519.PrivateKey {
	s := keySeed(master, index)
	return ed25519.NewKeyFromSeed(s[:])
}

// --- deterministic content ---------------------------------------------

// fill returns n bytes derived from (master, tag, index): SHA-256 in counter
// mode over master ‖ tag ‖ index ‖ counter.
func fill(master int64, tag string, index, n int) []byte {
	if n <= 0 {
		return nil
	}
	out := make([]byte, 0, n)
	var hdr [8 + binary.MaxVarintLen64]byte
	binary.LittleEndian.PutUint64(hdr[:8], uint64(master))
	m := binary.PutUvarint(hdr[8:], uint64(index))
	prefix := append(append([]byte{}, hdr[:8+m]...), tag...)
	for ctr := uint64(0); len(out) < n; ctr++ {
		var cb [binary.MaxVarintLen64]byte
		c := binary.PutUvarint(cb[:], ctr)
		block := sha256.Sum256(append(append([]byte{}, prefix...), cb[:c]...))
		out = append(out, block[:]...)
	}
	return out[:n]
}

// --- deterministic clock ------------------------------------------------

// Clock hands out increasing timestamps from a fixed base, so created_at (which
// feeds the id) is reproducible and satisfies the §Claims monotonicity rule.
// One Clock is the time source for the generator and Sequencer alike.
type Clock struct {
	t    time.Time
	step time.Duration
}

// NewClock returns a Clock at base advancing by step per tick.
func NewClock(base time.Time, step time.Duration) *Clock { return &Clock{t: base, step: step} }

// Now returns the current time without advancing.
func (c *Clock) Now() time.Time { return c.t }

// Tick returns the current time and advances the clock by one step.
func (c *Clock) Tick() time.Time {
	t := c.t
	c.t = c.t.Add(c.step)
	return t
}

// --- deterministic contributors ----------------------------------------

// contributor builds a signed root contributor for index: its multikey pubkey is
// its content (§5.7). The returned Contributor carries the key, so claims sign.
func contributor(ctx context.Context, master int64, index int, at time.Time) (ranke.Contributor, error) {
	priv := keyForIndex(master, index)
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	if err != nil {
		return nil, err
	}
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream). // a multikey pubkey is raw bytes
		WithField(ranke.FieldName, contributorName(master, index)).
		WithCreatedAt(at).
		Sign(priv)
	if err != nil {
		return nil, err
	}
	// Inline pubkey → no Universe needed to resolve it.
	return c.AsContributor(ctx, nil, priv)
}

// contributorName picks contributor #index's name, gofakeit seeded from keySeed.
func contributorName(master int64, index int) string {
	s := keySeed(master, index)
	f := gofakeit.New(binary.LittleEndian.Uint64(s[:8]))
	return f.Name()
}

// contentSeed derives a gofakeit content seed from (master, tag, index), by the
// same construction as fill and keySeed.
func contentSeed(master int64, tag string, index int) uint64 {
	var hdr [8 + binary.MaxVarintLen64]byte
	binary.LittleEndian.PutUint64(hdr[:8], uint64(master))
	m := binary.PutUvarint(hdr[8:], uint64(index))
	b := append(append([]byte{}, hdr[:8+m]...), tag...)
	s := sha256.Sum256(b)
	return binary.LittleEndian.Uint64(s[:8])
}

// textFor returns legible text/plain for a claim's content: content is knowledge
// (§Everything Is Knowledge), so it reads as a source note or derivation summary.
func textFor(master int64, tag string, index int) string {
	return gofakeit.New(contentSeed(master, tag, index)).Sentence(12)
}

// tinyFor returns exactly n bytes of deterministic legible text.
func tinyFor(master int64, tag string, index, n int) string {
	return gofakeit.New(contentSeed(master, tag, index)).LetterN(uint(n))
}

// nameFor returns a deterministic person name for an entity/person's content.
func nameFor(master int64, index int) string {
	return gofakeit.New(contentSeed(master, "person", index)).Name()
}

// datedForms cycles through the EDTF Level 1 corners `V-DATED` admits: a plain date,
// an interval, an unspecified-digit year, a qualified value, and an instant carrying a
// time of day — the one corner whose span is narrower than a day.
var datedForms = []string{"2014-06-11", "2014/2016", "201X", "2014?", "2014-06-11T09:30:00+02:00?"}

// datedFor returns a deterministic `dated` value for a claim, one of datedForms.
func datedFor(index int) string {
	return datedForms[index%len(datedForms)]
}

// --- deterministic branches --------------------------------------------

// branchCount is how many branches a graph of claimCount claims carries: at
// least 2 (one branch is degenerate), growing ≈2 at 100 claims to ≈10 at 100k.
func branchCount(claimCount int) int {
	if claimCount < 1 {
		claimCount = 1
	}
	c := int(2.5 * (math.Log10(float64(claimCount)) - 1))
	if c < 2 {
		return 2
	}
	return c
}

// branchNames returns stable names for claimCount claims: "main" plus gofakeit words.
func branchNames(claimCount int, seed int64, want int) []string {
	if want < 1 {
		want = branchCount(claimCount)
	}
	names := []string{"main"}
	f := gofakeit.New(contentSeed(seed, "branch", 0))
	seen := map[string]bool{"main": true}
	for len(names) < want {
		// The word list carries hyphens and apostrophes, which no branch name may
		// hold, so keep only what ValidateBranchName admits.
		w := branchNameSafe(f.Word())
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		names = append(names, w)
	}
	return names
}

// branchNameSafe reduces a generated word to the charset a branch name takes.
func branchNameSafe(word string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(word) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
