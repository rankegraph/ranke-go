// package: cmd/vectors / malformed
// type:    cmd
// job:     the records no builder here will produce — a timestamp outside the form `V-TIME` fixes,
// and a record carrying both content slots `V-CONTENT` forbids — each offered under an id that
// holds for its bytes, so the named defect is the only one
// limits:  patches the CBOR of a record this program built (-> graph.go); what the rule requires is
// read from the paper, never from what this repo's decoder happens to do
package main

import (
	"errors"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/internal/vectors"
	"github.com/veraison/go-cose"
)

// errIDFraming fires when this file's derived id disagrees with the library's over
// the same bytes, which would make every patched id wrong alike.
var errIDFraming = errors.New("cmd/vectors: re-derived id does not match the library's")

// errTooFewEdges fires when the edge-order case has too few edges to reorder, which
// would ship a vector carrying no defect.
var errTooFewEdges = errors.New("cmd/vectors: the edge-order case needs a claim with at least two edges")

// Record keys `V-SER` fixes, the ones these cases reach for.
const (
	keyContentHash = 5
	keyCreatedAt   = 9
	keyEdges       = 10
)

// noNanoCreatedAt is epoch in plain RFC 3339 — the near-miss `V-TIME` exists to
// outlaw, and the form this repo's own generator once wrote. A reader that accepts
// it reads one instant in two forms.
const noNanoCreatedAt = "2023-11-14T22:13:20Z"

// unparsableTime is a value no reader can take for a timestamp at all.
const unparsableTime = "whenever"

// malformed writes the records that break `V-TIME` and `V-CONTENT`. The two field
// cases go through the builder, which does not judge a field's value, so their only
// defect is the timestamp; the other two are patched bytes, since no encoder here
// emits them.
func (g *gen) malformed() error {
	if err := g.badFieldTime("rejected-delete-by-form", ranke.FieldDeleteBy,
		"delete_by is not RFC 3339: a deletion due date no reader can place in time"); err != nil {
		return err
	}
	if err := g.badFieldTime("rejected-pubkey-window-form", ranke.FieldPubkeyExpiresAfter,
		"pubkey_expires_after is not RFC 3339, so the key window it states cannot be read"); err != nil {
		return err
	}
	if err := g.badCreatedAt(); err != nil {
		return err
	}
	if err := g.badDated(); err != nil {
		return err
	}
	return g.bothContentSlots()
}

// badDated signs a claim whose `dated` carries an unparsable value — neither a
// timestamp nor EDTF Level 1. Unlike a generic field, the builder does judge
// `dated` (`V-DATED`), so this goes through AllowInvalid to still seal a record.
func (g *gen) badDated() error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), g.who).
		WithInlineContent([]byte("a note dated " + unparsableTime)).
		WithEncoding(ranke.EncodingPlain).
		WithDatedEDTF(unparsableTime).
		WithHeight(1).
		WithCreatedAt(epoch.Add(13 * time.Second)).
		AllowInvalid().
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.Envelope()
	if err != nil {
		return err
	}
	return g.addBroken("rejected-dated-form", raw, c.ID().String(), vectors.ReasonDatedForm,
		"dated is neither an RFC 3339 timestamp nor a valid EDTF Level 1 value", "V-DATED")
}

// badFieldTime signs a record whose field carries an unparsable timestamp. The
// builder judges no field value, so the timestamp is the whole defect.
func (g *gen) badFieldTime(name, field, why string) error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), g.who).
		WithInlineContent([]byte("a note carrying "+field+"="+unparsableTime)).
		WithEncoding(ranke.EncodingPlain).
		WithField(field, unparsableTime).
		WithHeight(1).
		WithCreatedAt(epoch.Add(10 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.Envelope()
	if err != nil {
		return err
	}
	return g.addBroken(name, raw, c.ID().String(), vectors.ReasonTimestampForm, why, "V-TIME")
}

// badCreatedAt rewrites created_at to a form that drops the nanoseconds, which is
// the near-miss a reader is likeliest to accept.
func (g *gen) badCreatedAt() error {
	raw, id, err := g.patchedRecord([]byte("a note whose created_at drops its nanoseconds"),
		func(node map[uint64]cbor.RawMessage) error {
			enc, err := ranke.MarshalCBOR(noNanoCreatedAt)
			if err != nil {
				return err
			}
			node[keyCreatedAt] = enc
			return nil
		})
	if err != nil {
		return err
	}
	return g.addBroken("rejected-created-at-form", raw, id.String(), vectors.ReasonTimestampForm,
		"created_at is RFC 3339 without the nanosecond fraction the rule fixes", "V-TIME")
}

// bothContentSlots adds content_hash to a record that already carries content, the
// malformed shape another implementation might store.
func (g *gen) bothContentSlots() error {
	raw, id, err := g.patchedRecord([]byte("a note in both content slots at once"),
		func(node map[uint64]cbor.RawMessage) error {
			addr, err := ranke.HashContent(externalBlob)
			if err != nil {
				return err
			}
			// A record holds the multihash unwrapped, which is what an Id's multibase
			// string form decodes back to.
			_, hash, err := multibase.Decode(addr.String())
			if err != nil {
				return err
			}
			enc, err := ranke.MarshalCBOR(hash)
			if err != nil {
				return err
			}
			node[keyContentHash] = enc
			return nil
		})
	if err != nil {
		return err
	}
	return g.addBroken("rejected-both-content-slots", raw, id.String(), vectors.ReasonBothContent,
		"one record carrying content and content_hash, which the rule makes exclusive", "V-CONTENT")
}

// patchedRecord patches the serialized claim inside a fresh claim's envelope and
// seals it again. Re-sealing leaves ONE defect: the old id and signature would both
// fail first, hiding the one named.
func (g *gen) patchedRecord(content []byte, patch func(map[uint64]cbor.RawMessage) error) ([]byte, ranke.Id, error) {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), g.who).
		WithInlineContent(content).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(11 * time.Second)).
		Sign()
	if err != nil {
		return nil, nil, err
	}
	return patchedClaim(c, patch)
}

// patchedClaim is patchedRecord over a claim already built, for a defect needing a
// shape the one above lacks. It signs under the root identity, as sealPayload does.
func patchedClaim(c ranke.Claim, patch func(map[uint64]cbor.RawMessage) error) ([]byte, ranke.Id, error) {
	raw, err := c.Envelope()
	if err != nil {
		return nil, nil, err
	}
	// The framing below is re-derived here, so it is proven against the record whose
	// id the library just produced: same payload, same id, or the framing is wrong.
	unpatched, err := patchPayload(raw, func(map[uint64]cbor.RawMessage) error { return nil })
	if err != nil {
		return nil, nil, err
	}
	_, check, err := sealPayload(unpatched)
	if err != nil {
		return nil, nil, err
	}
	if !check.Equal(c.ID()) {
		return nil, nil, errIDFraming
	}
	patched, err := patchPayload(raw, patch)
	if err != nil {
		return nil, nil, err
	}
	return sealPayload(patched)
}

// sealPayload derives the envelope and id(v) for a payload this program assembled: a
// COSE_Sign1 under the `EdDSA` header (`V-ENV`, `V-SIGN`), hashed as `V-HASH` fixes.
func sealPayload(payload []byte) ([]byte, ranke.Id, error) {
	sgn, err := cose.NewSigner(cose.AlgorithmEd25519, signer(rootSeed))
	if err != nil {
		return nil, nil, err
	}
	msg := cose.NewSign1Message()
	msg.Payload = payload
	msg.Headers.Protected[cose.HeaderLabelAlgorithm] = cose.AlgorithmEd25519
	if err := msg.Sign(nil, nil, sgn); err != nil {
		return nil, nil, err
	}
	env, err := msg.MarshalCBOR()
	if err != nil {
		return nil, nil, err
	}
	mh, err := multihash.Sum(env, multihash.SHA2_256, -1)
	if err != nil {
		return nil, nil, err
	}
	s, err := multibase.Encode(multibase.Base32, mh)
	if err != nil {
		return nil, nil, err
	}
	id, err := ranke.ParseId(s)
	return env, id, err
}

// reverseEdges flips the edges array, which `V-EORDER` fixes ascending by id(e).
func reverseEdges(node map[uint64]cbor.RawMessage) error {
	var edges []cbor.RawMessage
	if err := cbor.Unmarshal(node[keyEdges], &edges); err != nil {
		return err
	}
	if len(edges) < 2 {
		return errTooFewEdges
	}
	for i, j := 0, len(edges)-1; i < j; i, j = i+1, j-1 {
		edges[i], edges[j] = edges[j], edges[i]
	}
	enc, err := ranke.MarshalCBOR(edges)
	if err != nil {
		return err
	}
	node[keyEdges] = enc
	return nil
}

// patchPayload rewrites the serialized claim an envelope carries. Untouched keys pass
// through raw, so the patch is the only difference.
func patchPayload(env []byte, patch func(map[uint64]cbor.RawMessage) error) ([]byte, error) {
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(env); err != nil {
		return nil, err
	}
	var node map[uint64]cbor.RawMessage
	if err := cbor.Unmarshal(msg.Payload, &node); err != nil {
		return nil, err
	}
	if err := patch(node); err != nil {
		return nil, err
	}
	return ranke.MarshalCBOR(node)
}
