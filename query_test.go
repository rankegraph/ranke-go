package ranke

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tests for the RQL reference executor (DefaultQuery over a Universe): forward
// and reverse traversal, filtering, ordering, limiting, and output shaping.

// queryFixture builds root(0) ← a:source(1) ← b:entity(2) in a memory
// Universe and returns it with the three claims.
func queryFixture(t *testing.T) (Universe, Contributor, Claim, Claim) {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	a := srcClaim(t, root, "aardvark")
	b := entityClaim(t, root, "person", "b", a)
	for _, c := range []Claim{root, a, b} {
		require.NoError(t, PutClaim(ctx, u, c))
	}
	return u, root, a, b
}

// drain reads every element the stream emits — the report among them, since that is
// what a generic reader sees (`R-QSTREAM`).
func drain(t *testing.T, rs ResultStream) []QueryResult {
	t.Helper()
	var out []QueryResult
	for rs.Next() {
		out = append(out, rs.Result())
	}
	require.NoError(t, rs.Err())
	require.NoError(t, rs.Close())
	return out
}

// drainResults is the results alone, for a test whose subject is the answer rather
// than the shape of the sequence carrying it. That shape is asserted where it is the
// subject — TestQueryReport and TestReportOffEmitsNoReportElement — so dropping the
// element here hides nothing.
func drainResults(t *testing.T, rs ResultStream) []QueryResult {
	t.Helper()
	var out []QueryResult
	for _, r := range drain(t, rs) {
		if r.Kind != KindReport {
			out = append(out, r)
		}
	}
	return out
}

func idSet(rs []QueryResult) map[string]bool {
	m := map[string]bool{}
	for _, r := range rs {
		m[r.ClaimNative.ID().String()] = true
	}
	return m
}

// TestQueryFullClosure: an empty Path walks the full forward closure from the
// root — here b reaches a (derivation) and root (contributor), and a reaches root.
func TestQueryFullClosure(t *testing.T) {
	u, root, a, b := queryFixture(t)
	rs, err := u.Query(context.Background(), Query{Select: Select{Branch: BranchUniverse, Head: b.ID()}}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := idSet(drain(t, rs))
	require.Equal(t, map[string]bool{
		b.ID().String(): true, a.ID().String(): true, root.ID().String(): true,
	}, got, "full closure reaches b, a, root")
}

// TestQueryPathTypedDepth: a derivation/* step of depth 1 from b, landing on
// source/* nodes, reaches only a — root (a contributor edge) is off-path.
func TestQueryPathTypedDepth(t *testing.T) {
	u, _, a, b := queryFixture(t)
	q := Query{Select: Select{
		Branch: BranchUniverse,
		Head:   b.ID(),
		Claim:  Anchors(b.ID()),
		Path:   []PathStep{{Edges: []string{"derivation/*"}, Max: 1, Nodes: []string{"source/*"}}},
	}}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := idSet(drain(t, rs))
	require.Equal(t, map[string]bool{a.ID().String(): true}, got)
}

// TestQueryWhereType: a Where on type filters the closure to source claims.
func TestQueryWhereType(t *testing.T) {
	u, _, a, b := queryFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID()},
		Where:  &Where{Field: "type", Test: &Comparison{Glob: "source/*"}},
	}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := idSet(drain(t, rs))
	require.Equal(t, map[string]bool{a.ID().String(): true}, got)
}

// TestQueryWhereHeight: height is a first-class queryable field — ge 1 drops
// the height-0 root, keeping a(1) and b(2).
func TestQueryWhereHeight(t *testing.T) {
	u, root, a, b := queryFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID()},
		Where:  &Where{Field: "height", Test: &Comparison{Ge: 1}},
	}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := idSet(drain(t, rs))
	require.Equal(t, map[string]bool{a.ID().String(): true, b.ID().String(): true}, got)
	require.False(t, got[root.ID().String()], "height-0 root excluded")
}

// TestQueryOrderLimit: order by height desc and cap to one → the deepest claim.
func TestQueryOrderLimit(t *testing.T) {
	u, _, _, b := queryFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID()},
		Order:  []OrderKey{{Field: "height", Compare: CompareNumeric, Dir: SortDesc}},
		Limit:  Limit{Results: 1},
	}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := drain(t, rs)
	require.Len(t, got, 1)
	require.True(t, got[0].ClaimNative.ID().Equal(b.ID()), "highest-height claim first")
}

// TestQueryOutputPath: ShapePath returns the route root→claim.
func TestQueryOutputPath(t *testing.T) {
	u, _, a, b := queryFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID(), Claim: Anchors(b.ID()),
			Path: []PathStep{{Edges: []string{"derivation/*"}, Max: 1, Nodes: []string{"source/*"}}}},
		Output: Output{Shape: ShapePath},
	}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := drain(t, rs)
	require.Len(t, got, 1)
	require.Len(t, got[0].PathNative, 2, "route is b → a")
	require.True(t, got[0].PathNative[0].ID().Equal(b.ID()))
	require.True(t, got[0].PathNative[1].ID().Equal(a.ID()))
}

// TestQueryReverseSupported: a byte store serves a reverse step via closure
// inversion (not a refusal) — DirConnections from the head reaches its whole
// closure, both directions, confined to it.
func TestQueryReverseSupported(t *testing.T) {
	u, root, a, b := queryFixture(t)
	q := Query{Select: Select{Branch: BranchUniverse, Head: b.ID(), Claim: Anchors(b.ID()),
		Path: []PathStep{{Min: Hops(0), Dir: DirConnections}}}}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err, "reverse walk is served via closure inversion, not refused")
	require.Equal(t, idsOf(a, b, root), idSet(drain(t, rs)))
}

// TestQueryNoScopeAnchor: this walker enumerates a scope by closing over an
// anchor, so a branch read reaching it without one — Select.Head unset and the
// Scope unresolved — cannot be served. Branch resolution is upstream.
func TestQueryNoScopeAnchor(t *testing.T) {
	u := NewMemoryUniverse()
	_, err := u.Query(context.Background(), Query{Select: Select{Branch: "main"}}, Scope{})
	require.ErrorIs(t, err, ErrQueryNoHead)
}

// TestQueryReport: the report is the stream's final element, tagged as one
// (`R-QSTREAM`), so a reader iterating generically meets it like any other element
// and Report() is a convenience over it rather than a channel beside it.
func TestQueryReport(t *testing.T) {
	u, _, _, b := queryFixture(t)
	rs, err := u.Query(context.Background(), Query{
		Select:    Select{Branch: BranchUniverse, Head: b.ID()},
		Execution: Execution{Report: ReportInfo},
	}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := drain(t, rs)
	require.NotEmpty(t, got)

	last := got[len(got)-1]
	require.Equal(t, KindReport, last.Kind, "the run asked for a report, so it ends with one")
	require.NotNil(t, last.Report)
	// The tag is enough to know what the element holds: no other field is set on it.
	require.Nil(t, last.ClaimId)
	require.Nil(t, last.ClaimNative)
	require.Nil(t, last.ClaimEncoded)
	for _, r := range got[:len(got)-1] {
		require.NotEqual(t, KindReport, r.Kind, "one report, and it is last")
	}

	rep := rs.Report()
	require.Same(t, last.Report, rep, "one report, read two ways")
	require.Equal(t, len(got)-1, rep.Results, "Results counts the results, not itself")
	require.NotEmpty(t, rep.Events, "the report carries an execution log")
	// The native engine logged at least one event, and the log ends with results.
	var sawNative bool
	for _, e := range rep.Events {
		if e.Engine == "native" {
			sawNative = true
		}
	}
	require.True(t, sawNative, "native engine events are recorded")
}

// TestQueryTimeOperandForm is `R-QTIMEOP`: a comparison testing a time takes a
// `V-TIME` timestamp or an EDTF Level 1 value, and every other value is refused. One
// spelling per instant is what keeps a bound off the neighbouring second — where a
// normalising engine would answer, quietly, for the wrong one.
func TestQueryTimeOperandForm(t *testing.T) {
	timeQuery := func(field string, c Comparison) Query {
		return Query{
			Select: Select{Branch: BranchArchive},
			Where:  &Where{Field: field, Test: &c},
		}
	}

	// Accepted: the `V-TIME` form on a timestamp field, EDTF on dated.
	for _, tc := range []struct {
		field string
		value string
	}{
		{"created_at", "2026-01-01T00:00:02.000000000Z"},
		{FieldDeleteBy, "2030-01-01T00:00:00.000000000Z"},
		{"dated", "2014"},
		{"dated", "201X"},
		{"dated", "2014/2016"},
	} {
		require.NoError(t, ValidateQuery(timeQuery(tc.field, Comparison{Ge: tc.value})),
			"%s=%s is one of the two admitted forms", tc.field, tc.value)
	}

	// Refused: every other spelling of the same instant, and a value that is no time.
	for _, value := range []string{
		"2026-01-01T00:00:02Z",      // RFC 3339 without the fractional digits
		"2026-01-01T00:00:02.000Z",  // milliseconds, not nanoseconds
		"2026-01-01T01:00:02+01:00", // the same instant, not in UTC
		"2026-01-01 00:00:02Z",      // a space for the T
		"1767225602",                // epoch seconds
		"yesterday",
	} {
		require.ErrorIs(t, ValidateQuery(timeQuery("created_at", Comparison{Ge: value})),
			ErrQueryTimeOperand, "created_at=%s must be refused", value)
	}

	// A non-string operand carries no form at all.
	require.ErrorIs(t, ValidateQuery(timeQuery("created_at", Comparison{Ge: 1767225602})),
		ErrQueryTimeOperand)

	// Every operator, and every member of an `in` set.
	for _, c := range []Comparison{
		{Eq: "2026-01-01T00:00:02Z"}, {Ne: "2026-01-01T00:00:02Z"},
		{Lt: "2026-01-01T00:00:02Z"}, {Le: "2026-01-01T00:00:02Z"},
		{Gt: "2026-01-01T00:00:02Z"}, {Ge: "2026-01-01T00:00:02Z"},
		{In: []any{"2026-01-01T00:00:02.000000000Z", "2026-01-01T00:00:03Z"}},
		{Glob: "2026-*"},
	} {
		require.ErrorIs(t, ValidateQuery(timeQuery("created_at", c)), ErrQueryTimeOperand)
	}

	// A field no time rule governs keeps taking whatever it took before.
	require.NoError(t, ValidateQuery(timeQuery("encoding", Comparison{Glob: "text/*"})))
	require.NoError(t, ValidateQuery(timeQuery("height", Comparison{Ge: 1})))
}

// TestFormatTimestampIsTheAdmittedForm: the renderer callers reach for produces what
// `R-QTIMEOP` admits, so a Go caller holding an instant has a sanctioned spelling.
func TestFormatTimestampIsTheAdmittedForm(t *testing.T) {
	s := FormatTimestamp(time.Date(2026, 1, 1, 1, 0, 2, 0, time.FixedZone("CET", 3600)))
	require.Equal(t, "2026-01-01T00:00:02.000000000Z", s, "rendered in UTC at fixed width")

	require.NoError(t, ValidateQuery(Query{
		Select: Select{Branch: BranchArchive},
		Where:  &Where{Field: "created_at", Test: &Comparison{Ge: s}},
	}))
}
