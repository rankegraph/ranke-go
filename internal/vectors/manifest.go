// package: internal/vectors / manifest
// type:    data
// job:     the reference-artifact manifest — the schema both the generator and the CLI read, over
// claims, content and bookmark records, plus lookup of the case describing one file
// limits:  the description only; generating artifacts is cmd/vectors' and verifying them is the
// library's (-> verify, verify_content)
package vectors

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Name is the manifest's filename inside an artifact set.
const Name = "manifest.json"

// Reason codes, so a test asserts the outcome it expected rather than any.
const (
	ReasonOK              = "ok"
	ReasonIDMismatch      = "id_mismatch"
	ReasonWrongMessage    = "wrong_message"
	ReasonMalformedID     = "malformed_id"
	ReasonNotEnveloped    = "not_enveloped"
	ReasonNoContributor   = "unresolvable_contributor"
	ReasonHeightWrong     = "height_wrong"
	ReasonContentMismatch = "content_hash_mismatch"
	ReasonTimestampForm   = "timestamp_form"
	ReasonBothContent     = "content_both_slots"
	ReasonEdgeOrder       = "edge_order"
	ReasonDatedForm       = "dated_form"
	// A record naming one signature scheme in its header and another in its pubkey's
	// framing, which `V-SIGN` requires to agree.
	ReasonSignatureScheme = "signature_scheme"
	// The first branch table's fixed height, read off the record alone.
	ReasonFirstTableHeight = "first_table_height"
	// The bookmark codes, one per rule a 𝒰_hist record can break.
	ReasonBookmarkForm      = "bookmark_form"
	ReasonBookmarkSignature = "bookmark_signature"
	ReasonBookmarkSlot      = "bookmark_slot"
	ReasonBookmarkReference = "bookmark_reference"
	ReasonBookmarkGap       = "bookmark_gap"
)

// AllReasons is every code above. The list lives beside the declarations so a new
// reason demands a case by existing — the hand-kept copy in a test was what let
// `V-TIME` arrive with no case at all.
var AllReasons = []string{
	ReasonOK, ReasonIDMismatch, ReasonWrongMessage, ReasonMalformedID,
	ReasonNotEnveloped, ReasonNoContributor, ReasonHeightWrong,
	ReasonContentMismatch, ReasonTimestampForm, ReasonBothContent, ReasonEdgeOrder,
	ReasonDatedForm, ReasonSignatureScheme, ReasonFirstTableHeight,
	ReasonBookmarkForm, ReasonBookmarkSignature, ReasonBookmarkSlot,
	ReasonBookmarkReference, ReasonBookmarkGap,
}

// Manifest names every artifact and the outcome an implementation must reach for it.
type Manifest struct {
	Note       string         `json:"note"`
	Provenance Provenance     `json:"provenance"`
	Claims     []ClaimCase    `json:"claims"`
	Content    []ContentCase  `json:"content"`
	Bookmarks  []BookmarkCase `json:"bookmarks,omitempty"`
}

// Provenance records what produced the set, so a reader can reproduce it. Version is
// "(devel)" unless the generator ran as a released module.
type Provenance struct {
	Generator   string `json:"generator"`
	Version     string `json:"version"`
	GeneratedAt string `json:"generated_at"`
}

// ClaimCase is one claim record under the id it is offered as. The id is not part of
// the record, so the pairing is what a case asserts.
type ClaimCase struct {
	File   string `json:"file"`
	Id     string `json:"id"`
	Verify bool   `json:"verify"`
	Reason string `json:"reason"`
	Why    string `json:"why"`
	// Violates names the rules this record breaks, and is what the coverage gate
	// counts (-> scripts/rule-vectors.sh). Empty for a case that must verify.
	Violates []string `json:"violates,omitempty"`
}

// BookmarkCase is one 𝒰_hist record under the id_seq(i, s) slot it is offered at
// (`V-BMENV`). A case naming a List belongs to that whole list rather than standing
// alone: the list is assembled from every case sharing the name, opened at the one
// marked Open, and judged together — which is the only register `V-BMGAPLESS` has.
type BookmarkCase struct {
	File     string   `json:"file"`
	Slot     string   `json:"slot"`
	Verify   bool     `json:"verify"`
	Reason   string   `json:"reason"`
	Why      string   `json:"why"`
	List     string   `json:"list,omitempty"`
	Open     bool     `json:"open,omitempty"`
	Violates []string `json:"violates,omitempty"`
}

// ContentCase is one content blob under the hash it is offered as.
type ContentCase struct {
	File     string   `json:"file"`
	Hash     string   `json:"hash"`
	Verify   bool     `json:"verify"`
	Reason   string   `json:"reason"`
	Why      string   `json:"why"`
	Violates []string `json:"violates,omitempty"`
}

// Load reads the manifest in dir.
func Load(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, Name))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// searchDepth bounds the walk upward, enough to reach a set's root from its
// claims/ or content/ subdirectory.
const searchDepth = 4

// Find looks for a manifest at or above the directory holding path, so a file named
// by a person rather than by its id can still be identified. It returns the manifest
// and the directory it came from.
func Find(path string) (*Manifest, string, bool) {
	dir := filepath.Dir(path)
	for range searchDepth {
		if m, err := Load(dir); err == nil {
			return m, dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil, "", false
}

// ClaimFor returns the case describing file, which is named relative to root.
func (m *Manifest) ClaimFor(root, file string) *ClaimCase {
	rel, err := relative(root, file)
	if err != nil {
		return nil
	}
	for i := range m.Claims {
		if m.Claims[i].File == rel {
			return &m.Claims[i]
		}
	}
	return nil
}

// ContentFor returns the case describing file, which is named relative to root.
func (m *Manifest) ContentFor(root, file string) *ContentCase {
	rel, err := relative(root, file)
	if err != nil {
		return nil
	}
	for i := range m.Content {
		if m.Content[i].File == rel {
			return &m.Content[i]
		}
	}
	return nil
}

// relative names file the way the manifest does: slash-separated, from root.
func relative(root, file string) (string, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
