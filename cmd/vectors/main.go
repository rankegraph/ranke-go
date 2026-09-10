// package: cmd/vectors / cli
// type:    cmd
// job:     generates the cross-implementation reference artifacts — a conformance graph of claim
// records, the archive's bookmark list, plus records that must be rejected, described by a manifest
// limits:  renders bytes only; the artifacts are the spec's, and which of them are correct is
// settled against the paper, not against this program
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/internal/vectors"
)

// epoch fixes every timestamp, so a regenerated set is byte-identical.
var epoch = time.Unix(1700000000, 0).UTC()

// generatorPath names this tool in the manifest.
const generatorPath = "github.com/rankegraph/ranke-go/cmd/vectors"

func main() {
	out := flag.String("out", "", "directory to write the artifacts into (required)")
	flag.Parse()
	if *out == "" {
		flag.Usage()
		os.Exit(2)
	}

	p := vectors.Provenance{
		Generator:   generatorPath,
		Version:     generatorVersion(),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := run(context.Background(), *out, p); err != nil {
		fmt.Fprintln(os.Stderr, "vectors:", err)
		os.Exit(1)
	}
}

// generatorVersion reads the module version this ran as, so the manifest names a
// release rather than a working copy.
func generatorVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" {
		return "unknown"
	}
	return bi.Main.Version
}

// run builds the artifacts and writes them under dir.
func run(ctx context.Context, dir string, p vectors.Provenance) error {
	g := &gen{dir: dir, prov: p, ids: map[string]ranke.Id{}, raw: map[string][]byte{}}
	for _, sub := range []string{"claims", "content", "bookmarks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	if err := g.conformanceGraph(ctx); err != nil {
		return fmt.Errorf("conformance graph: %w", err)
	}
	if err := g.bookmarkCases(); err != nil {
		return fmt.Errorf("bookmarks: %w", err)
	}
	if err := g.broken(ctx); err != nil {
		return fmt.Errorf("broken records: %w", err)
	}
	if err := g.malformed(); err != nil {
		return fmt.Errorf("malformed records: %w", err)
	}
	return g.writeManifest()
}

// gen accumulates artifacts and the cases describing them.
type gen struct {
	dir       string
	prov      vectors.Provenance
	claims    []vectors.ClaimCase
	content   []vectors.ContentCase
	bookmarks []vectors.BookmarkCase
	ids       map[string]ranke.Id
	raw       map[string][]byte
	who       ranke.Contributor // the root identity, which the rejected cases reuse
}

// writeManifest renders the manifest last, when every case is known.
func (g *gen) writeManifest() error {
	m := vectors.Manifest{
		Note: "Reference artifacts for the Ranke-Graph ADT. A claim record is {1: S(node)}; " +
			"its id is not in the record, so each case pairs bytes with the id they are offered " +
			"under. Verification hashes the record as received, never a re-encode. A bookmark " +
			"record is a COSE_Sign1 over S([i, s, k]), paired with the id_seq(i, s) slot it is " +
			"offered at.",
		Provenance: g.prov,
		Claims:     g.claims,
		Content:    g.content,
		Bookmarks:  g.bookmarks,
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(g.dir, vectors.Name), append(b, '\n'), 0o644)
}

// addClaim writes a claim that must verify, remembering it for the broken cases.
func (g *gen) addClaim(name string, c ranke.Claim, why string) error {
	raw, err := c.Envelope()
	if err != nil {
		return err
	}
	file := filepath.Join("claims", name+".cbor")
	if err := os.WriteFile(filepath.Join(g.dir, file), raw, 0o644); err != nil {
		return err
	}
	g.ids[name], g.raw[name] = c.ID(), raw
	g.claims = append(g.claims, vectors.ClaimCase{
		File: file, Id: c.ID().String(), Verify: true, Reason: vectors.ReasonOK, Why: why,
	})
	return nil
}

// addBroken writes a record that must be rejected under the id it names, naming the
// rules it breaks so the coverage gate can count them.
func (g *gen) addBroken(name string, raw []byte, id, reason, why string, violates ...string) error {
	file := filepath.Join("claims", name+".cbor")
	if err := os.WriteFile(filepath.Join(g.dir, file), raw, 0o644); err != nil {
		return err
	}
	g.claims = append(g.claims, vectors.ClaimCase{
		File: file, Id: id, Verify: false, Reason: reason, Why: why, Violates: violates,
	})
	return nil
}

// addBookmark writes a bookmark record under the id_seq(index, seed) slot it is
// offered at, filling in the case's file and slot around what the caller stated.
func (g *gen) addBookmark(name string, raw []byte, index uint64, seed []byte, c vectors.BookmarkCase) error {
	slot, err := ranke.IdSeq(index, seed)
	if err != nil {
		return err
	}
	c.File = filepath.Join("bookmarks", name+".cbor")
	c.Slot = slot.String()
	if err := os.WriteFile(filepath.Join(g.dir, c.File), raw, 0o644); err != nil {
		return err
	}
	g.bookmarks = append(g.bookmarks, c)
	return nil
}

// addContent writes a blob under the hash it is offered as.
func (g *gen) addContent(name string, blob []byte, hash ranke.Id, ok bool, reason, why string, violates ...string) error {
	file := filepath.Join("content", name+".bin")
	if err := os.WriteFile(filepath.Join(g.dir, file), blob, 0o644); err != nil {
		return err
	}
	g.content = append(g.content, vectors.ContentCase{
		File: file, Hash: hash.String(), Verify: ok, Reason: reason, Why: why, Violates: violates,
	})
	return nil
}

// signer derives a fixed Ed25519 key from seed, so the artifacts reproduce.
func signer(seed string) ed25519.PrivateKey {
	h := sha256.Sum256([]byte(seed))
	return ed25519.NewKeyFromSeed(h[:])
}

// contributorClaim builds a root contributor: an initial claim whose content is its
// own multikey pubkey (§5.7), signed by the key it publishes.
func contributorClaim(ctx context.Context, priv ed25519.PrivateKey, at time.Time) (ranke.Claim, ranke.Contributor, error) {
	pub, err := ranke.EncodePublicKey(priv.Public())
	if err != nil {
		return nil, nil, err
	}
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pub).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(at).
		Sign(priv)
	if err != nil {
		return nil, nil, err
	}
	who, err := c.AsContributor(ctx, nil, priv)
	if err != nil {
		return nil, nil, err
	}
	return c, who, nil
}
