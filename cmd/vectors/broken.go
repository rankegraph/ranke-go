// package: cmd/vectors / broken
// type:    cmd
// job:     the records every implementation must reject, each isolating one failure — a wrong id, a
// record stored bare where an envelope belongs, an unresolvable contributor, a declared height that
// is not the derived one, content that misses its hash
// limits:  derives from the conformance graph (-> graph.go); each case breaks one thing, so a
// rejection names a cause
package main

import (
	"context"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/internal/vectors"
	"github.com/veraison/go-cose"
)

// broken writes the rejected cases, each pairing bytes with an id that does not
// hold for them, or a record whose closure cannot answer for it.
func (g *gen) broken(ctx context.Context) error {
	note, noteID := g.raw["source-note"], g.ids["source-note"]

	if err := g.addBroken("rejected-wrong-message", note, g.ids["derived-note"].String(),
		vectors.ReasonWrongMessage,
		"source-note's record under derived-note's id: a real signature over a different record",
		"V-ID"); err != nil {
		return err
	}
	if err := g.addBroken("rejected-wrong-signer", g.raw["other-note"], g.ids["source-note"].String(),
		vectors.ReasonWrongMessage,
		"other-note's record under an id signed by the root key, which never signed it",
		"V-ID"); err != nil {
		return err
	}
	if err := g.unenveloped(note); err != nil {
		return err
	}
	if err := g.malformedID(note, noteID); err != nil {
		return err
	}
	if err := g.tamperedContent(noteID); err != nil {
		return err
	}
	if err := g.wrongHeight(); err != nil {
		return err
	}
	if err := g.misorderedEdges(noteID); err != nil {
		return err
	}
	if err := g.unresolvableContributor(ctx); err != nil {
		return err
	}
	if err := g.firstTableHeight(); err != nil {
		return err
	}
	return g.tamperedBlob()
}

// firstTableHeight declares height 2 on an initial branch table, which stands on its
// contributor edge alone and so on height 1. One record settles it: the rule needs no
// seed, no bookmark and no walk to say what that height must be. The derived height
// disagrees too, so the record breaks `V-HEIGHT` alongside it.
func (g *gen) firstTableHeight() error {
	c, err := ranke.NewClaim(ranke.NodeBranches, g.who).
		WithHeight(2).
		WithCreatedAt(epoch.Add(16 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.Envelope()
	if err != nil {
		return err
	}
	return g.addBroken("rejected-first-table-height", raw, c.ID().String(), vectors.ReasonFirstTableHeight,
		"an initial branch table declaring height 2, where its lone contributor edge fixes 1",
		"V-ARCHIVEHEIGHT", "V-HEIGHT")
}

// unenveloped offers the serialized claim on its own, under the hash of those very
// bytes. The id holds for what is stored, so `V-ID` passes and the envelope is the
// whole defect: no signature travels with the record, and nothing attests it.
func (g *gen) unenveloped(raw []byte) error {
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(raw); err != nil {
		return err
	}
	hash, err := ranke.HashContent(msg.Payload)
	if err != nil {
		return err
	}
	return g.addBroken("rejected-not-enveloped", msg.Payload, hash.String(), vectors.ReasonNotEnveloped,
		"the serialized claim stored bare, with no envelope to carry a signature over it",
		"V-ENV")
}

// misorderedEdges reverses a two-edge claim's edges array and seals the result, so
// the id holds and the signature verifies. Edge order is then the whole defect, and
// it is the one that decides whether two implementations agree on any id at all.
func (g *gen) misorderedEdges(noteID ranke.Id) error {
	e, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: noteID,
		Type:      ranke.TypeDerivation("note"),
	})
	if err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.TypeDerivation("note"), g.who).
		WithInlineContent([]byte("a derivation whose edges are stored out of order")).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(e).
		WithHeight(2).
		WithCreatedAt(epoch.Add(12 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, id, err := patchedClaim(c, reverseEdges)
	if err != nil {
		return err
	}
	return g.addBroken("rejected-edge-order", raw, id.String(), vectors.ReasonEdgeOrder,
		"a claim's edges stored descending by id(e), where the rule fixes ascending",
		"V-EORDER")
}

// malformedID truncates the id's multibase payload, so it fails before any hashing.
func (g *gen) malformedID(raw []byte, id ranke.Id) error {
	s := id.String()
	return g.addBroken("rejected-malformed-id", raw, s[:len(s)-6], vectors.ReasonMalformedID,
		"the id's payload is truncated, so its multihash framing no longer parses",
		"V-HASH")
}

// tamperedContent restates the note with different content under the original id,
// the shape a modified archive takes.
func (g *gen) tamperedContent(id ranke.Id) error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), g.who).
		WithInlineContent([]byte("a tampered note")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.Envelope()
	if err != nil {
		return err
	}
	return g.addBroken("rejected-tampered-content", raw, id.String(), vectors.ReasonIDMismatch,
		"source-note with its content changed, still offered under source-note's id",
		"V-ID")
}

// wrongHeight declares a height the edge set does not derive, which the verifier
// re-derives and enforces even though the signature holds.
func (g *gen) wrongHeight() error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), g.who).
		WithInlineContent([]byte("a note claiming the wrong height")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(7).
		WithCreatedAt(epoch.Add(7 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.Envelope()
	if err != nil {
		return err
	}
	return g.addBroken("rejected-wrong-height", raw, c.ID().String(), vectors.ReasonHeightWrong,
		"height 7 where the only reference is a height-0 contributor, so 1 is derived",
		"V-HEIGHT")
}

// unresolvableContributor attributes a claim to an identity absent from the set, so
// no pubkey answers for the signature.
func (g *gen) unresolvableContributor(ctx context.Context) error {
	_, who, err := contributorClaim(ctx, signer("ranke-vectors/absent"), epoch.Add(8*time.Second))
	if err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte("a note by an identity not in this set")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(9 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.Envelope()
	if err != nil {
		return err
	}
	return g.addBroken("rejected-absent-contributor", raw, c.ID().String(), vectors.ReasonNoContributor,
		"its contributor edge names a claim this set omits, so the pubkey cannot be resolved",
		"V-REF", "V-SIG")
}

// tamperedBlob offers different bytes under the external content's hash.
func (g *gen) tamperedBlob() error {
	hash, err := ranke.HashContent(externalBlob)
	if err != nil {
		return err
	}
	return g.addContent("rejected-external-blob", []byte("tampered content, same declared hash"),
		hash, false, vectors.ReasonContentMismatch,
		"bytes that do not hash to the content_hash external-content declares",
		"V-CONTENT")
}
