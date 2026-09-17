// package: ranke / query
// type:    data
// job:     the declarative read AST (RQL — the paper's §Filtered Reads) and its result/stream shapes
// limits:  types only; the reference executor is DefaultQuery (-> query_default.go)
package ranke

import "time"

// Query is a declarative read (RQL): generate a set of claims (Select), filter it
// (Where), shape each result (Output), then order and bound the read. Its meaning
// is fixed here, whichever layer answers it via Universe.Query.
type Query struct {
	Select    Select
	Where     *Where // nil = no filter
	Output    Output
	Order     []OrderKey // sort keys in priority order; empty = natural (created_at, id)
	Limit     Limit
	Execution Execution
}

// Reserved virtual branch scopes for Select.Branch (the $-target family, spec
// §Access); a real branch name confines to that branch's closure.
const (
	// BranchUniverse is unconfined, privileged access rooted at Select.Claim,
	// which it therefore requires.
	BranchUniverse = "$universe"
	// BranchArchive confines to the whole Ranke-Archive: the closure of the
	// branch-table header. Select.Claim defaults to the current archive head.
	BranchArchive = "$archive"
	// TargetBranches names the branch table itself (§Access), the surface a grant over
	// branch creation is held against. It scopes no read, so Select.Branch rejects it.
	TargetBranches = "$branches"
)

// Select is a generator: Branch is the scope, Head narrows it to one claim's
// closure (`R-QHEAD`), Claim anchors the frontier (`R-QANCHOR`) and Path is the
// traversal. The two empties of Path differ (`R-QSTEPS`): an EMPTY Path takes no step
// and returns the frontier, a NIL one its full outward closure.
type Select struct {
	Branch string // scope: BranchUniverse, BranchArchive, or a branch name
	Head   Id     // narrows the scope to this claim's closure
	Claim  []Id   // anchors the frontier at one claim or a set; nil leaves it unanchored
	Path   []PathStep
}

// Anchors is Select.Claim over the ids given — the frontier `R-QANCHOR` names, whether
// one claim or a set of them.
func Anchors(ids ...Id) []Id { return ids }

// PathStep follows typed edges over a hop range, optionally constraining endpoint
// node types. Entries are globs over "class/sub"; a leading "-" excludes.
type PathStep struct {
	Edges []string
	Dir   Direction // default DirProvenance
	Min   *int      // min hops; nil means 1. Hops(0) includes the step's start.
	Max   int       // max hops; 0 = unbounded for this step
	Nodes []string
}

// Hops is a PathStep.Min value; Hops(0) admits the step's start itself.
func Hops(n int) *int { return &n }

// MinHops is Min with its default of one hop applied. ValidateQuery refuses a negative
// Min and every read passes it, so this states the value it is given.
func (s PathStep) MinHops() int {
	if s.Min == nil {
		return 1
	}
	return *s.Min
}

// Direction is which way a step follows an edge. Provenance (outgoing) is the
// cheap default; Uses/Connections walk backward, native on ReverseWalk backends.
type Direction string

const (
	DirProvenance  Direction = "provenance"  // outgoing (default) — follow each edge to its reference
	DirUses        Direction = "uses"        // incoming — the claims that reference this one
	DirConnections Direction = "connections" // either direction
)

// Where is a boolean tree: exactly one of And, Or, Not or a leaf (Field + Test).
type Where struct {
	And   []Where
	Or    []Where
	Not   *Where
	Field string      // leaf: the field tested
	Test  *Comparison // leaf: the comparison on Field
}

// Comparison tests one field with exactly one operator.
type Comparison struct {
	Eq   any
	Ne   any
	Lt   any
	Le   any
	Gt   any
	Ge   any
	In   []any  // set membership
	Glob string // shell-style wildcard (path.Match)
}

// Output shapes each result along orthogonal axes: Shape, Detail, Form, Content, Encoding.
type Output struct {
	Shape    Shape          // single | path
	Detail   Detail         // id | claims
	Form     Form           // original | materialized
	Content  *OutputContent // content inlined per claim; nil inlines none
	Encoding ResultEncoding // serialized form of each claim (json | cbor)
}

// OutputContent caps the content inlined per claim (`R-QCONTENT`). Max bounds the claim
// rather than each record, spent along the claim's content sequence — its edges' content
// in S(v)'s order, then the node's — and what arrives is a prefix of it. Max 0 inlines
// in full, as a zero bound elsewhere in a query means unbounded.
type OutputContent struct {
	Max      int      // cap in bytes for the whole claim; 0 inlines in full
	Overflow Overflow // where the prefix ends at the cap; empty is OverflowOmit
}

// Overflow is where a claim's inlined content ends once Max is reached. A claim keeps
// every field it carries either way, so no value stands in for content left out.
type Overflow string

const (
	OverflowCutoff Overflow = "cutoff" // inline the bytes up to the cap, ending inside a value
	OverflowOmit   Overflow = "omit"   // inline whole values only (default; empty == omit)
)

// Form is whether a claim is returned as written (the id-defining bytes) or
// resolved via its diff chain.
type Form string

const (
	FormMaterialized Form = "materialized" // resolved via the diff chain (default; empty == materialized)
	FormOriginal     Form = "original"     // as written (the stored/canonical claim)
)

// ResultEncoding is the form a result is returned in, carrying the same
// information whichever it is.
type ResultEncoding string

const (
	ResultNative ResultEncoding = "native" // Go objects in ClaimNative/PathNative (default; empty == native)
	ResultJSON   ResultEncoding = "json"   // Encoded holds text, content base64
	ResultCBOR   ResultEncoding = "cbor"   // Encoded holds the stored canonical CBOR, verbatim
)

// Shape is whether each result is a single item or a path (the chain to it).
type Shape string

const (
	ShapeSingle Shape = "single" // individual claims (default; empty == single)
	ShapePath   Shape = "path"   // the route to each claim
)

// Detail is how much each element carries (`R-QDETAIL`).
type Detail string

const (
	DetailID     Detail = "id"     // identities only
	DetailClaims Detail = "claims" // the serialized claim, as Form and Content shape it (default; empty == claims)
	// DetailEnvelope is the stored record copied out: the bytes whose hash is the id,
	// carrying the signature over them (`R-QCANON`). Form and Content do not apply,
	// and it is CBOR alone — the bytes are what they are.
	DetailEnvelope Detail = "envelope"
)

// OrderKey is one sort key; keys apply in priority order, ties by (created_at, id).
type OrderKey struct {
	Field   string
	Compare Collation // how values compare; empty = lexical
	Dir     SortDir   // asc | desc; empty = asc
}

// Collation is how ordered values compare.
type Collation string

const (
	CompareLexical  Collation = "lexical"  // string comparison (default)
	CompareNumeric  Collation = "numeric"  // numeric comparison
	CompareTemporal Collation = "temporal" // by span midpoint, ms — a timestamp and an EDTF `dated` on one axis (`R-QTEMPORAL`)
)

// SortDir is a sort direction.
type SortDir string

const (
	SortAsc  SortDir = "asc"  // ascending (default)
	SortDesc SortDir = "desc" // descending
)

// Limit bounds a read.
type Limit struct {
	Results int           // max claims; 0 = unbounded
	Time    time.Duration // execution budget; 0 = none
}

// Execution selects where the query runs and how deeply it reports on itself.
type Execution struct {
	Layer string // pin to one named storage layer; empty = the backend chooses
	// Report is the execution-report verbosity threshold (see ReportLevel); when
	// set, the stream carries a QueryReport (ResultStream.Report).
	Report ReportLevel
}

// Scope is the branch context a query runs in — the Archive's resolution of
// q.Select.Branch, Head confining to its closure and the rest pruning natively.
type Scope struct {
	Head     Id     // closure anchor; nil = unconfined ($universe)
	Branch   string // resolved branch name — a native prune key
	Height   uint64 // branch-head height
	Revision int    // archive spine revision (_br)
}

// ResultStream streams a query's elements one at a time, in the query's order:
// its results, then the report where one was asked for (`R-QSTREAM`).
type ResultStream interface {
	// Next advances to the next element, returning false at end of stream or error.
	Next() bool
	// Result returns the current element (valid after Next returned true). A reported
	// run ends with a KindReport element, which is the report itself.
	Result() QueryResult
	// Report returns the report the stream's last element carries, once Next has
	// returned false — a convenience over that element rather than a channel beside
	// it, so the two cannot disagree. nil when Execution.Report asked for none.
	Report() *QueryReport
	// Err returns the first error that stopped the stream, if any.
	Err() error
	// Close releases the stream's resources.
	Close() error
}

// QueryResult is one element of a result stream: a reached claim shaped per Output,
// or the report a reported run ends with. Kind names the one field carrying the
// payload, so a caller switches once and streams that field, and a reader learns
// what an element holds without inspecting it (`R-QSTREAM`).
type QueryResult struct {
	Kind         ResultKind
	ClaimId      Id
	PathId       []Id
	ClaimNative  Claim
	PathNative   []Claim
	ClaimEncoded []byte
	PathEncoded  [][]byte
	Report       *QueryReport // KindReport alone: the run's execution log
}

// ResultKind names the QueryResult field an element's payload is in: for a result,
// the product of Output.Shape (single | path) and Output.Detail/Encoding (id |
// native | encoded); for the report a reported run ends with, KindReport.
type ResultKind string

const (
	KindClaimId      ResultKind = "claim_id"
	KindPathId       ResultKind = "path_id"
	KindClaimNative  ResultKind = "claim_native"
	KindPathNative   ResultKind = "path_native"
	KindClaimEncoded ResultKind = "claim_encoded"
	KindPathEncoded  ResultKind = "path_encoded"
	// KindClaimEnvelope / KindPathEnvelope tag stored bytes copied out under
	// DetailEnvelope, apart from an encoded serialized claim, since a reader parses
	// the one and hands the other on as bytes (`R-QSTREAM`).
	KindClaimEnvelope ResultKind = "claim_envelope"
	KindPathEnvelope  ResultKind = "path_envelope"
	// KindReport tags the final element of a run that asked for a report
	// (`R-QREPORT`). The value is the one ranke-ts discriminates its framed report
	// record under, the wire having carried the report in band all along.
	KindReport ResultKind = "report"
)
