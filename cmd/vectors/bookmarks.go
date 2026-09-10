// package: cmd/vectors / bookmarks
// type:    cmd
// job:     the 𝒰_hist cases — one valid bookmark list every implementation must open, plus a record
// per rule a bookmark can break: its envelope, its signature, its slot, its k, and its list's
// contiguity
// limits:  builds records, not lists on disk; the head each one records comes from the conformance
// graph (-> graph.go)
package main

import (
	"crypto/ed25519"
	"strconv"

	"github.com/multiformats/go-multibase"
	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/internal/vectors"
	cose "github.com/veraison/go-cose"
)

// bookmarkSeed is the fixed seed the valid list is keyed on, so the artifacts
// reproduce byte for byte. A Sequencer mints one from crypto/rand; any byte string of
// enough entropy qualifies, and nothing keeps it secret.
var bookmarkSeed = []byte("ranke-vectors/bookmark-seed-0001")

// gapSeed keys the list with a hole in it, and each rejected record below keys its own
// seed. No two cases may share a slot: a downstream implementation loads the whole set
// into one 𝒰_hist, where a shared slot would put one case's record under another's key.
var (
	gapSeed       = []byte("ranke-vectors/bookmark-seed-gap0")
	payloadSeed   = []byte("ranke-vectors/bookmark-seed-form")
	signatureSeed = []byte("ranke-vectors/bookmark-seed-sig0")
	slotSeed      = []byte("ranke-vectors/bookmark-seed-slot")
	referenceSeed = []byte("ranke-vectors/bookmark-seed-ref0")
	kidSeed       = []byte("ranke-vectors/bookmark-seed-kid0")
)

// bookmarks writes the 𝒰_hist cases. The valid list comes first, since the rejected
// records are the same shape with one thing changed.
func (g *gen) bookmarkCases() error {
	if err := g.validList(); err != nil {
		return err
	}
	if err := g.gaplessBroken(); err != nil {
		return err
	}
	if err := g.badBookmarkPayload(); err != nil {
		return err
	}
	if err := g.badBookmarkSignature(); err != nil {
		return err
	}
	if err := g.badBookmarkSlot(); err != nil {
		return err
	}
	if err := g.badBookmarkKid(); err != nil {
		return err
	}
	return g.badBookmarkReference()
}

// validList is the archive's own bookmark list: k₀ at index 0 and k₁ at index 1,
// opened at index 1. Opening it there is the point — every bookmark carries s, so any
// one of them reaches the whole list and index 0 holds no privilege (§Backup).
func (g *gen) validList() error {
	heads := []string{"branch-table", "branch-table-revision"}
	for i, head := range heads {
		raw, err := ranke.SignBookmark(g.who, uint64(i), bookmarkSeed, g.ids[head])
		if err != nil {
			return err
		}
		c := vectors.BookmarkCase{
			Verify: true, Reason: vectors.ReasonOK, List: "archive", Open: i == len(heads)-1,
			Why: "index " + strconv.Itoa(i) + " of the archive's list, recording " + head,
		}
		if err := g.addBookmark("bookmark-"+strconv.Itoa(i), raw, uint64(i), bookmarkSeed, c); err != nil {
			return err
		}
	}
	return nil
}

// gaplessBroken is a list holding indices 0 and 2, so index 1 is a hole. Opened at 0
// the search settles the top at 0, and the bounded probe above it finds index 2 —
// present over a missing index, which is what falsifies the rule.
func (g *gen) gaplessBroken() error {
	for _, i := range []uint64{0, 2} {
		raw, err := ranke.SignBookmark(g.who, i, gapSeed, g.ids["branch-table"])
		if err != nil {
			return err
		}
		c := vectors.BookmarkCase{
			Reason: vectors.ReasonBookmarkGap, List: "gap", Open: i == 0,
			Why:      "a list holding indices 0 and 2, whose present indices are not one contiguous range",
			Violates: []string{"V-BMGAPLESS"},
		}
		if err := g.addBookmark("rejected-bookmark-gap-"+strconv.FormatUint(i, 10), raw, i, gapSeed, c); err != nil {
			return err
		}
	}
	return nil
}

// badBookmarkPayload signs S([i, s]) — the id_seq input rather than a bookmark's own
// three-element payload — so the record is a signed CBOR array of the wrong arity.
func (g *gen) badBookmarkPayload() error {
	payload, err := ranke.MarshalCBOR([]any{uint64(0), payloadSeed})
	if err != nil {
		return err
	}
	raw, err := signedBookmark(signer(rootSeed), g.ids["root-contributor"], payload)
	if err != nil {
		return err
	}
	return g.addBookmark("rejected-bookmark-payload", raw, 0, payloadSeed, vectors.BookmarkCase{
		Reason:   vectors.ReasonBookmarkForm,
		Why:      "a two-element payload S([i, s]) where a bookmark's is the three-element S([i, s, k])",
		Violates: []string{"V-BMENV"},
	})
}

// badBookmarkSignature names root-contributor as kid and signs under the other
// identity's key, so the signature is real and answers for the wrong contributor.
func (g *gen) badBookmarkSignature() error {
	payload, err := bookmarkPayload(0, signatureSeed, g.ids["branch-table"])
	if err != nil {
		return err
	}
	raw, err := signedBookmark(signer("ranke-vectors/other"), g.ids["root-contributor"], payload)
	if err != nil {
		return err
	}
	return g.addBookmark("rejected-bookmark-signature", raw, 0, signatureSeed, vectors.BookmarkCase{
		Reason:   vectors.ReasonBookmarkSignature,
		Why:      "signed under a key the kid's contributor claim does not publish",
		Violates: []string{"V-BMSIG"},
	})
}

// badBookmarkSlot offers a well-formed bookmark for index 1 at index 3's slot — the
// relocation a storage fault or a write to 𝒰_hist could produce, which the rule turns
// into absence at the slot rather than acceptance under a borrowed index.
func (g *gen) badBookmarkSlot() error {
	raw, err := ranke.SignBookmark(g.who, 1, slotSeed, g.ids["branch-table"])
	if err != nil {
		return err
	}
	return g.addBookmark("rejected-bookmark-slot", raw, 3, slotSeed, vectors.BookmarkCase{
		Reason:   vectors.ReasonBookmarkSlot,
		Why:      "a bookmark carrying i=1 offered at id_seq(3, s), which its own payload does not key",
		Violates: []string{"V-BMSLOT"},
	})
}

// badBookmarkKid names source-note as the kid, where the rule fixes a
// contribution/contributor claim. Whatever an implementation reads as a pubkey there
// answers for a key nobody published.
func (g *gen) badBookmarkKid() error {
	payload, err := bookmarkPayload(0, kidSeed, g.ids["branch-table"])
	if err != nil {
		return err
	}
	raw, err := signedBookmark(signer(rootSeed), g.ids["source-note"], payload)
	if err != nil {
		return err
	}
	return g.addBookmark("rejected-bookmark-kid", raw, 0, kidSeed, vectors.BookmarkCase{
		Reason:   vectors.ReasonBookmarkForm,
		Why:      "its kid names a source/note claim, where the rule fixes contribution/contributor",
		Violates: []string{"V-BMENV"},
	})
}

// badBookmarkReference records a source note as the head, where only a branch table
// can be one: a locator pointing at a claim no archive is headed by.
func (g *gen) badBookmarkReference() error {
	raw, err := ranke.SignBookmark(g.who, 0, referenceSeed, g.ids["source-note"])
	if err != nil {
		return err
	}
	return g.addBookmark("rejected-bookmark-reference", raw, 0, referenceSeed, vectors.BookmarkCase{
		Reason:   vectors.ReasonBookmarkReference,
		Why:      "its k resolves to a source/note claim, where the rule requires contribution/branches",
		Violates: []string{"V-BMREF"},
	})
}

// bookmarkPayload renders S([i, s, k]), the payload a bookmark envelope carries.
func bookmarkPayload(i uint64, seed []byte, head ranke.Id) ([]byte, error) {
	raw, err := idPayload(head)
	if err != nil {
		return nil, err
	}
	return ranke.MarshalCBOR([]any{i, seed, raw})
}

// signedBookmark seals payload as a bookmark envelope: alg and kid protected, nothing
// unprotected (`V-BMENV`). It is SignBookmark reopened, so a case can name one signer
// in the header and sign under another.
func signedBookmark(key ed25519.PrivateKey, kid ranke.Id, payload []byte) ([]byte, error) {
	sgn, err := cose.NewSigner(cose.AlgorithmEd25519, key)
	if err != nil {
		return nil, err
	}
	raw, err := idPayload(kid)
	if err != nil {
		return nil, err
	}
	msg := cose.NewSign1Message()
	msg.Payload = payload
	msg.Headers.Protected[cose.HeaderLabelAlgorithm] = cose.AlgorithmEd25519
	msg.Headers.Protected[cose.HeaderLabelKeyID] = raw
	if err := msg.Sign(nil, nil, sgn); err != nil {
		return nil, err
	}
	return msg.MarshalCBOR()
}

// idPayload unwraps an id to the multihash bytes a record carries, which is what its
// multibase string form decodes back to.
func idPayload(id ranke.Id) ([]byte, error) {
	_, raw, err := multibase.Decode(id.String())
	return raw, err
}
