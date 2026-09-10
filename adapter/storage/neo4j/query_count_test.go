package neo4j

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/internal/exclusive"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/stretchr/testify/require"
)

// Default credentials for a local test neo4j (overridable via RANKE_NEO4J_*).
const (
	testNeo4jUser = "neo4j"
	testNeo4jPass = "rankeperfpass"
)

func testEnv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// neo4jReachable reports whether a neo4j serves at the default (or env-named)
// HTTP endpoint — a fast probe, so tests run against a running instance with no
// env var and skip cleanly when none is up.
func neo4jReachable() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(testEnv("RANKE_NEO4J_HTTP", "http://127.0.0.1:7474") + "/")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// connectTestNeo4j connects to a live neo4j and flushes it, returning an empty
// cache Universe and the driver (so a test can read the projection directly).
func connectTestNeo4j(t *testing.T) (*neo4jUniverse, neo4jdriver.DriverWithContext) {
	t.Helper()
	if os.Getenv("RANKE_NEO4J_BOLT") == "" && !neo4jReachable() {
		t.Skip("no neo4j reachable (services/neo4j.sh native up)")
	}
	// The flush below empties the one live instance every package shares, so access is
	// held exclusive for the test's lifetime (see internal/exclusive).
	release := exclusive.Lock(exclusive.Neo4j)
	t.Cleanup(release)
	driver, err := neo4jdriver.NewDriverWithContext(testEnv("RANKE_NEO4J_BOLT", "bolt://127.0.0.1:7687"),
		neo4jdriver.BasicAuth(testEnv("RANKE_NEO4J_USER", testNeo4jUser), testEnv("RANKE_NEO4J_PASS", testNeo4jPass), ""))
	require.NoError(t, err)
	t.Cleanup(func() { _ = driver.Close(context.Background()) })
	_, err = neo4jdriver.ExecuteQuery(context.Background(), driver, "MATCH (n) DETACH DELETE n", nil,
		neo4jdriver.EagerResultTransformer, neo4jdriver.ExecuteQueryWithDatabase("neo4j"))
	require.NoError(t, err)
	return New(driver, WithDatabase("neo4j"), WithContentCap(4096)).(*neo4jUniverse), driver
}

// openTestNeo4j connects to a live neo4j, flushes, seeds a small graph, and
// returns the *neo4jUniverse (so tests can read its query counter) and head id.
func openTestNeo4j(t *testing.T) (*neo4jUniverse, ranke.Id) {
	t.Helper()
	u, _ := connectTestNeo4j(t)

	ctx := context.Background()
	mem := ranke.NewMemoryUniverse()
	// Size 8 is ~50 claims: enough to truncate a limit of 10 and to carry the path
	// depths the query tests assert, and a third of the copy cost of the size that
	// was here. Each assertion polices its own floor, so a seed too small fails.
	man, err := generator.Generate(ctx, mem, generator.SpecForSize(1, 8))
	require.NoError(t, err)
	require.NoError(t, u.CopyClaims(ctx, mem, []ranke.Id{man.Head}, ranke.WithClosure()))
	return u, man.Head
}

// TestOneQueryPerRQL asserts every RQL shape resolves in exactly one Cypher
// round-trip — no reconstruction re-fetch, no fallback.
func TestOneQueryPerRQL(t *testing.T) {
	u, head := openTestNeo4j(t)
	ctx := context.Background()
	scope := ranke.Scope{Branch: ranke.BranchUniverse}
	sel := ranke.Select{Branch: ranke.BranchUniverse, Head: head}
	selPath := ranke.Select{Branch: ranke.BranchUniverse, Claim: head, Path: []ranke.PathStep{{Edges: []string{"derivation/*"}, Max: 3}}}

	cases := map[string]ranke.Query{
		"single/id":     {Select: sel, Output: ranke.Output{Detail: ranke.DetailID}},
		"single/claims": {Select: sel, Output: ranke.Output{Detail: ranke.DetailClaims}},
		"single/where":  {Select: sel, Where: &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}}},
		"single/order":  {Select: sel, Order: []ranke.OrderKey{{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortDesc}}, Limit: ranke.Limit{Results: 10}},
		"path/claims":   {Select: selPath, Output: ranke.Output{Shape: ranke.ShapePath, Detail: ranke.DetailClaims}},
		// An unbounded traversal (one step, no depth) still reports routes.
		"path/unbounded": {Select: ranke.Select{Branch: ranke.BranchUniverse, Claim: head, Path: []ranke.PathStep{{}}},
			Output: ranke.Output{Shape: ranke.ShapePath}},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			before := u.queries.Load()
			rs, err := u.Query(ctx, q, scope)
			require.NoError(t, err)
			n := 0
			for rs.Next() {
				n++
			}
			require.NoError(t, rs.Err())
			require.NoError(t, rs.Close())
			got := u.queries.Load() - before
			require.Equal(t, int64(1), got, "expected exactly 1 Cypher query, got %d (%d results)", got, n)
		})
	}
}
