package ranke

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tests for the RQL execution report (query_report.go): the structured event
// log a query collects when Execution.Report is set, and the zero-overhead
// no-op path when it isn't.

// opsOf collects the Op of every event, and indexes events by Op.
func opsOf(rep *QueryReport) map[string]QueryEvent {
	m := map[string]QueryEvent{}
	for _, e := range rep.Events {
		m[e.Op] = e
	}
	return m
}

// TestReportRecordsExecutionLog: a reported query produces a structured,
// engine-attributed, ordered log covering the reference engine's stages.
func TestReportRecordsExecutionLog(t *testing.T) {
	u, _, a, b := queryFixture(t) // root(0) ← a:source(1) ← b:entity(2)
	rs, err := u.Query(context.Background(), Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID(), Claim: Anchors(b.ID()),
			Path: []PathStep{{Edges: []string{"derivation/*"}, Max: 1, Nodes: []string{"source/*"}}}},
		Execution: Execution{Report: ReportTrace},
	}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := drain(t, rs)
	require.Len(t, got, 2, "the one result, then the report the run asked for")
	require.True(t, got[0].ClaimNative.ID().Equal(a.ID()))
	require.Equal(t, KindReport, got[1].Kind)

	rep := rs.Report()
	require.NotNil(t, rep)
	require.Same(t, got[1].Report, rep, "Report reads the element, so there is one report")
	require.False(t, rep.StartedAt.IsZero(), "start time stamped")
	require.Equal(t, 1, rep.Results)
	require.NotEmpty(t, rep.Events)

	by := opsOf(rep)
	for _, op := range []string{"select", "load-start", "step", "filter", "sort", "results"} {
		e, ok := by[op]
		require.True(t, ok, "event %q present", op)
		require.Equal(t, "native", e.Engine, "engine on %q", op)
	}
	// The traversal step is timed and carries structured attrs.
	require.Equal(t, ReportInfo, by["step"].Level)
	require.NotNil(t, by["step"].Attrs["reached"])
	// The filter records how many candidates survived.
	require.NotNil(t, by["filter"].Attrs["kept"])
	// Events are ordered by their offset from start.
	for i := 1; i < len(rep.Events); i++ {
		require.GreaterOrEqual(t, rep.Events[i].At, rep.Events[i-1].At, "events in time order")
	}
}

// TestReportLevelThreshold: the level is a verbosity threshold — Info records
// the high-level stages but not per-claim fetches; Trace adds them.
func TestReportLevelThreshold(t *testing.T) {
	u, _, _, b := queryFixture(t)
	fetchEvents := func(level ReportLevel) int {
		rs, err := u.Query(context.Background(), Query{
			Select:    Select{Branch: BranchUniverse, Head: b.ID()},
			Execution: Execution{Report: level},
		}, Scope{Branch: BranchUniverse})
		require.NoError(t, err)
		_ = drain(t, rs)
		n := 0
		for _, e := range rs.Report().Events {
			if e.Op == "fetch" {
				n++
			}
		}
		return n
	}
	require.Zero(t, fetchEvents(ReportInfo), "Info omits per-claim fetch (Trace) events")
	require.Positive(t, fetchEvents(ReportTrace), "Trace records per-claim fetch events")
}

// TestReportOffEmitsNoReportElement: `R-QREPORT` puts a report in the stream when,
// and only when, one was asked for — so an unreported run ends on a result, and the
// collector path is a no-op behind it.
func TestReportOffEmitsNoReportElement(t *testing.T) {
	u, _, _, b := queryFixture(t)
	rs, err := u.Query(context.Background(), Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID()},
	}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := drain(t, rs)
	require.NotEmpty(t, got, "the closure is not empty, so this says something")
	for i, r := range got {
		require.NotEqual(t, KindReport, r.Kind, "element %d is a result", i)
	}
	require.Nil(t, rs.Report(), "no report when Execution.Report is unset")
}

// TestReportFilterCounts: the filter event reports input vs kept, so a selective
// Where is visible in the log.
func TestReportFilterCounts(t *testing.T) {
	u, _, a, b := queryFixture(t)
	rs, err := u.Query(context.Background(), Query{
		Select:    Select{Branch: BranchUniverse, Head: b.ID()}, // full closure: root, a, b
		Where:     &Where{Field: "type", Test: &Comparison{Glob: "source/*"}},
		Execution: Execution{Report: ReportTrace},
	}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := drain(t, rs)
	require.Len(t, got, 2, "the surviving result, then the report")
	require.True(t, got[0].ClaimNative.ID().Equal(a.ID()))

	f := opsOf(rs.Report())["filter"]
	require.Equal(t, 3, f.Attrs["in"], "closure had 3 claims")
	require.Equal(t, 1, f.Attrs["kept"], "only the source survived the Where")
}

// TestResultKindWireValues: a Kind is a wire tag, not a Go name. Another
// implementation reads a framed sequence by discriminating on these strings —
// ranke-ts on `report` for the record this stream now ends with — so renaming a
// constant here must never rename the value.
func TestResultKindWireValues(t *testing.T) {
	require.Equal(t, "report", string(KindReport))
	require.Equal(t, "claim_id", string(KindClaimId))
	require.Equal(t, "path_id", string(KindPathId))
	require.Equal(t, "claim_native", string(KindClaimNative))
	require.Equal(t, "path_native", string(KindPathNative))
	require.Equal(t, "claim_encoded", string(KindClaimEncoded))
	require.Equal(t, "path_encoded", string(KindPathEncoded))
}

// TestReportCarriesWireNames: a report travels a result sequence as its last record,
// so its keys are wire names. Without tags they were Go field names — capitalised,
// where every other shape on the wire is lower-case — and a duration was a bare
// integer saying nothing of its unit.
func TestReportCarriesWireNames(t *testing.T) {
	report := QueryReport{
		StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Elapsed:   1500 * time.Millisecond,
		Results:   3,
		Truncated: true,
		Events: []QueryEvent{{
			At: 250 * time.Millisecond, Engine: "cypher", Op: "step",
			Level: ReportInfo, Duration: 10 * time.Millisecond, Detail: "MATCH …",
		}},
	}

	raw, err := json.Marshal(report)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	require.Equal(t, "2026-01-02T03:04:05Z", got["started_at"])
	require.Equal(t, float64(1_500_000_000), got["elapsed_ns"], "nanoseconds, as the name says")
	require.Equal(t, float64(3), got["results"])
	require.Equal(t, true, got["truncated"])

	events, ok := got["events"].([]any)
	require.True(t, ok)
	event, ok := events[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(250_000_000), event["at_ns"])
	require.Equal(t, "cypher", event["engine"])
	require.Equal(t, float64(10_000_000), event["duration_ns"])
}

// TestMarshalCBORIsDeterministic: the helper a sequence frames its non-claim payloads
// with. One encoder serves the whole system, so a map's keys order the way a claim's
// do and two runs agree byte for byte.
func TestMarshalCBORIsDeterministic(t *testing.T) {
	// Keys order by their encoded bytes, so "b" precedes "aa" — the rule that makes
	// two implementations agree, and the reason this is not any CBOR encoder.
	raw, err := MarshalCBOR(map[string]string{"aa": "1", "b": "2"})
	require.NoError(t, err)
	require.Equal(t, "a2616261326261616131", hex.EncodeToString(raw))

	again, err := MarshalCBOR(map[string]string{"b": "2", "aa": "1"})
	require.NoError(t, err)
	require.Equal(t, raw, again, "insertion order does not reach the bytes")

	// An id and a route, which is what a result sequence carries under cbor framing.
	id, err := MarshalCBOR("bciqlu6awx6hqdt7kifaubxs5vyrchmadmgrzmf32ts2bb73b6iablli")
	require.NoError(t, err)
	require.Equal(t, byte(0x78), id[0], "a CBOR text string, readable as an id")

	route, err := MarshalCBOR([]string{"bciqa", "bciqb"})
	require.NoError(t, err)
	require.Equal(t, byte(0x82), route[0], "a CBOR array of two")
}

// TestReportTimestampMatchesAcrossEncodings: §Output has both encodings carry the same
// information, so a timestamp is the same text in each. CBOR's default for a time.Time
// is epoch seconds, which gave the report two forms and dropped its sub-second
// precision from one of them.
func TestReportTimestampMatchesAcrossEncodings(t *testing.T) {
	started := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	report := QueryReport{StartedAt: started, Elapsed: time.Second, Results: 1}

	asJSON, err := json.Marshal(report)
	require.NoError(t, err)
	var fromJSON map[string]any
	require.NoError(t, json.Unmarshal(asJSON, &fromJSON))
	require.Equal(t, "2026-01-02T03:04:05.123456789Z", fromJSON["started_at"])

	asCBOR, err := MarshalCBOR(report)
	require.NoError(t, err)
	require.Contains(t, string(asCBOR), "2026-01-02T03:04:05.123456789Z",
		"a CBOR text string, so the nanoseconds survive and the two forms agree")
}

// TestClaimBytesIgnoreTheTimeMode: a claim states created_at as text already, so no
// claim record holds a time.Time and the encoder's time mode cannot reach its bytes.
func TestClaimBytesIgnoreTheTimeMode(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithCreatedAt(time.Date(2026, 6, 2, 3, 4, 5, 123456789, time.UTC)).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)

	raw, err := c.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	require.Contains(t, string(raw), "2026-06-02T03:04:05.123456789Z",
		"created_at is text in the record, whatever the encoder does with a time.Time")
}
