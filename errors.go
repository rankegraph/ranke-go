// package: ranke / errors
// type:    data
// job:     centralized package error sentinels — one static error per fixed condition
// limits:  no fmt.Errorf anywhere in the package — dynamic errors compose via
// wrap/withDetail/wrapDetail over these sentinels
package ranke

import "errors"

var (
	// --- Universe / storage (exported API) ---
	ErrNotFound  = errors.New("ranke: not found")
	ErrIntegrity = errors.New("ranke: integrity check failed")
	ErrClosed    = errors.New("ranke: closed")
	// ErrUnsupported: the backend does not support this operation (e.g. an
	// opaque byte store asked to tag). Callers gate on the relevant Capability.
	ErrUnsupported = errors.New("ranke: operation not supported by this backend")
	// ErrContentCapped: a layer holds a claim/reference but its stored content
	// is shorter than the expected content_size — capped or truncated (e.g. a
	// cache filled under a smaller cap in a previous run). A stack treats it
	// like a miss and descends to a layer that holds the full bytes.
	ErrContentCapped = errors.New("ranke: content capped (stored shorter than content_size)")
	// ErrBranchNotFound: the archive holds no branch of that name. Also matches
	// ErrNotFound, so a caller may read it either way.
	ErrBranchNotFound = errors.New("ranke.Archive.GetBranch: branch not found")
	// ErrSequencerGenesis: the bookmark list holds no entry, so this archive does
	// not exist yet and every operation on it waits for Found. A caller branches on
	// Sequencer.InGenesis rather than on this error.
	ErrSequencerGenesis = errors.New("ranke.Sequencer: the archive awaits its first contributor (Found)")

	// --- Id ---
	errInvalidId     = errors.New("ranke: invalid id")
	errInvalidVarint = errors.New("ranke: invalid varint prefix")

	// --- Type parsing ---
	errEmptyType = errors.New("ranke: empty type")

	// --- Claim / Contributor ---
	errSigningKeyNoPubkey       = errors.New("ranke.Claim.AsContributor: signing key supplied but the contributor declares no pubkey to match it against")
	errSigningKeyMismatch       = errors.New("ranke.Claim.AsContributor: signing key does not match contributor pubkey")
	errResolveContributorPubkey = errors.New("ranke.Claim.AsContributor: cannot resolve contributor pubkey from content")
	errNilContributor           = errors.New("ranke: nil contributor")
	errResolvedNoPubkey         = errors.New("ranke: SigningKey supplied but resolved contributor has no pubkey")
	errResolvedNoKey            = errors.New("ranke: resolved contributor has a pubkey but no SigningKey was supplied")
	errResolvedMismatch         = errors.New("ranke: SigningKey's public key does not match the resolved contributor pubkey")

	// --- Node / content ---
	errContentExternal      = errors.New("ranke: content is external — use GetContent with a Universe")
	errNoUniverseForContent = errors.New("ranke: external content requires a Universe")

	// --- Edge ---
	errNilEdge          = errors.New("ranke: nil edge")
	errEdgeRefRequired  = errors.New("ranke.NewEdge: Reference is required")
	errEdgeTypeRequired = errors.New("ranke.NewEdge: Type (or TypeClass + TypeSub) is required")
	errEdgeContentXOR   = errors.New("ranke.NewEdge: inline and external content are mutually exclusive")
	errEdgeRelationDir  = errors.New("ranke.NewEdge: relation/* edges must set RelationDirection (RelationFrom or RelationTo)")

	// --- Claim builder ---
	errClaimTypeRequired        = errors.New("ranke.NewClaim: Type (or TypeClass + TypeSub) is required")
	errClaimContentXOR          = errors.New("ranke.NewClaim: inline and external content are mutually exclusive")
	errClaimContributorRequired = errors.New("ranke.NewClaim: Contributor is required (only the root contribution/contributor may omit it)")
	errEncodingWithoutContent   = errors.New("ranke.NewClaim: encoding set without content")
	errContentWithoutEncoding   = errors.New("content set without encoding: a content-bearing claim or edge must declare a media type")
	errDiffEdgeUnnamed          = errors.New("ranke.NewClaim: a diff claim's edges must be named (except the contributor)")
	errDiffEdgeDupName          = errors.New("ranke.NewClaim: duplicate edge name in a diff claim")
	errTwoContributors          = errors.New("ranke.NewClaim: a claim may carry only one contribution/contributor edge")
	errTwoDiffEdges             = errors.New("ranke.NewClaim: a claim may carry only one contribution/diff edge")
	errHeightRequired           = errors.New("ranke.NewClaim: a claim with references must declare its height (use WithHeight or WithAutoHeight)")
	errHeightOnInitial          = errors.New("ranke.NewClaim: an initial claim (no references) must have height 0")
	errHeightWithAuto           = errors.New("ranke.NewClaim: WithHeight and WithAutoHeight are mutually exclusive")

	// --- Graph ---
	errNilClaim          = errors.New("ranke: nil claim")
	errEmptyGraph        = errors.New("ranke.Graph.Consolidate: empty graph")
	errNoContributorEdge = errors.New("ranke: non-initial claim missing contribution/contributor edge")

	// --- Verify content ---
	errNilHash = errors.New("ranke: nil content hash")

	// --- Detail-carrying (used with withDetail; the detail is the value) ---
	errNotContributorClaim    = errors.New("ranke.Claim.AsContributor: claim is not contribution/contributor")
	errUnknownNodeClass       = errors.New("ranke.NewClaim: unknown node class")
	errUnknownEncodingClass   = errors.New("ranke.NewClaim: unknown encoding class")
	errUnknownEdgeClass       = errors.New("ranke.NewEdge: unknown edge class")
	errFieldNotSet            = errors.New("ranke: field not set")
	errInvalidFieldName       = errors.New("ranke: invalid field name (use lowercase letters, digits, and '_'; no leading '_')")
	errFieldNameTooLong       = errors.New("ranke: field name too long (max 128 bytes)")
	errFieldValueTooLong      = errors.New("ranke: field value too long (max 64 KiB) — put large data in content")
	errTooManyFields          = errors.New("ranke: too many fields on one record (max 256)")
	errInlineContentTooLarge  = errors.New("ranke: inline content too large (max 1 MiB) — use external content")
	errInvalidSubtype         = errors.New("ranke: invalid subtype (use lowercase letters, digits, and '_'; no leading '_')")
	errInvalidEncodingSubtype = errors.New("ranke: invalid encoding subtype")

	// --- Operation-prefix sentinels (fmt.Errorf replacements) ---
	// Used with wrap/wrapDetail: the sentinel is the operation prefix, the
	// detail carries the stage or a dynamic value, the cause is wrapped.
	errNewClaim              = errors.New("ranke.NewClaim")
	errNewEdge               = errors.New("ranke.NewEdge")
	errRelationDirNonRel     = errors.New("ranke.NewEdge: RelationDirection must be 0 for non-relation edges")
	errBuildGraph            = errors.New("ranke.NewGraphFromClosure")
	errGraphAddClaim         = errors.New("ranke.Graph.AddClaims")
	errConsolidate           = errors.New("ranke.Graph.Consolidate")
	errCopyClaims            = errors.New("ranke.CopyClaims")
	errCopyContents          = errors.New("ranke.CopyContents")
	errQuery                 = errors.New("ranke.Query")
	ErrQueryNoHead           = errors.New("ranke.Query: Select.Head is required under $universe (it has no natural head to scope by)")
	ErrQueryNoScope          = errors.New("ranke.Query: Select.Branch is required (scope is mandatory — use BranchUniverse for an unconfined read)")
	ErrQueryScanShape        = errors.New("ranke.Query: a scan (no Select.Path) reaches claims by no stated route, so Output.Shape must be single")
	ErrQueryEncoding         = errors.New("ranke.Query: unknown Output.Encoding (native | json | cbor)")
	errDecodeQuery           = errors.New("ranke.DecodeQuery")
	errEncodeQuery           = errors.New("ranke.EncodeQuery")
	ErrQueryWhereForm        = errors.New("ranke.Query: a where node is exactly one of and | or | not | {field, test}")
	ErrQueryTimeOperand      = errors.New("ranke.Query: a comparison on a time field takes a `V-TIME` timestamp or an EDTF Level 1 value (`R-QTIMEOP`)")
	ErrQueryComparisonForm   = errors.New("ranke.Query: a comparison applies exactly one operator (eq | ne | lt | le | gt | ge | in | glob)")
	ErrQueryHops             = errors.New("ranke.Query: a PathStep's hop bounds admit no count")
	ErrQueryEnum             = errors.New("ranke.Query: value outside the set the schema fixes for its field")
	ErrQueryBounds           = errors.New("ranke.Query: value below the minimum the schema fixes for its field")
	ErrQueryEnvelopeAxis     = errors.New("ranke.Query: detail envelope returns the stored bytes, which this axis would have to change (`R-QDETAIL`)")
	ErrQueryOrderField       = errors.New("ranke.Query: a sort key names the field it orders on")
	ErrQueryLayerName        = errors.New("ranke.Query: execution.layer names a layer, so a stated one may not be empty")
	ErrReservedType          = errors.New("ranke.Contribution: node type is the Sequencer's alone (lift it to add one)")
	ErrFutureDated           = errors.New("ranke.Contribution: claim is dated after the base the contribution opened against")
	ErrBranchNotCreatable    = errors.New("ranke.Contribution: branch is absent from the base, and creating one is a right of its own")
	ErrUnreadableReference   = errors.New("ranke.Contribution: reference reaches a claim outside the branches this contribution may read")
	errContributionRefs      = errors.New("ranke.AdmitReferences")
	ErrWire                  = errors.New("ranke.Wire")
	ErrWireKind              = errors.New("ranke.Wire: unknown record kind (0 claim | 1 content | 2 branches | 3 referencable | 4 lifted)")
	ErrWireNoBranch          = errors.New("ranke.Wire: a claim record must name the branch it joins")
	ErrWireNoHeader          = errors.New("ranke.Wire: a stream opens with the branches it writes to and the branches it may reference from")
	ErrWireLateConstraint    = errors.New("ranke.Wire: every constraint record precedes the payload")
	ErrWireUndeclared        = errors.New("ranke.Wire: claim names a branch the header does not declare")
	errVerify                = errors.New("ranke.verify")
	ErrKeyNotYetValid        = errors.New("ranke.verify: claim is dated before its contributor key's pubkey_valid_from")
	ErrKeyExpired            = errors.New("ranke.verify: claim is dated after its contributor key's pubkey_expires_after")
	errKeyWindowField        = errors.New("ranke.verify: contributor key validity bound is not RFC 3339")
	errContributorUnresolved = errors.New("ranke.verify: contributor claim unresolved")
	errHeightResolve         = errors.New("ranke.NewClaim: resolve reference height for WithAutoHeight")
	ErrDeleteByNotCopied     = errors.New("ranke.verify: an edge must carry exactly the delete_by its referenced claim declares")
	ErrStructureNotDeletable = errors.New("ranke: a contribution/* claim carries the graph's structure and its own identity, so it takes no delete_by")
	errHeightMismatch        = errors.New("ranke.verify: claim height ≠ 1 + max(reference heights)")
	ErrCreatedAtNotMonotone  = errors.New("ranke.verify: claim is dated before a claim it references")
	errNotBranchTable        = errors.New("ranke.Archive.Verify: head is not a contribution/branches claim")
	errRefsBranchTable       = errors.New("ranke.verify: claim references a branch table")
	errEncodeClaim           = errors.New("ranke: encode claim")
	errMarshalCBOR           = errors.New("ranke.MarshalCBOR")
	errDecodeClaim           = errors.New("ranke.DecodeClaim")
	errAssemble              = errors.New("ranke.AssembleClaim")
	errNodePreimage          = errors.New("ranke: claim CBOR has no node preimage")
	errNotArchive            = errors.New("ranke.TagArchive: Archive is not the built-in type")
	errID                    = errors.New("ranke.Id")
	errEncodePubkey          = errors.New("ranke: unsupported public key type")
	errDecodePubkey          = errors.New("ranke.DecodePublicKey")
	errSignEnvelope          = errors.New("ranke.signEnvelope")
	errDecodeEnvelope        = errors.New("ranke.decodeEnvelope")
	errVerifyEnvelope        = errors.New("ranke.verifyEnvelope")
	errEnvelopeNoKey         = errors.New("ranke.signEnvelope: a claim is signed, so a signing key is required (`V-SIG`)")
	errEnvelopeNoPubkey      = errors.New("ranke.verifyEnvelope: the contributor carries no pubkey, so nothing verifies its claims (`V-SIG`)")
	errEnvelopeNoPayload     = errors.New("ranke: the envelope carries no payload, so there is no claim in it")
	errNoEnvelope            = errors.New("ranke.Claim.Envelope: this claim holds no stored record — it was rebuilt from parts, which carry no signature; id")
	errLoadKeypair           = errors.New("ranke.LoadPrivateKey")
	errLoadPrivKey           = errors.New("ranke.LoadEd25519PrivateKeyPEM")
	errLoadPubKey            = errors.New("ranke.LoadEd25519PublicKeyPEM")
	errVerifyContentOp       = errors.New("ranke.VerifyContent")
	errVerifyingReader       = errors.New("ranke.NewVerifyingReader")
	errContent               = errors.New("ranke: content")
	errExpectedClassSub      = errors.New("ranke: expected \"class/sub\"")
	errEncodeSigningKey      = errors.New("ranke: encode signing key's pubkey")
	errArchiveLoadHead       = errors.New("ranke.NewArchive: load head")

	// --- Archive ---
	errNilUniverse = errors.New("ranke.NewArchive: nil Universe")
	errNilHeadID   = errors.New("ranke.NewArchive: nil head id")
	errNilID       = errors.New("ranke.Archive: nil id")

	// --- ADT shape, checked wherever a claim arrives rather than at the builder
	// alone: a record decoded or assembled meets these too.
	ErrContentBothSlots  = errors.New("ranke: a record carries both content and content_hash, which are mutually exclusive")
	ErrIDMismatch        = errors.New("ranke.verify: the claim's id is not the hash of the envelope it is stored as")
	ErrEnvelopeHeaders   = errors.New("ranke: an envelope carries the alg parameter alone, protected, and an empty unprotected header (`V-ENV`)")
	ErrEdgeOrder         = errors.New("ranke.verify: a claim's edges are inlined ascending by id(e) (`V-EORDER`)")
	ErrUnknownTypeClass  = errors.New("ranke.verify: type class is not one of the fixed set")
	ErrRelationDirection = errors.New("ranke.verify: a relation/* edge carries relation_direction 1 or -1, an edge of any other class 0")
	// ErrDeleteMarkNoTarget: a mark that names nothing explains no gap (`R-DGAP`).
	ErrDeleteMarkNoTarget = errors.New("ranke.verify: a contribution/delete claim must carry a contribution/delete edge naming its target")
	ErrTimestampForm      = errors.New("ranke: a timestamp must be RFC 3339, UTC, at nanosecond precision (2026-01-05T12:00:00.000000000Z)")
	ErrDatedForm          = errors.New("ranke: dated must be an RFC 3339 timestamp or a valid EDTF Level 1 value (`V-DATED`)")
	// ErrArchiveFirstTableHeight: the archive's initial branch table stands on its
	// contributor edge alone, so height 1 is the only value that re-derives.
	ErrArchiveFirstTableHeight = errors.New("ranke.verify: an archive's first branch-table claim must have height 1 (`V-ARCHIVEHEIGHT`)")
	// --- Bookmarks (𝒰_hist, foundation paper §Bookmarks) ---
	errBookmarkNoHead     = errors.New("ranke.Bookmarks.Append: nil head id")
	errBookmarkNoSigner   = errors.New("ranke: no contributor with a signing key to sign a bookmark under")
	errBookmarkNoSeed     = errors.New("ranke.NewBookmarks: a list is keyed on a non-empty seed (`V-IDSEQ`)")
	errBookmarkNilSlot    = errors.New("ranke: nil id_seq(i,s) slot")
	errBookmarkNoUniverse = errors.New("ranke: a bookmark list needs the Universe holding its 𝒰_hist")
	errBookmarkEncode     = errors.New("ranke.SignBookmark: encode bookmark")
	errBookmarkSeedGen    = errors.New("ranke.MintSeed: generate seed")
	// The shape and authorship a stored record must have to be a bookmark at all.
	errBookmarkForm    = errors.New("ranke: a bookmark's payload is the three-element S([i, s, k]) (`V-BMENV`)")
	errBookmarkHeaders = errors.New("ranke: a bookmark's protected header carries alg and kid alone, its unprotected header nothing (`V-BMENV`)")
	// errBookmarkSlot is the one sanctioned absence: what sits here belongs elsewhere.
	errBookmarkSlot      = errors.New("ranke: a bookmark's own (i, s) key another slot than the one it was fetched at (`V-BMSLOT`)")
	errBookmarkSignature = errors.New("ranke: a bookmark's signature does not verify against the pubkey its kid names (`V-BMSIG`)")
	errBookmarkReference = errors.New("ranke: a bookmark's k does not resolve to a contribution/branches claim (`V-BMREF`)")
	errBookmarkGap       = errors.New("ranke: a bookmark list's present indices must form one contiguous range (`V-BMGAPLESS`)")
	// errBookmarkSlotRead is damage rather than a hole: read as absence it would break
	// the presence monotonicity the O(log n) head search rests on.
	errBookmarkSlotRead     = errors.New("ranke.Bookmarks: the record at id_seq(i,s) is not a bookmark of this list")
	errBookmarkOpen         = errors.New("ranke.OpenBookmarks: the id names no bookmark that opens a list")
	errBookmarkIndexRange   = errors.New("ranke.Bookmarks: index outside the list's range")
	errBookmarkRangeInvalid = errors.New("ranke.Bookmarks.GetBulk: invalid range")
	// ErrUnexplainedGap: a claim's bytes are missing and nothing explains the gap —
	// no copied delete_by on the edge reaching it, no contribution/delete mark against
	// it. Indistinguishable from data loss, which is why it fails.
	errUnexplainedGap = errors.New("ranke.verify: a missing claim with no explained gap (no copied delete_by, no contribution/delete mark)")
)

// wrapErr attaches an optional detail string and/or an optional cause to a
// sentinel without building any string at construction — the message is
// composed lazily in Error(). errors.Is/As match the sentinel and, when
// present, the cause (via Unwrap). It replaces fmt.Errorf across the
// package: withDetail for %s/%q/%d-style detail, wrap for %w-style cause,
// wrapDetail for both.
type wrapErr struct {
	sentinel error
	detail   string
	cause    error
}

func (e *wrapErr) Error() string {
	msg := e.sentinel.Error()
	if e.detail != "" {
		msg += ": " + e.detail
	}
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}
	return msg
}

// Unwrap exposes the sentinel and, when present, the cause, so errors.Is
// and errors.As match either.
func (e *wrapErr) Unwrap() []error {
	if e.cause == nil {
		return []error{e.sentinel}
	}
	return []error{e.sentinel, e.cause}
}

// WithDetail returns sentinel with detail appended lazily.
func WithDetail(sentinel error, detail string) error {
	return &wrapErr{sentinel: sentinel, detail: detail}
}

// alsoErr reads as its primary sentinel and matches a second one as well, for a
// condition two rules both name. errors.Join would do the matching and render both
// messages on separate lines; a client shows this one, so the message stays the
// primary's.
type alsoErr struct {
	primary error
	also    error
}

func (e *alsoErr) Error() string   { return e.primary.Error() }
func (e *alsoErr) Unwrap() []error { return []error{e.primary, e.also} }

// alsoMatches returns primary, matchable as also too.
func alsoMatches(primary, also error) error {
	return &alsoErr{primary: primary, also: also}
}

// Wrap returns sentinel wrapping cause; both are matchable via errors.Is.
func Wrap(sentinel, cause error) error {
	return &wrapErr{sentinel: sentinel, cause: cause}
}

// WrapDetail returns sentinel with detail and a wrapped cause.
func WrapDetail(sentinel error, detail string, cause error) error {
	return &wrapErr{sentinel: sentinel, detail: detail, cause: cause}
}
