package neo4j

import (
	"context"
	"testing"

	"github.com/rankegraph/ranke-go"
)

// These tests exercise neo4j's native RQL→Cypher lowering one query at a time,
// each with a full execution report, so the actual Cypher and per-step timing are
// visible (run with `go test -run TestNeo4jQuery -v`). Gated on RANKE_NEO4J_BOLT.

// runReported executes one query with a full trace report and logs the Cypher and
// per-step timings.
func runReported(t *testing.T, u ranke.Universe, name string, q ranke.Query) {
	t.Helper()
	q.Execution.Report = ranke.ReportTrace
	rs, err := u.Query(context.Background(), q, ranke.Scope{Branch: ranke.BranchUniverse})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	var last ranke.QueryResult
	for rs.Next() {
		last = rs.Result()
		if last.Kind != ranke.KindReport {
			n++
		}
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	// The report ends the sequence here as it does natively (`R-QSTREAM`), so a client
	// reading either engine's stream generically meets it the same way.
	if last.Kind != ranke.KindReport {
		t.Fatalf("a reported run ends with its report, got kind %q", last.Kind)
	}
	rep := rs.Report()
	_ = rs.Close()
	if rep == nil {
		t.Fatal("report requested")
	}
	if rep != last.Report {
		t.Fatal("Report() and the element disagree, so there are two reports")
	}
	t.Logf("── %s: %d results in %s ──", name, n, rep.Elapsed)
	for _, e := range rep.Events {
		if e.Engine == "cypher" {
			t.Logf("  [%s/%s] %s", e.Engine, e.Op, e.Detail) // the lowered Cypher
			continue
		}
		t.Logf("  [%s/%s] %s dur=%s", e.Engine, e.Op, e.Level, e.Duration)
	}
}

func TestNeo4jQueryClosure(t *testing.T) {
	u, head := openTestNeo4j(t)
	runReported(t, u, "closure", ranke.Query{Select: ranke.Select{Branch: ranke.BranchUniverse, Head: head}})
}

func TestNeo4jQueryWhereType(t *testing.T) {
	u, head := openTestNeo4j(t)
	runReported(t, u, "where type glob source/*", ranke.Query{
		Select: ranke.Select{Branch: ranke.BranchUniverse, Head: head},
		Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
	})
}

func TestNeo4jQueryWhereHeight(t *testing.T) {
	u, head := openTestNeo4j(t)
	runReported(t, u, "where height ge 2", ranke.Query{
		Select: ranke.Select{Branch: ranke.BranchUniverse, Head: head},
		Where:  &ranke.Where{Field: "height", Test: &ranke.Comparison{Ge: 2}},
	})
}

func TestNeo4jQueryOrderLimit(t *testing.T) {
	u, head := openTestNeo4j(t)
	runReported(t, u, "order height desc limit 10", ranke.Query{
		Select: ranke.Select{Branch: ranke.BranchUniverse, Head: head},
		Order:  []ranke.OrderKey{{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortDesc}},
		Limit:  ranke.Limit{Results: 10},
	})
}

func TestNeo4jQueryPathDepth(t *testing.T) {
	u, head := openTestNeo4j(t)
	runReported(t, u, "derivation path depth 3", ranke.Query{
		Select: ranke.Select{Branch: ranke.BranchUniverse, Claim: ranke.Anchors(head), Path: []ranke.PathStep{{Edges: []string{"derivation/*"}, Max: 3}}},
	})
}

func TestNeo4jQueryDetailPath(t *testing.T) {
	u, head := openTestNeo4j(t)
	runReported(t, u, "derivation path depth 3, shape=path", ranke.Query{
		Select: ranke.Select{Branch: ranke.BranchUniverse, Claim: ranke.Anchors(head), Path: []ranke.PathStep{{Edges: []string{"derivation/*"}, Max: 3}}},
		Output: ranke.Output{Shape: ranke.ShapePath},
	})
}
