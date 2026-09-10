// package: tests/matrix / conformance
// type:    test
// job:     the cross-backend agreement matrix — build one deterministic archive into every
// available backend, run the shared RQL corpus against each, and assert every backend's
// answer matches the reference's
// limits:  correctness only, never timing (-> tests/performance); it asks whether an answer is
// right, never how a backend produced it — routing, lowering, and cache tiers are the
// backend's business
//
// Two backends must answer a query identically; a divergence is a conformance bug
// in at least one. Run it over your own rows with matrix.Run(t, Config{Rows: ...}).
package matrix

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/tests/backends"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/rql"
)

// defaultSize builds fast into every backend while still carrying every corner
// (revisions, diff chains, external blobs, both relation polarities).
//
// It also carries the one corner that only some sizes have: a source reached only
// through the derivation citing it, so a reverse step must re-cross that edge
// (`R-QFRONTIER`). Sources acquire a contribution/head route of their own as the
// archive grows, which retires the corner — it is absent below 5 and above 20, and
// densest at 8. Moving this knob outside that window drops the corner silently.
const defaultSize = 5

// Config parameterises an agreement run.
type Config struct {
	// Reference is the engine every row is compared against; the zero value is
	// backends.Reference(), the in-memory store on the reference executor.
	Reference *backends.Backend
	// Rows are the backends under test; nil means the set the run asks for. The row
	// named like the reference drops out — nothing is compared to itself.
	Rows []backends.Backend
	// Size is the generator size knob; 0 means defaultSize.
	Size int
	// Only narrows the run to the one corpus entry with this name — the focus
	// mode for isolating a single divergence. Empty runs the whole corpus.
	Only string
}

// Run builds one deterministic archive into the reference and every available
// row, then asserts each row answers the corpus identically, in stream order.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	ctx := context.Background()

	ref := backends.Reference()
	if cfg.Reference != nil {
		ref = *cfg.Reference
	}
	rows := cfg.Rows
	if rows == nil {
		rows = Rows(t)
	}
	size := cfg.Size
	if size == 0 {
		size = defaultSize
	}
	recipe := FromSpec(generator.SpecForSize(1, size))

	// The reference is the baseline every row is judged against, so its own
	// failure to open is fatal.
	refU, refM := shared(t, recipe, ref)
	refHead := branchHead(t, ctx, refU, refM)
	queries := corpus(refM, refHead, cfg.Only)
	require.NotEmptyf(t, queries, "no corpus entry named %q", cfg.Only)

	refAnswers := make(map[string]rql.Answer, len(queries))
	for _, nq := range queries {
		answer, err := rql.Run(ctx, refU, nq.Q, refM.Head)
		require.NoErrorf(t, err, "reference %q: query %s", ref.Name, nq.Name)
		refAnswers[nq.Name] = answer
	}

	// A query matching nothing would make every row agree trivially, so the
	// corpus is checked for substance before any row is compared against it.
	t.Run("reference/corpus-is-substantive", func(t *testing.T) {
		for _, nq := range queries {
			if len(refAnswers[nq.Name]) == 0 {
				t.Errorf("%s matched no claim on the %s reference — the entry proves nothing; fix the query or the generator corner it targets\n  rql: %s",
					nq.Name, ref.Name, rql.Describe(nq.Q))
			}
		}
	})

	// Nothing is compared to itself, so the reference drops out of the row set.
	compared := make([]backends.Backend, 0, len(rows))
	for _, row := range rows {
		if row.Name != ref.Name {
			compared = append(compared, row)
		}
	}
	eachRow(t, compared, recipe, func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		// Content addressing makes generation reproducible: one spec yields the
		// same ids everywhere, which is what makes answers comparable.
		require.Equalf(t, refHead.String(), branchHead(t, ctx, u, m).String(),
			"branch head differs from the %s reference — the archives are not the same graph", ref.Name)

		for _, nq := range queries {
			t.Run(nq.Name, func(t *testing.T) {
				got, err := rql.Run(ctx, u, nq.Q, m.Head)
				require.NoError(t, err)
				require.Equalf(t, refAnswers[nq.Name], got,
					"%s disagrees with %s\n  rql: %s\n  %s returned %d results, %s returned %d",
					t.Name(), ref.Name, rql.Describe(nq.Q),
					ref.Name, len(refAnswers[nq.Name]), t.Name(), len(got))
			})
		}
	})

	if len(compared) == 0 {
		t.Logf("the row set holds nothing to compare against %s — the matrix proved nothing this run "+
			"(name rows with %s, and start their services: services/neo4j.sh native up)", ref.Name, backends.RowsEnv)
	}
}

// Recipe builds one archive into a universe. Key is its identity — one key, one
// instance, shared by every test that asks, so those tests must only READ it.
type Recipe[T any] struct {
	Key   string
	Build func(context.Context, ranke.Universe) (T, error)
}

// FromSpec is the recipe for a generator spec. The spec is the key: one spec yields one
// archive, ids included, by the generator's contract.
func FromSpec(spec generator.Spec) Recipe[*generator.Manifest] {
	return Recipe[*generator.Manifest]{
		Key: fmt.Sprintf("spec%+v", spec),
		Build: func(ctx context.Context, u ranke.Universe) (*generator.Manifest, error) {
			return generator.Generate(ctx, u, spec)
		},
	}
}

// Each runs check over every row this run asks for, against the archive r builds — the
// package's one row iteration.
func Each[T any](t *testing.T, r Recipe[T], check func(*testing.T, ranke.Universe, T)) {
	t.Helper()
	eachRow(t, Rows(t), r, check)
}

// eachRow is Each over an explicit row set — what Run needs, since it drops the
// reference from the set it compares.
func eachRow[T any](t *testing.T, rows []backends.Backend, r Recipe[T], check func(*testing.T, ranke.Universe, T)) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			u, built := shared(t, r, row)
			check(t, u, built)
		})
	}
}

// fixture is one built archive: the open universe, its cleanup, and whatever the recipe
// produced. It outlives the test that built it, which is the point.
type fixture struct {
	u       ranke.Universe
	cleanup func()
	built   any
}

type fixtureKey struct{ recipe, row string }

var (
	fixtureMu sync.Mutex
	fixtures  = map[fixtureKey]*fixture{}
	opens     atomic.Int64
)

// shared returns the row's universe with r's archive in it, built on first ask and
// reused after. A row holding an exclusive service cannot be kept alive, so it is built
// for this test alone. A requested row that cannot open FAILS: ErrUnavailable is an
// error like any other, and the row set decides what runs.
func shared[T any](t *testing.T, r Recipe[T], row backends.Backend) (ranke.Universe, T) {
	t.Helper()
	if row.Exclusive != "" {
		u, cleanup := openRow(t, row)
		t.Cleanup(cleanup)
		built, err := r.Build(context.Background(), u)
		require.NoErrorf(t, err, "row %q: building the archive", row.Name)
		return u, built
	}

	fixtureMu.Lock()
	defer fixtureMu.Unlock()
	key := fixtureKey{recipe: r.Key, row: row.Name}
	if f, ok := fixtures[key]; ok {
		return f.u, f.built.(T)
	}
	u, cleanup := openRow(t, row)
	built, err := r.Build(context.Background(), u)
	if err != nil {
		cleanup()
		require.NoErrorf(t, err, "row %q: building the archive", row.Name)
	}
	fixtures[key] = &fixture{u: u, cleanup: cleanup, built: built}
	return u, built
}

// openRow opens the row or fails the test, counting the open so a change that reopens
// a row per test shows up as a number.
func openRow(t *testing.T, row backends.Backend) (ranke.Universe, func()) {
	t.Helper()
	opens.Add(1)
	u, cleanup, err := row.Open()
	require.NoErrorf(t, err, "row %q was asked for and could not open", row.Name)
	return u, cleanup
}

// Opens counts the backends opened for fixtures so far. A test asserts on it, so a
// change that reopens a row per test shows up as a number rather than as minutes.
func Opens() int { return int(opens.Load()) }

// CloseFixtures releases every shared archive. TestMain calls it once the run is over:
// the instances outlive the tests that built them, so no test can close them.
func CloseFixtures() {
	fixtureMu.Lock()
	defer fixtureMu.Unlock()
	for key, f := range fixtures {
		f.cleanup()
		delete(fixtures, key)
	}
}

// Rows is the row set this run asks for (RANKE_ROWS, all of them when unset). An
// unresolvable name fails the test: a mistyped row would otherwise narrow it silently.
func Rows(t *testing.T) []backends.Backend {
	t.Helper()
	rows, err := backends.Requested()
	require.NoErrorf(t, err, "the row set named by %s", backends.RowsEnv)
	return rows
}

// corpus is the query set for a run, narrowed to a single entry in focus mode.
func corpus(m *generator.Manifest, root ranke.Id, only string) []rql.NamedQuery {
	all := rql.Corpus(m, root)
	if only == "" {
		return all
	}
	var out []rql.NamedQuery
	for _, nq := range all {
		if nq.Name == only {
			out = append(out, nq)
		}
	}
	return out
}

// branchHead reads the "main" head off a built archive. A backend needing a
// preparation call to become correct is the defect.
func branchHead(t *testing.T, ctx context.Context, u ranke.Universe, m *generator.Manifest) ranke.Id {
	t.Helper()
	arc, err := ranke.NewArchive(ctx, u, m.Head)
	require.NoError(t, err)
	br, err := arc.GetBranch(ctx, rql.Branch)
	require.NoError(t, err)
	return br.Head()
}
