package matrix_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/tests/backends"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/matrix"
	"github.com/rankegraph/ranke-go/tests/rql"
)

// TestBranchesArePopulated: the generated archive carries several branches, each
// holding claims. Branch-scoped reads mean nothing on an archive with one branch.
func TestBranchesArePopulated(t *testing.T) {
	u, cleanup, err := backends.Reference().Open()
	require.NoError(t, err)
	defer cleanup()

	m, err := generator.Generate(context.Background(), u, generator.SpecForSize(1, 5))
	require.NoError(t, err)
	require.Greater(t, len(m.Branches), 1, "the archive names more than one branch")

	for _, name := range m.Branches {
		require.NotEmpty(t, branchMembers(t, u, m.Head, name),
			"branch %q holds claims", name)
	}
}

// TestBranchIsolation: a branch read returns that branch's members and leaves the
// others' exclusive claims out.
func TestBranchIsolation(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyBranches(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		require.Greater(t, len(m.Branches), 1, "needs more than one branch to isolate")

		members := map[string][]string{}
		for _, name := range m.Branches {
			members[name] = branchMembers(t, u, m.Head, name)
		}
		for _, name := range m.Branches {
			exclusive := exclusiveTo(name, members)
			require.NotEmpty(t, exclusive, "branch %q holds claims no other branch does", name)
			for _, other := range m.Branches {
				if other == name {
					continue
				}
				for _, id := range exclusive {
					require.NotContains(t, members[other], id,
						"branch %q leaves out %q's exclusive claim", other, name)
				}
			}
		}
	})
}

// TestBranchConfinesAReverseStep: a step walking backward stays inside the scope's graph
// (`R-QCSCOPE`). The toy's source sits in both branches, each holding one derivation that
// cites it, so the other branch's referrer is backward-reachable and must stay out. The
// answer is stated outright: both engines confine, so a gap they shared would satisfy an
// agreement check.
func TestBranchConfinesAReverseStep(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyBranches(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		require.Len(t, m.Branches, 2, "needs a second branch to leave out")
		require.Equal(t, rql.Branch, m.Branches[0], "main takes the first contribution")
		other := m.Branches[1]

		src, here, there := m.Sources[0], m.DiffChainHead, m.Derivations[0]
		members := map[string][]string{rql.Branch: branchMembers(t, u, m.Head, rql.Branch),
			other: branchMembers(t, u, m.Head, other)}
		require.Subset(t, members[rql.Branch], []string{src.String(), here.String()},
			"%q holds the source and the derivation citing it", rql.Branch)
		require.Subset(t, members[other], []string{src.String(), there.String()},
			"%q holds the same source and a derivation of its own", other)
		require.NotContains(t, members[rql.Branch], there.String(),
			"that far derivation is the other branch's alone")

		// Backward from the source along the edges that cite it, unbounded: a segment
		// free to run as far as the graph allows is the one confinement must bound.
		uses := func(branch string) []string {
			return reached(t, u, m.Head, ranke.Query{
				Select: ranke.Select{Branch: branch, Claim: ranke.Anchors(src),
					Path: []ranke.PathStep{{Dir: ranke.DirUses, Edges: []string{"derivation/*"}}}},
			})
		}
		require.ElementsMatch(t, idStrings([]ranke.Id{here, there}), uses(ranke.BranchArchive),
			"across the archive the walk reaches both referrers, so each is backward-reachable")
		require.Equal(t, []string{here.String()}, uses(rql.Branch),
			"under %q the walk reaches that branch's referrer alone", rql.Branch)
		require.Equal(t, []string{there.String()}, uses(other),
			"under %q likewise, so neither answer is the whole set narrowed by luck", other)

		// A path shape lowers through a pipeline of its own, so its route is confined
		// separately: one route, ending at the branch's own referrer.
		route, err := rql.Run(context.Background(), u, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: ranke.Anchors(src),
				Path: []ranke.PathStep{{Dir: ranke.DirUses, Edges: []string{"derivation/*"}}}},
			Output: ranke.Output{Shape: ranke.ShapePath, Detail: ranke.DetailID},
		}, m.Head)
		require.NoError(t, err)
		require.Len(t, route, 1, "one endpoint in reach under %q, hence one route", rql.Branch)
		require.Contains(t, route[0], "path["+src.String()+" "+here.String()+"]",
			"the route runs from the source to the referrer the branch holds")
	})
}

// Universe.ClaimsInBranches decides which references a contribution may read, so a
// backend answering from its own index has to agree with one that walks for it.

// branchHeads reads each branch's head off the archive.
func branchHeads(t *testing.T, u ranke.Universe, archiveHead ranke.Id) map[string]ranke.Id {
	t.Helper()
	arc, err := ranke.NewArchive(context.Background(), u, archiveHead)
	require.NoError(t, err)
	bs, err := arc.GetBranches(context.Background())
	require.NoError(t, err)

	out := map[string]ranke.Id{}
	for _, b := range bs {
		out[b.Name()] = b.Head()
	}
	return out
}

// TestClaimsInBranchesIsBranchScoped: a branch holds its own head alone, which is the
// whole question the read check asks.
func TestClaimsInBranchesIsBranchScoped(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyBranches(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		ctx := context.Background()
		heads := branchHeads(t, u, m.Head)
		require.Len(t, heads, 2, "needs a second branch to be wrong about")

		for name, head := range heads {
			held, err := u.ClaimsInBranches(ctx, map[string]ranke.Id{name: head}, []ranke.Id{head})
			require.NoError(t, err)
			require.Equal(t, []bool{true}, held, "branch %q holds its own head", name)

			for other, otherHead := range heads {
				if other == name {
					continue
				}
				held, err := u.ClaimsInBranches(ctx, map[string]ranke.Id{other: otherHead}, []ranke.Id{head})
				require.NoError(t, err)
				require.Equal(t, []bool{false}, held, "branch %q must not hold %q's head", other, name)
			}
		}
	})
}

// TestClaimsInBranchesUnionsScopes: several branches at once answer for their union,
// a caller passing exactly the scopes it may read.
func TestClaimsInBranchesUnionsScopes(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyBranches(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		heads := branchHeads(t, u, m.Head)
		ids := make([]ranke.Id, 0, len(heads))
		for _, h := range heads {
			ids = append(ids, h)
		}
		held, err := u.ClaimsInBranches(context.Background(), heads, ids)
		require.NoError(t, err)
		require.Equal(t, []bool{true, true}, held, "each head lies in the union of both branches")
	})
}

// TestClaimsInBranchesArchiveCoversTheSpine: $archive is every branch's superset plus
// the tables, so it holds the archive head itself.
func TestClaimsInBranchesArchiveCoversTheSpine(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyBranches(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		ctx := context.Background()
		heads := branchHeads(t, u, m.Head)
		archive := map[string]ranke.Id{ranke.BranchArchive: m.Head}

		held, err := u.ClaimsInBranches(ctx, archive, []ranke.Id{m.Head})
		require.NoError(t, err)
		require.Equal(t, []bool{true}, held, "$archive holds the branch table at the archive head")

		held, err = u.ClaimsInBranches(ctx, heads, []ranke.Id{m.Head})
		require.NoError(t, err)
		require.Equal(t, []bool{false}, held, "no branch holds the table that names it")

		for name, head := range heads {
			held, err := u.ClaimsInBranches(ctx, archive, []ranke.Id{head})
			require.NoError(t, err)
			require.Equal(t, []bool{true}, held, "$archive holds branch %q's head", name)
		}
	})
}

// TestClaimsInBranchesRejectsStrangers: a claim the archive never merged is held by
// nothing, so a reference to it passes no scope.
func TestClaimsInBranchesRejectsStrangers(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyBranches(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		ctx := context.Background()
		stranger, err := ranke.HashContent([]byte("never contributed"))
		require.NoError(t, err)

		for scope, arg := range map[string]map[string]ranke.Id{
			"branches": branchHeads(t, u, m.Head),
			"$archive": {ranke.BranchArchive: m.Head},
		} {
			held, err := u.ClaimsInBranches(ctx, arg, []ranke.Id{stranger})
			require.NoError(t, err, "an absent claim is answered false")
			require.Equal(t, []bool{false}, held, "%s holds nothing it never merged", scope)
		}
	})
}

// TestClaimsInBranchesIsPositional: answers are read by index, so a mixed batch has to
// line up with what was asked.
func TestClaimsInBranchesIsPositional(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyBranches(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		var name string
		var head ranke.Id
		for n, h := range branchHeads(t, u, m.Head) {
			name, head = n, h
			break
		}
		stranger, err := ranke.HashContent([]byte("never contributed"))
		require.NoError(t, err)

		held, err := u.ClaimsInBranches(context.Background(), map[string]ranke.Id{name: head},
			[]ranke.Id{stranger, head, stranger})
		require.NoError(t, err)
		require.Equal(t, []bool{false, true, false}, held, "each answer sits at its own index")
	})
}

// branchMembers scans one branch and returns the ids it holds.
func branchMembers(t *testing.T, u ranke.Universe, archiveHead ranke.Id, branch string) []string {
	t.Helper()
	answer, err := rql.Run(context.Background(), u, ranke.Query{
		Select: ranke.Select{Branch: branch},
	}, archiveHead)
	require.NoError(t, err)
	out := make([]string, 0, len(answer))
	for _, fp := range answer {
		out = append(out, firstField(fp))
	}
	return out
}

// exclusiveTo returns the ids only this branch holds.
func exclusiveTo(branch string, members map[string][]string) []string {
	elsewhere := map[string]bool{}
	for name, ids := range members {
		if name == branch {
			continue
		}
		for _, id := range ids {
			elsewhere[id] = true
		}
	}
	var out []string
	for _, id := range members[branch] {
		if !elsewhere[id] {
			out = append(out, id)
		}
	}
	return out
}
