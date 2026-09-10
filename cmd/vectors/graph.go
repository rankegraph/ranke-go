// package: cmd/vectors / graph
// type:    cmd
// job:     the conformance graph every implementation must verify — one claim per ADT shape, small
// enough that a reader can check each record against §4.1 by eye
// limits:  valid records only; the rejected ones derive from these (-> broken.go)
package main

import (
	"context"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/internal/vectors"
)

// externalBlob is the content the external-content claim addresses.
var externalBlob = []byte("externalized content, addressed by hash")

// rootSeed derives the identity that signs most of the set, patched records included.
const rootSeed = "ranke-vectors/root"

// conformanceGraph builds the claims that must verify: a contributor, a source note, a
// derived claim citing it, external content, node fields, a dated claim, the
// archive's empty branch table and one revision of it, and a second contributor.
func (g *gen) conformanceGraph(ctx context.Context) error {
	root, who, err := contributorClaim(ctx, signer(rootSeed), epoch)
	if err != nil {
		return err
	}
	g.who = who
	if err := g.addClaim("root-contributor", root,
		"initial claim (height 0), content is its own multikey pubkey, signed by that key (§5.7)"); err != nil {
		return err
	}

	note, err := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte("a source note")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(time.Second)).
		Sign()
	if err != nil {
		return err
	}
	if err := g.addClaim("source-note", note,
		"inline content, one auto contributor edge, height 1"); err != nil {
		return err
	}

	if err := g.derived(who, note); err != nil {
		return err
	}
	if err := g.external(who); err != nil {
		return err
	}
	if err := g.withFields(who); err != nil {
		return err
	}
	if err := g.dated(who); err != nil {
		return err
	}
	table, err := g.branchTable(root, who)
	if err != nil {
		return err
	}
	if err := g.tableRevision(table, who); err != nil {
		return err
	}
	return g.secondContributor(ctx)
}

// derived adds a claim citing the note through a derivation edge, which exercises
// height = 1 + max(refs). Citing a source is a practice, not a requirement, so the
// edge's presence is what this case shows and nothing rests on it.
func (g *gen) derived(who ranke.Contributor, note ranke.Claim) error {
	e, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: note.ID(),
		Type:      ranke.TypeDerivation("note"),
	})
	if err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.TypeDerivation("note"), who).
		WithInlineContent([]byte("derived from the source note")).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(e).
		WithHeight(2).
		WithCreatedAt(epoch.Add(2 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	return g.addClaim("derived-note", c,
		"derivation edge to source-note, height 2 = 1 + max(referenced heights)")
}

// external adds a claim whose content lives outside the record, plus the blob.
func (g *gen) external(who ranke.Contributor) error {
	hash, err := ranke.HashContent(externalBlob)
	if err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.TypeSource("blob"), who).
		WithExternalContent(hash, uint64(len(externalBlob))).
		WithEncoding(ranke.EncodingOctetStream).
		WithHeight(1).
		WithCreatedAt(epoch.Add(3 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	if err := g.addClaim("external-content", c,
		"content_hash + content_size in the record, bytes alongside (§Content)"); err != nil {
		return err
	}
	return g.addContent("external-blob", externalBlob, hash, true, vectors.ReasonOK,
		"the bytes external-content addresses; hashes to the content_hash it declares")
}

// withFields adds a claim carrying node fields, whose keys the encoder aliases.
func (g *gen) withFields(who ranke.Contributor) error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte("a note with fields")).
		WithEncoding(ranke.EncodingPlain).
		WithField("title", "reference vector").
		WithField("lang", "en").
		WithHeight(1).
		WithCreatedAt(epoch.Add(4 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	return g.addClaim("with-fields", c,
		"node fields under tag 8, key-sorted by the encoder; known keys carry a '.' alias")
}

// dated adds a claim carrying `dated` (`V-DATED`) as EDTF's own date-and-time
// form — a numeric offset and no fractional seconds, the shape a bare RFC 3339
// timestamp cannot take — so a decoder is exercised on more than a bare day.
func (g *gen) dated(who ranke.Contributor) error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte("a note dated by EDTF date-time")).
		WithEncoding(ranke.EncodingPlain).
		WithDatedEDTF("2014-06-15T09:30:00+02:00").
		WithHeight(1).
		WithCreatedAt(epoch.Add(7 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	return g.addClaim("dated", c,
		"dated as EDTF's date-and-time form (V-DATED) — a numeric offset, no fractional seconds")
}

// branchTable adds the empty contribution/branches claim a new archive is created
// with: its id is the archive's head k, and the initial claim is its only
// reference, so it stands at height 1 (`V-ARCHIVEHEIGHT`, §Ranke-Archive).
func (g *gen) branchTable(root ranke.Claim, who ranke.Contributor) (ranke.Claim, error) {
	c, err := ranke.NewClaim(ranke.NodeBranches, who).
		WithHeight(ranke.HeightOf(root)).
		WithCreatedAt(epoch.Add(8 * time.Second)).
		Sign()
	if err != nil {
		return nil, err
	}
	return c, g.addClaim("branch-table", c,
		"the empty initial branch table, this archive's head k (§Ranke-Archive)")
}

// tableRevision adds the archive's second head k₁: a branch table restating the whole
// table rather than diffing over it, so it carries a contribution/branches edge to its
// predecessor and is a base claim (`R-C6MERGE`). The bookmark list records both heads.
func (g *gen) tableRevision(prev ranke.Claim, who ranke.Contributor) error {
	e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: prev.ID(), Type: ranke.EdgeTypeBranches})
	if err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.NodeBranches, who).
		WithEdges(e).
		WithHeight(ranke.HeightOf(prev)).
		WithCreatedAt(epoch.Add(9 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	return g.addClaim("branch-table-revision", c,
		"the archive's second head k₁, restating the table over its predecessor (§Branches)")
}

// secondContributor adds a second identity and a claim of its own, so a case can
// offer one contributor's record under another's signature.
func (g *gen) secondContributor(ctx context.Context) error {
	other, who, err := contributorClaim(ctx, signer("ranke-vectors/other"), epoch.Add(5*time.Second))
	if err != nil {
		return err
	}
	if err := g.addClaim("other-contributor", other,
		"a second identity, so a record can be offered under the wrong signer's id"); err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte("a note by the other contributor")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(6 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	return g.addClaim("other-note", c, "attributed to other-contributor")
}
