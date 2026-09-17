// package: tests/rql / integration
// type:    tool
// job:     the shared RQL corpus — a broad set covering each axis of the read language (traversal,
// filter, shape, order, bound) for the conformance matrix, plus the small subset the
// timing harness measures
// limits:  queries and their names only; executing them and comparing answers live alongside
// (-> run.go), and the backend rows come from tests/backends
package rql

import (
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/tests/generator"
)

// Branch is the branch the branch-scoped queries confine to.
const Branch = "main"

// NamedQuery pairs a stable name with an RQL query; the name keys both the timing
// report and the cross-backend agreement check, so renaming reads as a new query.
type NamedQuery struct {
	Name string
	Q    ranke.Query
}

// BranchScoped reports whether the query confines to a named branch (resolved via
// tags); a backend without a tag overlay can answer only the unconfined entries.
func (nq NamedQuery) BranchScoped() bool { return nq.Q.Select.Branch != ranke.BranchUniverse }

// Timed is the small fixed set the performance harness measures — one entry per
// broad shape, named from Corpus so a timing and a conformance row are one query.
func Timed(m *generator.Manifest, root ranke.Id) []NamedQuery {
	want := map[string]bool{
		"closure/branch":          true,
		"where/type-glob-source":  true,
		"where/height-ge-2":       true,
		"path/derivation-d3":      true,
		"order/height-desc-limit": true,
		"path/uses-of-sources":    true,
		"closure/universe":        true,
		// A path shape, which lowers through its own per-step pipeline (`R-QCFRONTIER`)
		// and reconstructs a route per endpoint. Without one here the whole shape went
		// untimed, so what its lowering costs was invisible.
		"output/shape-path-multi-edge": true,
	}
	var out []NamedQuery
	for _, nq := range Corpus(m, root) {
		if want[nq.Name] {
			out = append(out, nq)
		}
	}
	return out
}

// Corpus is the broad conformance set: every axis of the read language, each entry
// grounded in a generator corner and matching at least one claim. root is main's head.
func Corpus(m *generator.Manifest, root ranke.Id) []NamedQuery {
	// sel scans the branch scope; path anchors a traversal at the branch head.
	sel := func() ranke.Select { return ranke.Select{Branch: Branch} }
	path := func(steps ...ranke.PathStep) ranke.Select {
		return ranke.Select{Branch: Branch, Claim: ranke.Anchors(root), Path: steps}
	}
	// scanFrom narrows a branch scan to one claim's closure.
	scanFrom := func(head ranke.Id) ranke.Select {
		return ranke.Select{Branch: Branch, Head: head}
	}
	// pathFrom anchors a traversal at a claim carrying the edges the steps follow.
	pathFrom := func(claim ranke.Id, steps ...ranke.PathStep) ranke.Select {
		return ranke.Select{Branch: Branch, Claim: ranke.Anchors(claim), Path: steps}
	}
	// archivePath spans every branch, for endpoints the generator may commit anywhere.
	archivePath := func(steps ...ranke.PathStep) ranke.Select {
		return ranke.Select{Branch: ranke.BranchArchive, Claim: ranke.Anchors(m.Head), Path: steps}
	}
	// unanchored names no start, so the steps match anywhere in the closure.
	unanchored := func(steps ...ranke.PathStep) ranke.Select {
		return ranke.Select{Branch: ranke.BranchArchive, Path: steps}
	}
	// A mid-graph instant: the clock starts at Base and advances Step per claim. Spelled
	// as `V-TIME` fixes it, the only form a time comparison takes (`R-QTIMEOP`) —
	// RFC3339Nano would drop the trailing zeros and name no valid operand.
	midpoint := ranke.FormatTimestamp(m.Spec.Base.Add(time.Duration(m.Spec.Size/2) * m.Spec.Step))

	return []NamedQuery{
		// ── traversal: the whole closure, in each of the three scopes ────────
		{"closure/branch", ranke.Query{Select: sel()}},
		{"closure/universe", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchUniverse, Head: m.Head},
		}},
		// $archive is the branch-table header's closure, reaching the spine and every
		// branch; its root defaults to the archive head, so Claim stays unset.
		{"closure/archive", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive},
		}},
		{"archive/where-branch-tables", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "contribution/branches"}},
		}},

		// ── the walk's starting point ────────────────────────────────────────
		// Rooted mid-branch: the walk starts there and the branch confines it, so a
		// lowering that scans branch membership and ignores the root over-returns.
		{"select/root-mid-branch", ranke.Query{Select: scanFrom(m.DiffChainHead)}},
		{"select/root-mid-branch-path", ranke.Query{
			Select: ranke.Select{Branch: Branch, Claim: ranke.Anchors(m.DiffChainHead),
				Path: []ranke.PathStep{{Edges: []string{"contribution/*"}, Max: 2}}},
		}},
		{"select/root-mid-archive", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive, Head: m.DiffChainHead},
		}},
		// A path-less anchor reads the anchor's closure (`R-QANCHOR`, `R-QSTEPS`). The
		// twin spells the same read with its zero step, holding both shapes to one answer.
		{"select/anchored-scan", ranke.Query{
			Select: ranke.Select{Branch: Branch, Claim: ranke.Anchors(m.DiffChainHead)},
		}},
		{"select/anchored-scan-path", ranke.Query{
			Select: pathFrom(m.DiffChainHead, ranke.PathStep{Min: ranke.Hops(0)}),
		}},
		// A set anchor with an empty path returns exactly the claims it names and
		// nothing they cite — the read a client makes to resolve `height` before
		// signing (`R-QANCHOR`, `R-QSTEPS`).
		// $archive spans every branch, since the generator commits an entity wherever
		// it likes and a set anchor must name claims the scope holds.
		{"select/anchor-set-no-step", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive,
				Claim: ranke.Anchors(m.Entities[0], m.Entities[1]), Path: []ranke.PathStep{}},
		}},
		// The same anchors with a step: every frontier member expands, so a lowering
		// that pins only the first under-returns.
		{"select/anchor-set-step", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive,
				Claim: ranke.Anchors(m.Entities[0], m.Entities[1]),
				Path:  []ranke.PathStep{{Min: ranke.Hops(0), Max: 1}}},
		}},
		// One anchor with an empty path is the claim itself, where the same anchor
		// path-less is its whole closure — the two empties of `path` (`R-QSTEPS`).
		{"select/anchor-no-step", ranke.Query{
			Select: ranke.Select{Branch: Branch,
				Claim: ranke.Anchors(m.DiffChainHead), Path: []ranke.PathStep{}},
		}},

		// ── traversal: unanchored, matched wherever it occurs in the closure ──
		{"unanchored/relation-to-entity", ranke.Query{Select: unanchored(
			ranke.PathStep{Dir: ranke.DirConnections, Edges: []string{"relation/*"},
				Nodes: []string{"entity/person"}})}},
		{"unanchored/derivation-to-source", ranke.Query{Select: unanchored(
			ranke.PathStep{Edges: []string{"derivation/*"}, Nodes: []string{"source/*"}})}},
		{"unanchored/uses-of-sources", ranke.Query{Select: unanchored(
			ranke.PathStep{Min: ranke.Hops(0), Nodes: []string{"source/*"}},
			ranke.PathStep{Dir: ranke.DirUses, Edges: []string{"derivation/*"}})}},
		{"unanchored/shape-path", ranke.Query{
			Select: unanchored(ranke.PathStep{Edges: []string{"relation/*"}, Max: 1}),
			Output: ranke.Output{Shape: ranke.ShapePath}}},

		// ── traversal: typed edges to a bounded depth ───────────────────────
		// Anchored at the diff chain's head, a derivation carrying derivation/* edges.
		{"path/derivation-d1", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"derivation/*"}, Max: 1})}},
		{"path/derivation-d3", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"derivation/*"}, Max: 3})}},
		{"path/derivation-unbounded", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"derivation/*"}})}},
		{"path/contribution-d2", ranke.Query{Select: path(
			ranke.PathStep{Edges: []string{"contribution/*"}, Max: 2})}},
		{"path/edges-exclude-contribution", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"-contribution/*"}, Max: 3})}},

		// ── traversal: several patterns in one type list ─────────────────────
		{"path/edges-multi", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"derivation/*", "contribution/contributor"}, Max: 2})}},
		{"path/nodes-multi", ranke.Query{Select: archivePath(
			ranke.PathStep{Nodes: []string{"source/*", "entity/person"}})}},
		{"path/nodes-multi-mixed", ranke.Query{Select: archivePath(
			ranke.PathStep{Nodes: []string{"source/*", "derivation/*", "-source/blob"}})}},
		{"output/shape-path-multi-edge", ranke.Query{
			Select: pathFrom(m.DiffChainHead,
				ranke.PathStep{Edges: []string{"derivation/*", "contribution/diff"}, Max: 3}),
			Output: ranke.Output{Shape: ranke.ShapePath}}},

		// ── traversal: endpoint node constraints ────────────────────────────
		// A derivation-only step cannot leave a contribution/head root.
		{"path/nodes-source", ranke.Query{Select: path(
			ranke.PathStep{Nodes: []string{"derivation/*"}},
			ranke.PathStep{Edges: []string{"derivation/*"}, Nodes: []string{"source/*"}})}},
		// A derivation's cited sources are sources, so this exclusion keeps them.
		{"path/nodes-exclude-entity", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"derivation/*"}, Nodes: []string{"-entity/*"}})}},
		{"path/chain-entity-then-source", ranke.Query{Select: archivePath(
			ranke.PathStep{Nodes: []string{"entity/person"}},
			ranke.PathStep{Edges: []string{"derivation/*"}, Nodes: []string{"source/*"}})}},

		// ── traversal: direction (the reverse-walk paths) ────────────────────
		// Forward to the sources, then backward to the derivations citing them, so the
		// reverse path runs; referrers sit above, hence the $archive head's reach.
		{"path/uses-of-sources", ranke.Query{Select: archivePath(
			ranke.PathStep{Min: ranke.Hops(0), Nodes: []string{"source/*"}},
			ranke.PathStep{Dir: ranke.DirUses, Edges: []string{"derivation/*"}, Nodes: []string{"derivation/*"}})}},
		// The same walk confined to one branch, so every row runs a reverse step whose
		// referrers the scope may exclude (`R-QCSCOPE`). Its $archive twin above cannot:
		// under $archive nothing is out of scope, so the confinement goes unexercised.
		{"path/uses-of-sources-branch", ranke.Query{Select: ranke.Select{Branch: Branch,
			Path: []ranke.PathStep{
				{Min: ranke.Hops(0), Nodes: []string{"source/*"}},
				{Dir: ranke.DirUses, Edges: []string{"derivation/*"}, Nodes: []string{"derivation/*"}}}}}},
		{"path/connections-relation", ranke.Query{Select: archivePath(
			ranke.PathStep{Nodes: []string{"entity/person"}},
			ranke.PathStep{Dir: ranke.DirConnections, Edges: []string{"relation/*"}, Max: 2})}},
		{"path/uses-of-entities", ranke.Query{Select: archivePath(
			ranke.PathStep{Nodes: []string{"entity/person"}},
			ranke.PathStep{Dir: ranke.DirUses, Edges: []string{"relation/*"}})}},

		// ── filter: one operator at a time ──────────────────────────────────
		{"where/type-glob-source", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}}}},
		// Scoped to the archive, whose head reaches every branch's entities.
		{"where/type-eq-entity-person", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Eq: "entity/person"}}}},
		{"where/type-ne-source-note", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "type", Test: &ranke.Comparison{Ne: "source/note"}}}},
		{"where/type-in-set", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "type", Test: &ranke.Comparison{
				In: []any{"source/note", "entity/person", "relation/knows"}}}}},
		{"where/height-ge-2", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "height", Test: &ranke.Comparison{Ge: 2}}}},
		{"where/height-lt-3", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "height", Test: &ranke.Comparison{Lt: 3}}}},
		{"where/content-size-gt-tiny", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "content_size", Test: &ranke.Comparison{Gt: m.Spec.TinyBlobBytes}}}},
		{"where/content-size-le-tiny", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "content_size", Test: &ranke.Comparison{Le: m.Spec.TinyBlobBytes}}}},
		{"where/encoding-glob-text", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "encoding", Test: &ranke.Comparison{Glob: "text/*"}}}},
		{"where/created-at-ge-midpoint", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "created_at", Test: &ranke.Comparison{Ge: midpoint}}}},

		// ── filter: boolean combinators ─────────────────────────────────────
		{"where/and", ranke.Query{Select: sel(), Where: &ranke.Where{And: []ranke.Where{
			{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
			{Field: "height", Test: &ranke.Comparison{Ge: 1}},
		}}}},
		{"where/or", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive},
			Where: &ranke.Where{Or: []ranke.Where{
				{Field: "type", Test: &ranke.Comparison{Eq: "entity/person"}},
				{Field: "type", Test: &ranke.Comparison{Eq: "relation/knows"}},
			}}}},
		{"where/not", ranke.Query{Select: sel(), Where: &ranke.Where{
			Not: &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "contribution/*"}}}}},
		{"where/nested", ranke.Query{Select: sel(), Where: &ranke.Where{And: []ranke.Where{
			{Or: []ranke.Where{
				{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
				{Field: "type", Test: &ranke.Comparison{Glob: "derivation/*"}},
			}},
			{Not: &ranke.Where{Field: "height", Test: &ranke.Comparison{Eq: 0}}},
		}}}},

		// ── shape: how much each result carries ─────────────────────────────
		{"output/detail-id", ranke.Query{Select: sel(),
			Output: ranke.Output{Detail: ranke.DetailID}}},
		{"output/detail-claims", ranke.Query{Select: sel(),
			Output: ranke.Output{Detail: ranke.DetailClaims}}},
		{"output/shape-path", ranke.Query{
			Select: pathFrom(m.DiffChainHead, ranke.PathStep{Edges: []string{"derivation/*"}, Max: 3}),
			Output: ranke.Output{Shape: ranke.ShapePath}}},
		{"output/shape-path-detail-id", ranke.Query{
			Select: pathFrom(m.DiffChainHead, ranke.PathStep{Edges: []string{"derivation/*"}, Max: 3}),
			Output: ranke.Output{Shape: ranke.ShapePath, Detail: ranke.DetailID}}},

		// ── shape: diff-chain resolution (the generator builds a long chain) ─
		{"output/form-materialized", ranke.Query{
			Select: scanFrom(m.DiffChainHead),
			Output: ranke.Output{Form: ranke.FormMaterialized}}},
		{"output/form-original", ranke.Query{
			Select: scanFrom(m.DiffChainHead),
			Output: ranke.Output{Form: ranke.FormOriginal}}},

		// ── order and bound ─────────────────────────────────────────────────
		{"order/height-desc-limit", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortDesc}},
			Limit: ranke.Limit{Results: 20}}},
		{"order/height-asc", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortAsc}}}},
		{"order/created-at-desc", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{{Field: "created_at", Dir: ranke.SortDesc}}}},
		{"order/type-lexical", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{{Field: "type", Compare: ranke.CompareLexical, Dir: ranke.SortAsc}}}},
		{"order/multi-key", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{
				{Field: "type", Compare: ranke.CompareLexical, Dir: ranke.SortAsc},
				{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortDesc},
			}}},
		{"limit/results-5", ranke.Query{Select: sel(), Limit: ranke.Limit{Results: 5}}},

		// ── the report, in band ─────────────────────────────────────────────
		// A reported run ends with the report as a tagged element (`R-QSTREAM`), so
		// every backend must end this sequence the same way. The fingerprint reads the
		// tag, never the timings, so the comparison is the shape and not the clock.
		{"execution/report-ends-the-stream", ranke.Query{Select: sel(),
			Limit:     ranke.Limit{Results: 5},
			Execution: ranke.Execution{Report: ranke.ReportInfo}}},
	}
}
