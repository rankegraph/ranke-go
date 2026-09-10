// package: ranke / claim
// type:    logic
// job:     ClaimBuilder — assembles, validates, attributes, and signs a claim into an immutable Claim
// limits:  pure construction helpers live in claim_helpers.go; signing primitives in sign.go
package ranke

import (
	"bytes"
	"context"
	"crypto"
	"sort"
	"time"
)

// ClaimBuilder is the data-only input to ClaimBuilder{...}.Sign(): Type is
// required, and a Contributor except on the root contributor claim (§4.3).
type ClaimBuilder struct {
	Type          string
	TypeClass     NodeClass
	TypeSub       string
	Encoding      string
	EncodingClass EncodingClass
	EncodingSub   string
	InlineContent []byte
	ContentHash   Id
	ContentSize   uint64
	CreatedAt     time.Time
	// Dated is the time the claim's subject is assumed to stem from, an EDTF Level 1
	// value (`V-DATED`); "" leaves it absent.
	Dated       string
	Contributor Contributor
	// DiffOf makes this claim a diff over the referenced predecessor; the loader
	// materialises the chain.
	DiffOf Id
	Edges  []Edge
	Fields map[string]string
	// Height answers the claim's generation number (§4.1) from the references the
	// assembled claim carries: FixedHeight for a value already known, HeightsIn or
	// HeightsFrom to derive it. A referencing claim must set one.
	Height HeightResolver
	// heightCtx is the context WithHeightResolver took; a struct literal leaves it
	// nil and the resolver is asked against context.Background().
	heightCtx context.Context
	// SigningKey signs this claim's envelope. A contributor claim's pubkey is its
	// InlineContent, multikey-encoded.
	SigningKey crypto.Signer
	// allowInvalid backs AllowInvalid: the rules go unapplied, the sealing still
	// happens. Unexported, so the mode is reachable only through the setter.
	allowInvalid bool
}

// NewClaim seeds a ClaimBuilder with the required type and attributing
// contributor. Chain With* setters for the optionals, then call .Sign().
func NewClaim(typ string, contributor Contributor) ClaimBuilder {
	return ClaimBuilder{
		Type:        typ,
		Contributor: contributor,
	}
}

// Sign finalizes a ClaimBuilder into an immutable Claim. A variadic key
// overrides SigningKey, which otherwise comes from the Contributor (§5.7).
func (b ClaimBuilder) Sign(signingKey ...crypto.Signer) (Claim, error) {
	if len(signingKey) > 0 && signingKey[0] != nil {
		b.SigningKey = signingKey[0]
	}
	return buildClaim(b)
}

// --- chained setters ---

// WithType sets the claim type ("class/sub").
func (b ClaimBuilder) WithType(t string) ClaimBuilder { b.Type = t; return b }

// WithEncoding sets the content media type ("class/sub").
func (b ClaimBuilder) WithEncoding(e string) ClaimBuilder { b.Encoding = e; return b }

// WithDiff makes this claim a diff over the predecessor at id, restating only
// what differs, and adds the contribution/diff edge.
func (b ClaimBuilder) WithDiff(id Id) ClaimBuilder { b.DiffOf = id; return b }

// WithInlineContent sets the content bytes the claim itself carries.
func (b ClaimBuilder) WithInlineContent(c []byte) ClaimBuilder { b.InlineContent = c; return b }

// WithExternalContent references content stored elsewhere by hash and byte
// size, exclusive with WithInlineContent.
func (b ClaimBuilder) WithExternalContent(hash Id, size uint64) ClaimBuilder {
	b.ContentHash = hash
	b.ContentSize = size
	return b
}

// WithCreatedAt sets the creation timestamp, which defaults to now in UTC.
func (b ClaimBuilder) WithCreatedAt(t time.Time) ClaimBuilder { b.CreatedAt = t; return b }

// WithDated sets `dated` to t's calendar day, UTC (`V-DATED`) — distinct from
// CreatedAt, when the archive witnessed it. For anything EDTF alone can say — an
// interval, a decade, a season, an uncertain or approximate value — use
// WithDatedEDTF directly.
func (b ClaimBuilder) WithDated(t time.Time) ClaimBuilder {
	b.Dated = t.UTC().Format("2006-01-02")
	return b
}

// WithDatedEDTF sets `dated` to a raw EDTF Level 1 value (`V-DATED`), for a value
// WithDated's single calendar day can't express.
func (b ClaimBuilder) WithDatedEDTF(d string) ClaimBuilder { b.Dated = d; return b }

// HeightResolver answers the generation number (§4.1) a claim carries, given the
// references its assembled edge set holds — the contributor edge included, since the
// edge set is the input. `V-HEIGHT` fixes the answer at 1 + max over those
// references' heights, so a resolver that cannot reach one errors rather than
// treating it as height 0, which would build a claim verification refuses.
type HeightResolver func(ctx context.Context, refs []Id) (uint64, error)

// FixedHeight answers h whatever the references are — the resolver for a caller
// that already holds the value, and what WithHeight sets.
func FixedHeight(h uint64) HeightResolver {
	return func(context.Context, []Id) (uint64, error) { return h, nil }
}

// HeightsIn derives the height from u: 1 + max over the references' committed
// heights, and 0 for a claim referencing nothing.
func HeightsIn(u Universe) HeightResolver {
	return func(ctx context.Context, refs []Id) (uint64, error) {
		if len(refs) == 0 {
			return 0, nil
		}
		heights, err := u.GetClaimHeights(ctx, refs)
		if err != nil {
			return 0, err
		}
		if len(heights) != len(refs) {
			return 0, WithDetail(ErrNotFound, "the store answered fewer heights than the references given")
		}
		return maxHeight(heights) + 1, nil
	}
}

// HeightsFrom derives the height from claims already in hand, for a caller building
// against claims in memory rather than a Universe. A reference none of claims carries
// is reported absent, which is what catches a claim citing one the caller left out.
func HeightsFrom(claims ...Claim) HeightResolver {
	byID := make(map[string]uint64, len(claims))
	for _, c := range claims {
		if c == nil {
			continue
		}
		byID[c.ID().String()] = c.Node().Height()
	}
	return func(_ context.Context, refs []Id) (uint64, error) {
		if len(refs) == 0 {
			return 0, nil
		}
		heights := make([]uint64, len(refs))
		for i, r := range refs {
			if r == nil {
				return 0, WithDetail(ErrNotFound, "a nil reference has no height to resolve")
			}
			h, ok := byID[r.String()]
			if !ok {
				return 0, WithDetail(ErrNotFound,
					"reference "+r.String()+" is absent from the claims HeightsFrom was given")
			}
			heights[i] = h
		}
		return maxHeight(heights) + 1, nil
	}
}

// maxHeight is the greatest of heights, 0 when empty.
func maxHeight(heights []uint64) uint64 {
	var max uint64
	for _, h := range heights {
		if h > max {
			max = h
		}
	}
	return max
}

// WithHeight sets the generation number (§4.1) as a value already known — 1 + max
// over the referenced heights, or 0 for an initial claim. HeightsIn and HeightsFrom
// derive it instead. The verifier re-derives and enforces it either way.
func (b ClaimBuilder) WithHeight(h uint64) ClaimBuilder { b.Height = FixedHeight(h); return b }

// WithHeightResolver makes Sign ask resolve for the height, against every reference
// the closed claim carries. For a caller whose heights live somewhere other than a
// Universe — a database, or claims in memory.
func (b ClaimBuilder) WithHeightResolver(ctx context.Context, resolve HeightResolver) ClaimBuilder {
	b.heightCtx, b.Height = ctx, resolve
	return b
}

// WithAutoHeight resolves the height against u.
func (b ClaimBuilder) WithAutoHeight(ctx context.Context, u Universe) ClaimBuilder {
	if u == nil {
		return b.WithHeightResolver(ctx, nil)
	}
	return b.WithHeightResolver(ctx, HeightsIn(u))
}

// WithContributor sets the attributing contributor.
func (b ClaimBuilder) WithContributor(c Contributor) ClaimBuilder { b.Contributor = c; return b }

// WithSigningKey sets the key used to sign the claim id.
func (b ClaimBuilder) WithSigningKey(k crypto.Signer) ClaimBuilder { b.SigningKey = k; return b }

// AllowInvalid builds a claim the rules refuse: the type vocabulary, field limits,
// edge cardinality, the content slots and the key/pubkey pairing all go unjudged.
// What still happens is the sealing — canonical bytes, envelope, id — so the result
// is a real record breaking exactly what its caller aimed at, which is what a
// conformance case needs. A Sequencer verifies on admission, so such a claim reaches
// no archive through the front door.
func (b ClaimBuilder) AllowInvalid() ClaimBuilder { b.allowInvalid = true; return b }

// WithEdges appends the given edges to the builder's Edges slice.
func (b ClaimBuilder) WithEdges(edges ...Edge) ClaimBuilder {
	b.Edges = append(b.Edges, edges...)
	return b
}

// WithField sets one implementation-defined node field (§4.1) in a copied map.
func (b ClaimBuilder) WithField(key, value string) ClaimBuilder {
	f := make(map[string]string, len(b.Fields)+1)
	for k, v := range b.Fields {
		f[k] = v
	}
	f[key] = value
	b.Fields = f
	return b
}

// buildClaim constructs a Claim atomically per §4.3: resolve type, content mode
// and encoding; assemble and validate the edge set; build the node; sign it.
func buildClaim(cfg ClaimBuilder) (Claim, error) {
	if err := resolveType(&cfg); err != nil {
		return nil, err
	}
	hasInline, hasExternal := resolveContentState(&cfg)
	// Ahead of the encoding, which a claim carrying both slots would be judged for
	// first, naming the wrong defect.
	if !cfg.allowInvalid {
		if err := checkContentState(cfg, hasInline, hasExternal); err != nil {
			return nil, err
		}
	}
	if err := resolveEncoding(&cfg, hasInline || hasExternal); err != nil {
		return nil, err
	}

	isRootContributor := cfg.TypeClass == NodeClassContribution &&
		cfg.TypeSub == "contributor" &&
		cfg.Contributor == nil
	if !isRootContributor && cfg.Contributor == nil {
		return nil, errClaimContributorRequired
	}

	edges, err := assembleEdges(cfg, isRootContributor)
	if err != nil {
		return nil, err
	}
	if err := checkClaim(cfg, edges); err != nil {
		return nil, err
	}

	n := &node{
		typeClass:     cfg.TypeClass,
		typeSub:       cfg.TypeSub,
		encodingClass: cfg.EncodingClass,
		encodingSub:   cfg.EncodingSub,
		createdAt:     normalizeCreatedAt(cfg.CreatedAt),
		dated:         cfg.Dated,
		fields:        cloneFields(cfg.Fields),
	}
	if err := applyContent(n, cfg, hasInline, hasExternal); err != nil {
		return nil, err
	}
	n.edges = make([]Id, len(edges))
	for i, e := range edges {
		n.edges[i] = e.id
	}

	height, err := resolveHeight(cfg, edges)
	if err != nil {
		return nil, err
	}
	n.height = height

	env, err := signNode(n, edges, &cfg, isRootContributor)
	if err != nil {
		return nil, err
	}

	// The envelope the id was taken over, kept as the stored record: a built claim
	// and a decoded one then answer Encode() with the same bytes.
	c := &claim{node: n, edges: edges, raw: env}
	if isRootContributor {
		c.contributor = c // self-attribute
	} else {
		c.contributor = cfg.Contributor
	}
	return c, nil
}

// checkClaim applies the rules a claim must satisfy, which AllowInvalid skips. The
// steps around it resolve and seal, so what it governs is validity alone.
func checkClaim(cfg ClaimBuilder, edges []*edge) error {
	if cfg.allowInvalid {
		return nil
	}
	if err := checkType(cfg); err != nil {
		return err
	}
	if err := checkFields(cfg.Fields); err != nil {
		return err
	}
	if cfg.Dated != "" {
		if err := validateDated(cfg.Dated); err != nil {
			return err
		}
	}
	return CheckDeletable(cfg.TypeClass, cfg.TypeSub, cfg.Fields)
}

// resolveType fills TypeClass/TypeSub from the combined Type, which wins. A type is
// needed whatever the mode: without one there is nothing to serialize.
func resolveType(cfg *ClaimBuilder) error {
	if cfg.Type != "" {
		class, sub, err := splitType(cfg.Type)
		if err != nil {
			return WrapDetail(errNewClaim, "Type", err)
		}
		cfg.TypeClass = NodeClass(class)
		cfg.TypeSub = sub
	}
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return errClaimTypeRequired
	}
	return nil
}

// checkType judges the vocabulary `V-TYPE` fixes and the subtype's shape.
func checkType(cfg ClaimBuilder) error {
	if !validNodeClass(cfg.TypeClass) {
		return WithDetail(errUnknownNodeClass, string(cfg.TypeClass))
	}
	return checkSubtype(cfg.TypeSub)
}

// resolveContentState reports the content mode — none / inline / external.
func resolveContentState(cfg *ClaimBuilder) (hasInline, hasExternal bool) {
	return cfg.InlineContent != nil, cfg.ContentHash != nil
}

// checkContentState judges the slots: `V-CONTENT` makes inline and external
// exclusive, and `R-FIELDS` caps what may be inlined.
func checkContentState(cfg ClaimBuilder, hasInline, hasExternal bool) error {
	if hasInline && hasExternal {
		return errClaimContentXOR
	}
	if hasInline && len(cfg.InlineContent) > maxInlineContent {
		return errInlineContentTooLarge
	}
	return nil
}

// maxInlineContent caps inline content at construction only (NewClaim/NewEdge);
// larger blobs belong in external content (dedup + streaming).
const maxInlineContent = 1 << 20 // 1 MiB

// resolveEncoding fills EncodingClass/Sub from the combined or split Encoding
func resolveEncoding(cfg *ClaimBuilder, hasContent bool) error {
	enc := cfg.Encoding
	if enc == "" && (cfg.EncodingClass != "" || cfg.EncodingSub != "") {
		enc = string(cfg.EncodingClass) + "/" + cfg.EncodingSub
	}
	class, sub, err := resolveContentEncoding(enc, hasContent)
	if err != nil {
		return err
	}
	cfg.EncodingClass, cfg.EncodingSub = class, sub
	return nil
}

// resolveContentEncoding parses "class/sub" and enforces the content⇔encoding
// coupling shared by nodes (NewClaim) and edges (NewEdge)
func resolveContentEncoding(encoding string, hasContent bool) (EncodingClass, string, error) {
	var class EncodingClass
	var sub string
	if encoding != "" {
		c, s, err := splitType(encoding)
		if err != nil {
			return "", "", WrapDetail(errNewClaim, "Encoding", err)
		}
		class, sub = EncodingClass(c), s
	}
	if !hasContent {
		if class != "" || sub != "" {
			return "", "", errEncodingWithoutContent
		}
		return "", "", nil
	}
	if class == "" {
		return "", "", errContentWithoutEncoding
	}
	if !validEncodingClass(class) {
		return "", "", WithDetail(errUnknownEncodingClass, string(class))
	}
	if err := checkEncodingSubtype(sub); err != nil {
		return "", "", err
	}
	return class, sub, nil
}

// assembleEdges builds the edge set (caller edges, the auto contributor edge, a
// diff edge), enforces diff naming and cardinality, and returns the edges ascending
// by id(e) (`V-EORDER`).
func assembleEdges(cfg ClaimBuilder, isRootContributor bool) ([]*edge, error) {
	edges := make([]*edge, 0, len(cfg.Edges)+1)
	for _, e := range cfg.Edges {
		ce, err := asConcreteEdge(e)
		if err != nil {
			return nil, Wrap(errNewClaim, err)
		}
		edges = append(edges, ce)
	}
	if !isRootContributor {
		ce, err := buildContributorEdge(cfg.Contributor)
		if err != nil {
			return nil, WrapDetail(errNewClaim, "build contribution/contributor edge", err)
		}
		edges = append(edges, ce)
	}
	// A diff claim overlays a predecessor: name it with a contribution/diff edge.
	if cfg.DiffOf != nil {
		de, err := newEdge(EdgeConfig{
			Reference: cfg.DiffOf,
			TypeClass: EdgeClassContribution,
			TypeSub:   string(EdgeSubtypeDiff),
		})
		if err != nil {
			return nil, WrapDetail(errNewClaim, "diff edge", err)
		}
		edges = append(edges, de)
		if err := checkDiffEdgeNames(edges); err != nil {
			return nil, err
		}
	}
	if !cfg.allowInvalid {
		if err := checkEdgeCardinality(edges); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(edges, func(i, j int) bool {
		return bytes.Compare(idBytes(edges[i].id), idBytes(edges[j].id)) < 0
	})
	return edges, nil
}

// checkDiffEdgeNames requires a unique, non-empty name on every edge of a diff
// claim beyond the singletons (contributor, diff) — overlay is name-keyed (`V-DIFFEDGE`).
func checkDiffEdgeNames(edges []*edge) error {
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		if e.typeClass == EdgeClassContribution &&
			(e.typeSub == "contributor" || e.typeSub == string(EdgeSubtypeDiff)) {
			continue
		}
		name, ok := e.fields[FieldName]
		if !ok || name == "" {
			return errDiffEdgeUnnamed
		}
		if _, dup := seen[name]; dup {
			return WithDetail(errDiffEdgeDupName, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// checkEdgeCardinality enforces the per-claim singletons: at most one
// contribution/contributor edge and one contribution/diff edge.
func checkEdgeCardinality(edges []*edge) error {
	var nContrib, nDiff int
	for _, e := range edges {
		if e.typeClass != EdgeClassContribution {
			continue
		}
		switch e.typeSub {
		case "contributor":
			nContrib++
		case string(EdgeSubtypeDiff):
			nDiff++
		}
	}
	if nContrib > 1 {
		return errTwoContributors
	}
	if nDiff > 1 {
		return errTwoDiffEdges
	}
	return nil
}

// applyContent sets the node's content slots for the chosen mode: inline holds
// the bytes the id commits to (§Content), external the caller's hash+size.
func applyContent(n *node, cfg ClaimBuilder, hasInline, hasExternal bool) error {
	switch {
	case hasInline:
		n.content = cfg.InlineContent
		n.contentSize = uint64(len(cfg.InlineContent))
	case hasExternal:
		n.contentHash = cfg.ContentHash
		n.contentSize = cfg.ContentSize
	}
	return nil
}

// resolveHeight asks cfg.Height for the generation number (§4.1) over the references
// the assembled edges carry. One mechanism: a value already known arrives as
// FixedHeight, a derived one as HeightsIn or HeightsFrom. A claim referencing nothing
// is height 0 and needs no resolver, and refuses one answering otherwise.
func resolveHeight(cfg ClaimBuilder, edges []*edge) (uint64, error) {
	refs := make([]Id, len(edges))
	for i, e := range edges {
		refs[i] = e.reference
	}
	if cfg.Height == nil {
		if len(refs) == 0 {
			return 0, nil
		}
		return 0, errHeightRequired
	}
	ctx := cfg.heightCtx
	if ctx == nil {
		ctx = context.Background()
	}
	h, err := cfg.Height(ctx, refs)
	if err != nil {
		return 0, Wrap(errHeightResolve, err)
	}
	if len(refs) == 0 && h != 0 {
		return 0, errHeightOnInitial
	}
	return h, nil
}

// normalizeCreatedAt defaults a zero timestamp to now and normalises to UTC.
func normalizeCreatedAt(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

// signNode seals the claim into its envelope and sets the id it is filed under:
// S(v) signed (`V-ENV`), the id that envelope's hash (`V-ID`). It falls back to the
// Contributor's session key and rejects a key/pubkey mismatch (§5.7). The envelope
// is returned so the claim keeps the bytes rather than re-signing to recover them.
func signNode(n *node, edges []*edge, cfg *ClaimBuilder, isRootContributor bool) ([]byte, error) {
	if cfg.SigningKey == nil && cfg.Contributor != nil {
		cfg.SigningKey = cfg.Contributor.SigningKey()
	}
	if !cfg.allowInvalid {
		if err := checkSigningConsistency(*cfg, isRootContributor); err != nil {
			return nil, Wrap(errNewClaim, err)
		}
	}
	encoded, err := encodeNode(n, edges, nil)
	if err != nil {
		return nil, WrapDetail(errNewClaim, "canonical encode", err)
	}
	env, err := signEnvelope(cfg.SigningKey, encoded)
	if err != nil {
		return nil, WrapDetail(errNewClaim, "sign envelope", err)
	}
	nodeID, err := hashContent(env)
	if err != nil {
		return nil, WrapDetail(errNewClaim, "hash envelope", err)
	}
	n.id = nodeID
	return env, nil
}
