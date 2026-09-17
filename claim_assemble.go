// package: ranke / claim
// type:    logic
// job:     AssembleClaim — rebuild a Claim from its parsed components + id, without CBOR and
// without signing (the field-oriented sibling of DecodeClaim, for non-CBOR cache backends)
// limits:  reconstructs a cache view, not a self-verifiable claim — it has no canonical bytes, so
// it cannot be structurally verified; a cache is checked by comparison to the
// authoritative layer (-> verify.go)
package ranke

import (
	"bytes"
	"sort"
	"strconv"
	"time"
)

// ClaimParts rebuilds a stored claim without its canonical CBOR, which a
// graph-native cache holds as node and edge properties. External content needs no
// blob — the id commits to hash and size — while inline content needs the bytes.
type ClaimParts struct {
	ID            Id                // the node/claim id (a signature — taken as given, not recomputed)
	Type          string            // "class/sub"
	Encoding      string            // "class/sub", or "" when there is no content
	CreatedAt     time.Time         // must retain nanosecond precision to re-encode identically
	Height        uint64            // §4.1 generation number (0 for an initial claim); part of the id-preimage
	Dated         string            // EDTF Level 1 (`V-DATED`); "" when absent
	ContentHash   Id                // nil when the claim carries no content
	ContentSize   uint64            //
	InlineContent []byte            // present for inline content; omitted for external
	Fields        map[string]string //
	Edges         []EdgeParts       //
	Tags          map[string]string // mutable runtime tags (branch membership, revision) — not part of the id
}

// EdgeParts is the parsed structure of one edge. ID (the derived edge id) is
// required: a field-oriented cache must store it, since recomputing it means
// re-serializing S(e) from the claim CBOR the cache does not hold.
type EdgeParts struct {
	ID                Id // required — the cached edge id
	Reference         Id
	Type              string // "class/sub"
	Encoding          string // "class/sub", or "" when the edge has no content
	RelationDirection RelationDirection
	ContentHash       Id
	ContentSize       uint64
	InlineContent     []byte
	Fields            map[string]string
}

// AssembleClaim rebuilds a Claim from parts, taking every id as given and ordering
// edges canonically, so faithful parts re-encode to identical bytes. The closure
// verifier over the authoritative Universe is what checks the result.
func AssembleClaim(parts ClaimParts) (Claim, error) {
	if parts.ID == nil {
		return nil, errNilID
	}
	nClass, nSub, err := splitType(parts.Type)
	if err != nil {
		return nil, WrapDetail(errAssemble, "type", err)
	}
	// `V-CONTENT` forbids both slots. It matters most here: these parts carry no
	// canonical bytes, so a claim built from both re-encodes to neither record.
	if len(parts.InlineContent) > 0 && parts.ContentHash != nil {
		return nil, WrapDetail(errAssemble, "content", ErrContentBothSlots)
	}
	// `V-TIME`: these fields arrive as an unparsed string map, and this door is the
	// only one a graph-native rebuild passes through.
	if err := checkTimestampFields(parts.Fields); err != nil {
		return nil, WrapDetail(errAssemble, "fields", err)
	}
	// Parts describe a record that exists, so an unset created_at is a projection that
	// lost it rather than a value to default (`V-MONO`).
	if NamesNoInstant(parts.CreatedAt) {
		return nil, WrapDetail(errAssemble, "created_at", ErrCreatedAtZero)
	}
	// `V-DATED`: absence is no violation — dated is optional.
	if parts.Dated != "" {
		if err := validateDated(parts.Dated); err != nil {
			return nil, WrapDetail(errAssemble, "dated", err)
		}
	}
	n := &node{
		typeClass:   NodeClass(nClass),
		typeSub:     nSub,
		height:      parts.Height,
		contentHash: parts.ContentHash,
		contentSize: parts.ContentSize,
		content:     parts.InlineContent,
		createdAt:   parts.CreatedAt.UTC(),
		dated:       parts.Dated,
		fields:      cloneFields(parts.Fields),
		id:          parts.ID,
	}
	if parts.Encoding != "" {
		eClass, eSub, err := splitType(parts.Encoding)
		if err != nil {
			return nil, WrapDetail(errAssemble, "encoding", err)
		}
		n.encodingClass = EncodingClass(eClass)
		n.encodingSub = eSub
	}
	edges := make([]*edge, len(parts.Edges))
	for i, ep := range parts.Edges {
		if ep.Reference == nil {
			return nil, WrapDetail(errAssemble, "edge "+strconv.Itoa(i), errEdgeRefRequired)
		}
		class, sub, err := splitType(ep.Type)
		if err != nil {
			return nil, WrapDetail(errAssemble, "edge "+strconv.Itoa(i)+" type", err)
		}
		if len(ep.InlineContent) > 0 && ep.ContentHash != nil {
			return nil, WrapDetail(errAssemble, "edge "+strconv.Itoa(i)+" content", ErrContentBothSlots)
		}
		if err := checkTimestampFields(ep.Fields); err != nil {
			return nil, WrapDetail(errAssemble, "edge "+strconv.Itoa(i)+" fields", err)
		}
		e := &edge{
			reference:         ep.Reference,
			typeClass:         EdgeClass(class),
			typeSub:           sub,
			contentHash:       ep.ContentHash,
			contentSize:       ep.ContentSize,
			content:           ep.InlineContent,
			relationDirection: ep.RelationDirection,
			fields:            cloneFields(ep.Fields),
		}
		if ep.Encoding != "" {
			eeClass, eeSub, err := splitType(ep.Encoding)
			if err != nil {
				return nil, WrapDetail(errAssemble, "edge "+strconv.Itoa(i)+" encoding", err)
			}
			e.encodingClass = EncodingClass(eeClass)
			e.encodingSub = eeSub
		}
		if ep.ID == nil {
			return nil, WithDetail(errAssemble, "edge "+strconv.Itoa(i)+": id required")
		}
		e.id = ep.ID
		edges[i] = e
	}
	// Ascending by id(e) (`V-EORDER`), matching construction, so node.edges and the
	// serialized claim reproduce the original bytes.
	sort.SliceStable(edges, func(i, j int) bool {
		return bytes.Compare(idBytes(edges[i].id), idBytes(edges[j].id)) < 0
	})
	n.edges = make([]Id, len(edges))
	for i, e := range edges {
		n.edges[i] = e.id
	}
	return &claim{node: n, edges: edges, tags: cloneFields(parts.Tags)}, nil
}
