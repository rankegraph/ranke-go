package matrix_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/matrix"
)

// TestMain releases the shared archives once every test has had them. They are opened
// per (archive, row) rather than per test, so they outlive the test that built them and
// nothing but the end of the run can close them.
func TestMain(m *testing.M) {
	code := m.Run()
	matrix.CloseFixtures()
	os.Exit(code)
}

// TestMatrix is the full agreement matrix: every backend the run asks for, compared
// against the mem reference over the whole RQL corpus. A row that cannot open fails —
// name the set with RANKE_ROWS and bring its services up (services/*.sh native up).
func TestMatrix(t *testing.T) {
	matrix.Run(t, matrix.Config{})
}

// TestFixturesOpenOncePerRow: an archive is opened once per (archive, row) and reused,
// which is the difference between this package taking seconds and taking minutes. The
// exception is a row holding an exclusive service — the neo4j rows share one database
// and flush it at open, so theirs cannot be kept alive and is opened per test. Asserted
// with a spec no other test touches, so the count measured is this test's own.
func TestFixturesOpenOncePerRow(t *testing.T) {
	rows := matrix.Rows(t)
	var exclusive int
	for _, row := range rows {
		if row.Exclusive != "" {
			exclusive++
		}
	}
	noop := func(*testing.T, ranke.Universe, *generator.Manifest) {}

	spec := generator.ToyDiff(99)
	before := matrix.Opens()
	matrix.Each(t, matrix.FromSpec(spec), noop)
	first := matrix.Opens()
	require.Equal(t, len(rows), first-before, "one open per requested row, first time round")

	matrix.Each(t, matrix.FromSpec(spec), noop)
	require.Equal(t, exclusive, matrix.Opens()-first,
		"the same archive again opens only the rows that cannot be held open")
}

// TestMatrixBranchClosure is the minimal focus case: the smallest archive that
// still has multiple revisions and a claim shared across them, and just the
// branch closure. It isolates branch membership — the _b_<branch> tag set a
// native backend scans must equal the reference's closure walk.
func TestMatrixBranchClosure(t *testing.T) {
	matrix.Run(t, matrix.Config{Size: 2, Only: "closure/branch"})
}
