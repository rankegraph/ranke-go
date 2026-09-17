package ranke

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A real id, so the codec's ParseId path runs on something that parses.
const testQueryId = "bciqmi5j5hnobbrzqrcqeeodhegnb3o4rozbeh24woow3sxdxbzb2qsi"

// secondTestQueryId is another real id, for the set form `claim` also takes (`R-QANCHOR`).
const secondTestQueryId = "bciqd3vsbjnbihsf2xbhlkgfmaqkxqjc7azsbm3cfnvhlqyfmbdmmyoy"

// secondQueryId is secondTestQueryId parsed, for a Query built in Go.
func secondQueryId(t *testing.T) Id {
	t.Helper()
	id, err := ParseId(secondTestQueryId)
	require.NoError(t, err)
	return id
}

// Every document here was also run through the published rql.schema.json with ajv and
// accepted. A case added here belongs there too, or the two drift apart.
func TestDecodeQueryAcceptsSchemaValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
	}{
		{"universe with head", `{"select":{"branch":"$universe","head":"` + testQueryId + `"}}`},
		{"branch scan", `{"select":{"branch":"main"}}`},
		{"anchored path", `{"select":{"branch":"main","claim":"` + testQueryId + `","path":[{"edges":["derivation/*"],"max":3}]}}`},
		{"nested where", `{"select":{"branch":"main"},"where":{"and":[{"or":[{"field":"type","test":{"glob":"source/*"}},{"field":"type","test":{"glob":"derivation/*"}}]},{"not":{"field":"height","test":{"eq":0}}}]}}`},
		{"content cap", `{"select":{"branch":"main"},"output":{"content":{"max":4096,"overflow":"cutoff"},"encoding":"cbor"}}`},
		{"order and limit", `{"select":{"branch":"main"},"order":[{"field":"height","compare":"numeric","dir":"desc"}],"limit":{"results":200,"time":"5s"}}`},
		{"anchor set", `{"select":{"branch":"main","claim":["` + testQueryId + `","` + secondTestQueryId + `"],"path":[]}}`},
		{"empty path", `{"select":{"branch":"main","claim":"` + testQueryId + `","path":[]}}`},
		{"min zero carries the start", `{"select":{"branch":"main","path":[{"min":0,"nodes":["source/*"]}]}}`},
		{"unbounded step", `{"select":{"branch":"main","path":[{"edges":["derivation/*"],"max":0}]}}`},
		{"execution", `{"select":{"branch":"main"},"execution":{"layer":"neo4j","report":"debug"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeQuery([]byte(tc.doc))
			require.NoError(t, err)
		})
	}
}

// Refused by ajv against the published schema too. want pins each case to a reason,
// not merely to failure.
func TestDecodeQueryRefusesSchemaInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want error
	}{
		{"empty branch", `{"select":{"branch":""}}`, ErrQueryNoScope},
		{"universe without head", `{"select":{"branch":"$universe"}}`, ErrQueryNoHead},
		{"where holds two forms", `{"select":{"branch":"m"},"where":{"and":[{"field":"a","test":{"eq":1}}],"or":[{"field":"b","test":{"eq":2}}]}}`, ErrQueryWhereForm},
		{"where holds no form", `{"select":{"branch":"m"},"where":{}}`, ErrQueryWhereForm},
		{"empty and", `{"select":{"branch":"m"},"where":{"and":[]}}`, ErrQueryWhereForm},
		{"leaf without a test", `{"select":{"branch":"m"},"where":{"field":"type"}}`, ErrQueryWhereForm},
		{"comparison holds two operators", `{"select":{"branch":"m"},"where":{"field":"type","test":{"eq":"a","glob":"b"}}}`, ErrQueryComparisonForm},
		{"comparison holds no operator", `{"select":{"branch":"m"},"where":{"field":"type","test":{}}}`, ErrQueryComparisonForm},
		{"unknown operator", `{"select":{"branch":"m"},"where":{"field":"type","test":{"matches":"a"}}}`, ErrQueryComparisonForm},
		{"unknown direction", `{"select":{"branch":"m","path":[{"dir":"sideways"}]}}`, ErrQueryEnum},
		{"unknown shape", `{"select":{"branch":"m"},"output":{"shape":"tree"}}`, ErrQueryEnum},
		{"unknown overflow", `{"select":{"branch":"m"},"output":{"content":{"max":1,"overflow":"wrap"}}}`, ErrQueryEnum},
		{"native is not a wire encoding", `{"select":{"branch":"m"},"output":{"encoding":"native"}}`, ErrQueryEnum},
		{"warn is not a wire report", `{"select":{"branch":"m"},"execution":{"report":"warn"}}`, ErrQueryEnum},
		{"unknown top-level field", `{"select":{"branch":"m"},"selct":{}}`, errDecodeQuery},
		{"unknown step field", `{"select":{"branch":"m","path":[{"hops":2}]}}`, errDecodeQuery},
		{"malformed id", `{"select":{"branch":"$universe","head":"NOT-A-MULTIBASE-ID"}}`, errDecodeQuery},
		{"anchor set repeats an id", `{"select":{"branch":"m","claim":["` + testQueryId + `","` + testQueryId + `"]}}`, ErrQueryAnchorSet},
		{"anchor set names none", `{"select":{"branch":"m","claim":[]}}`, ErrQueryAnchorSet},
		{"anchor is neither an id nor a set", `{"select":{"branch":"m","claim":{"id":"` + testQueryId + `"}}}`, ErrQueryAnchorSet},
		{"malformed duration", `{"select":{"branch":"m"},"limit":{"time":"5 seconds"}}`, errDecodeQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeQuery([]byte(tc.doc))
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// A loss in either direction leaves a client and the engine disagreeing about the read.
func TestQueryRoundTripsThroughTheWire(t *testing.T) {
	head, err := ParseId(testQueryId)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		q    Query
	}{
		{"scan", Query{Select: Select{Branch: "main"}}},
		{"universe", Query{Select: Select{Branch: BranchUniverse, Head: head}}},
		{"anchored path", Query{Select: Select{Branch: "main", Claim: Anchors(head),
			Path: []PathStep{{Edges: []string{"derivation/*"}, Max: 3}}}}},
		{"anchor set", Query{Select: Select{Branch: "main", Claim: Anchors(head, secondQueryId(t)),
			Path: []PathStep{{Edges: []string{"derivation/*"}}}}}},
		{"empty path", Query{Select: Select{Branch: "main", Claim: Anchors(head),
			Path: []PathStep{}}}},
		{"min zero", Query{Select: Select{Branch: "main",
			Path: []PathStep{{Min: Hops(0), Nodes: []string{"source/*"}}}}}},
		{"dir uses", Query{Select: Select{Branch: "main",
			Path: []PathStep{{Dir: DirUses, Edges: []string{"derivation/*"}}}}}},
		{"where tree", Query{Select: Select{Branch: "main"}, Where: &Where{And: []Where{
			{Or: []Where{
				{Field: "type", Test: &Comparison{Glob: "source/*"}},
				{Field: "type", Test: &Comparison{Eq: "entity/person"}},
			}},
			{Not: &Where{Field: "height", Test: &Comparison{Ge: float64(2)}}},
		}}}},
		{"in set", Query{Select: Select{Branch: "main"},
			Where: &Where{Field: "type", Test: &Comparison{In: []any{"source/note", "entity/org"}}}}},
		{"output axes", Query{Select: Select{Branch: "main"}, Output: Output{
			Shape: ShapePath, Detail: DetailClaims, Form: FormOriginal, Encoding: ResultCBOR,
			Content: &OutputContent{Max: 4096, Overflow: OverflowCutoff}}}},
		{"order", Query{Select: Select{Branch: "main"},
			Order: []OrderKey{{Field: "height", Compare: CompareNumeric, Dir: SortDesc}}}},
		{"limit", Query{Select: Select{Branch: "main"}, Limit: Limit{Results: 200, Time: 5 * time.Second}}},
		{"execution", Query{Select: Select{Branch: "main"},
			Execution: Execution{Layer: "neo4j", Report: ReportTrace}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := EncodeQuery(tc.q)
			require.NoError(t, err)

			back, err := DecodeQuery(encoded)
			require.NoError(t, err)
			require.Equal(t, tc.q, back)

			// Encoding the decoded query again lands on the same bytes, so the wire
			// form is a fixpoint and a client may cache or compare it.
			again, err := EncodeQuery(back)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(again))
		})
	}
}

// An omitempty tag would drop a false or a 0, both of which a caller meant.
func TestComparisonKeepsFalsyValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"false", false},
		{"zero", float64(0)},
		{"empty string", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := Query{Select: Select{Branch: "main"},
				Where: &Where{Field: "flag", Test: &Comparison{Eq: tc.value}}}

			encoded, err := EncodeQuery(q)
			require.NoError(t, err)

			var probe map[string]any
			require.NoError(t, json.Unmarshal(encoded, &probe))
			where, ok := probe["where"].(map[string]any)
			require.True(t, ok, "where survives encoding")
			test, ok := where["test"].(map[string]any)
			require.True(t, ok, "test survives encoding")
			require.Contains(t, test, "eq", "the operator is on the wire, whatever its value")

			back, err := DecodeQuery(encoded)
			require.NoError(t, err)
			require.Equal(t, q, back)
		})
	}
}

// Native asks for Go objects, so it leaves no trace on the wire while staying valid in Go.
func TestEncodeQueryOmitsNative(t *testing.T) {
	q := Query{Select: Select{Branch: "main"}, Output: Output{Encoding: ResultNative}}
	require.NoError(t, ValidateQuery(q))

	encoded, err := EncodeQuery(q)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "native")
	require.NotContains(t, string(encoded), "encoding")
}

// The one rule the schema cannot state, comparing two sibling values.
func TestValidateQueryRefusesUnhoppableStep(t *testing.T) {
	q := Query{Select: Select{Branch: "main", Path: []PathStep{{Min: Hops(4), Max: 2}}}}
	require.ErrorIs(t, ValidateQuery(q), ErrQueryHops)

	// A Max of zero leaves the step unbounded, so no Min is above it.
	unbounded := Query{Select: Select{Branch: "main", Path: []PathStep{{Min: Hops(4), Max: 0}}}}
	require.NoError(t, ValidateQuery(unbounded))
}

// A query built in Go and the same query off the wire must be judged alike.
func TestValidateQueryIsOneVerdict(t *testing.T) {
	built := Query{Select: Select{Branch: BranchUniverse}}
	require.ErrorIs(t, ValidateQuery(built), ErrQueryNoHead)

	_, err := DecodeQuery([]byte(`{"select":{"branch":"$universe"}}`))
	require.ErrorIs(t, err, ErrQueryNoHead)
}

// TestValidateQueryRefusesNegativeHops: a hop count is a count, and the schema bounds
// both at zero. Clamping a negative to zero instead accepted a query the schema
// refuses, and any implementation reading ranke-go rather than the schema inherited
// that — so the refusal is here, where one verdict is reached.
func TestValidateQueryRefusesNegativeHops(t *testing.T) {
	q := Query{Select: Select{Branch: "main", Path: []PathStep{{Min: Hops(-1)}}}}
	require.ErrorIs(t, ValidateQuery(q), ErrQueryHops)

	q = Query{Select: Select{Branch: "main", Path: []PathStep{{Max: -1}}}}
	require.ErrorIs(t, ValidateQuery(q), ErrQueryHops)

	// Zero is admissible at both ends: Min 0 carries the starting set through, and
	// Max 0 leaves the step unbounded.
	ok := Query{Select: Select{Branch: "main", Path: []PathStep{{Min: Hops(0), Max: 0}}}}
	require.NoError(t, ValidateQuery(ok))
}

// TestMinHopsStatesItsValue: MinHops defaults an absent Min to one hop and otherwise
// states what it was given, validation having already refused a negative.
func TestMinHopsStatesItsValue(t *testing.T) {
	require.Equal(t, 1, PathStep{}.MinHops(), "absent means one hop")
	require.Equal(t, 0, PathStep{Min: Hops(0)}.MinHops())
	require.Equal(t, 3, PathStep{Min: Hops(3)}.MinHops())
}

// TestValidateQueryRefusesNegativeCaps: the schema bounds four values at zero, and
// hops was only two of them. A cap is a count, so a negative one names no read —
// caught here under a bounds sentinel, which a caller switching on the code can tell
// from a value outside an enumerated set.
func TestValidateQueryRefusesNegativeCaps(t *testing.T) {
	base := Select{Branch: "main"}

	require.ErrorIs(t, ValidateQuery(Query{Select: base, Limit: Limit{Results: -1}}), ErrQueryBounds)
	require.ErrorIs(t, ValidateQuery(Query{Select: base, Limit: Limit{Time: -time.Second}}), ErrQueryBounds)
	require.ErrorIs(t, ValidateQuery(Query{
		Select: base,
		Output: Output{Content: &OutputContent{Max: -1, Overflow: OverflowOmit}},
	}), ErrQueryBounds)

	// Zero is the unbounded read at every one of them.
	require.NoError(t, ValidateQuery(Query{
		Select: base,
		Limit:  Limit{Results: 0, Time: 0},
		Output: Output{Content: &OutputContent{Max: 0, Overflow: OverflowOmit}},
	}))
}

// TestNegativeHopsMatchBothRules: a negative hop count breaks the step rule and the
// schema's minimum, so it answers to either sentinel. A caller watching traversals
// reads ErrQueryHops; one checking every schema minimum reads ErrQueryBounds; neither
// has to know the other's name.
func TestNegativeHopsMatchBothRules(t *testing.T) {
	base := Select{Branch: "main"}
	for _, step := range []PathStep{{Min: Hops(-1)}, {Max: -1}} {
		err := ValidateQuery(Query{Select: Select{Branch: base.Branch, Path: []PathStep{step}}})
		require.ErrorIs(t, err, ErrQueryHops)
		require.ErrorIs(t, err, ErrQueryBounds)
		// One message, and it is the step rule's — errors.Join would print both.
		require.NotContains(t, err.Error(), "\n")
		require.Contains(t, err.Error(), "hop bounds admit no count")
	}

	// A floor above a bounded ceiling breaks no bound, so it stays the step rule alone.
	overshoot := ValidateQuery(Query{Select: Select{Branch: base.Branch,
		Path: []PathStep{{Min: Hops(3), Max: 1}}}})
	require.ErrorIs(t, overshoot, ErrQueryHops)
	require.NotErrorIs(t, overshoot, ErrQueryBounds)
}

// TestOverflowVocabularyMatchesTheSchema: cutoff or omit, and nothing else. A third
// value, "reference", left a content_hash stub where content had been — which changes
// the fields a claim carries, so `R-QCONTENT` dropped it.
func TestOverflowVocabularyMatchesTheSchema(t *testing.T) {
	sel := Select{Branch: "main"}
	for _, overflow := range []Overflow{OverflowCutoff, OverflowOmit} {
		q := Query{Select: sel, Output: Output{Content: &OutputContent{Max: 4096, Overflow: overflow}}}
		require.NoError(t, ValidateQuery(q), string(overflow))
	}
	q := Query{Select: sel, Output: Output{Content: &OutputContent{Max: 4096, Overflow: "reference"}}}
	require.ErrorIs(t, ValidateQuery(q), ErrQueryEnum, "reference left the vocabulary")

	// A cap alone is a whole content pair, an absent overflow being omit (`R-QCONTENT`).
	bare := `{"select":{"branch":"main"},"output":{"content":{"max":4096}}}`
	decoded, err := DecodeQuery([]byte(bare))
	require.NoError(t, err, "overflow is optional")
	require.Equal(t, Overflow(""), decoded.Output.Content.Overflow,
		"the default stays unwritten, so the engine applies it")
}

// TestDetailVocabularyMatchesTheSchema: id or claims (`R-QDETAIL`). "graph" asked for
// the closed graph — nodes with only the edges among them — which is a claim cut down
// to the result set, so a client could not tell a claim's edges from the ones that
// survived the cut.
func TestDetailVocabularyMatchesTheSchema(t *testing.T) {
	sel := Select{Branch: "main"}
	for _, detail := range []Detail{"", DetailID, DetailClaims} {
		q := Query{Select: sel, Output: Output{Detail: detail}}
		require.NoError(t, ValidateQuery(q), string(detail))
	}
	q := Query{Select: sel, Output: Output{Detail: "graph"}}
	require.ErrorIs(t, ValidateQuery(q), ErrQueryEnum, "graph left the vocabulary")
}
