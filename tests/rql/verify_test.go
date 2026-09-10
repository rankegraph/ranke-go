package rql

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage/mem"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/stretchr/testify/require"
)

// Every rule here is fed an answer that breaks it and asserted to catch it. A
// verifier nothing has been seen to fail is indistinguishable from a stub, so each
// test states the violation it introduces rather than only that something fired.

// check runs the verifier over elements with no archive behind it, for the rules
// that read the answer alone.
func check(t *testing.T, q ranke.Query, elements ...ranke.QueryResult) []Violation {
	t.Helper()
	return VerifyAnswer(context.Background(), nil, nil, q, elements,
		WithSkipRules("scope membership"))
}

// firedOn reports whether any violation came from the named rule.
func firedOn(vs []Violation, rule string) bool {
	for _, v := range vs {
		if v.Rule == rule {
			return true
		}
	}
	return false
}

// claimAt builds a claim dated at t, so an order test has real fields to sort on.
func claimAt(t *testing.T, ctr ranke.Contributor, at time.Time, height uint64) ranke.Claim {
	t.Helper()
	c, err := ranke.ClaimBuilder{
		Type:        ranke.TypeSource("note"),
		Contributor: ctr,
		CreatedAt:   at,
		Height:      ranke.FixedHeight(height),
	}.Sign()
	require.NoError(t, err)
	return c
}

// contributorFor mints one signing contributor for the order tests.
func contributorFor(t *testing.T) ranke.Contributor {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream).
		Sign(priv)
	require.NoError(t, err)
	ctr, err := c.AsContributor(context.Background(), nil, priv)
	require.NoError(t, err)
	return ctr
}

// TestAnswerRuleSetEnumerates: the registry is the purchase — a rule nobody checks
// has to read as an absent entry, which means the list must be enumerable and every
// entry must name the spec rule it enforces.
func TestAnswerRuleSetEnumerates(t *testing.T) {
	set := AnswerRuleSet()
	require.NotEmpty(t, set)
	seen := map[string]bool{}
	for _, r := range set {
		require.NotEmpty(t, r.Name, "every entry is addressable by name (WithSkipRules)")
		require.False(t, seen[r.Name], "names are unique: %q", r.Name)
		seen[r.Name] = true
		require.Contains(t, r.Rule, "R-Q", "entry %q cites no spec rule id", r.Name)
	}
	require.Len(t, set, len(answerRules), "the exported menu is the registry, not a copy of part of it")
}

// TestElementTagFires: `R-QSTREAM` — a reader must never inspect a payload to learn
// what an element holds, so an untagged element, an unknown tag, and a tag naming a
// payload that is absent are each caught.
func TestElementTagFires(t *testing.T) {
	q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}}

	t.Run("untagged", func(t *testing.T) {
		vs := check(t, q, ranke.QueryResult{})
		require.True(t, firedOn(vs, "element tag"), "an element with no Kind is untagged")
	})
	t.Run("unknown tag", func(t *testing.T) {
		vs := check(t, q, ranke.QueryResult{Kind: ranke.ResultKind("claim_something")})
		require.True(t, firedOn(vs, "element tag"))
	})
	t.Run("tag without its payload", func(t *testing.T) {
		vs := check(t, q, ranke.QueryResult{Kind: ranke.KindClaimNative}) // ClaimNative nil
		require.True(t, firedOn(vs, "element tag"))
	})
	t.Run("control: a tagged element with its payload passes", func(t *testing.T) {
		id, err := ranke.ParseId("bciqlu6awx6hqdt7kifaubxs5vyrchmadmgrzmf32ts2bb73b6iablli")
		require.NoError(t, err)
		vs := check(t, q, ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: id})
		require.False(t, firedOn(vs, "element tag"))
	})
}

// TestReportPlacementFires: `R-QREPORT` — when, and only when, execution.report is
// set, the FINAL element is a report.
func TestReportPlacementFires(t *testing.T) {
	reported := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive},
		Execution: ranke.Execution{Report: ranke.ReportInfo}}
	plain := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}}
	rep := ranke.QueryResult{Kind: ranke.KindReport, Report: &ranke.QueryReport{}}
	id, err := ranke.ParseId("bciqlu6awx6hqdt7kifaubxs5vyrchmadmgrzmf32ts2bb73b6iablli")
	require.NoError(t, err)
	result := ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: id}

	t.Run("asked for and absent", func(t *testing.T) {
		vs := check(t, reported, result)
		require.True(t, firedOn(vs, "report placement"))
	})
	t.Run("present and not asked for", func(t *testing.T) {
		vs := check(t, plain, result, rep)
		require.True(t, firedOn(vs, "report placement"))
	})
	t.Run("not final", func(t *testing.T) {
		vs := check(t, reported, rep, result)
		require.True(t, firedOn(vs, "report placement"), "a report before a result is not the final element")
	})
	t.Run("control: last and asked for passes", func(t *testing.T) {
		vs := check(t, reported, result, rep)
		require.False(t, firedOn(vs, "report placement"))
	})
}

// TestResultBoundFires: `R-QLIMIT` — the cap bounds the results, and truncation is
// claimed only where a bound could have cut the read.
func TestResultBoundFires(t *testing.T) {
	id, err := ranke.ParseId("bciqlu6awx6hqdt7kifaubxs5vyrchmadmgrzmf32ts2bb73b6iablli")
	require.NoError(t, err)
	res := func(n int) []ranke.QueryResult {
		out := make([]ranke.QueryResult, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: id})
		}
		return out
	}
	reportOf := func(results int, truncated bool) ranke.QueryResult {
		return ranke.QueryResult{Kind: ranke.KindReport,
			Report: &ranke.QueryReport{Results: results, Truncated: truncated}}
	}

	t.Run("over the cap", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}, Limit: ranke.Limit{Results: 2}}
		vs := check(t, q, res(3)...)
		require.True(t, firedOn(vs, "result bound"), "3 results under a cap of 2")
	})
	t.Run("report miscounts the results", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive},
			Execution: ranke.Execution{Report: ranke.ReportInfo}}
		vs := check(t, q, append(res(2), reportOf(5, false))...)
		require.True(t, firedOn(vs, "result bound"))
	})
	t.Run("truncation with no bound to cut it", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive},
			Execution: ranke.Execution{Report: ranke.ReportInfo}}
		vs := check(t, q, append(res(2), reportOf(2, true))...)
		require.True(t, firedOn(vs, "result bound"), "neither bound is set, so nothing cut the read")
	})
	t.Run("truncation below an uncut cap", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}, Limit: ranke.Limit{Results: 5},
			Execution: ranke.Execution{Report: ranke.ReportInfo}}
		vs := check(t, q, append(res(2), reportOf(2, true))...)
		require.True(t, firedOn(vs, "result bound"), "the cap of 5 did not cut an answer of 2")
	})
	t.Run("control: at the cap, truncated, passes", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}, Limit: ranke.Limit{Results: 2},
			Execution: ranke.Execution{Report: ranke.ReportInfo}}
		vs := check(t, q, append(res(2), reportOf(2, true))...)
		require.False(t, firedOn(vs, "result bound"))
	})
	t.Run("control: a time budget explains truncation below the cap", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive},
			Limit:     ranke.Limit{Results: 5, Time: time.Second},
			Execution: ranke.Execution{Report: ranke.ReportInfo}}
		vs := check(t, q, append(res(2), reportOf(2, true))...)
		require.False(t, firedOn(vs, "result bound"))
	})
}

// TestAnswerOrderFires: `R-QSORT` — the sequence must be non-descending under the
// keys, then the natural (created_at, id) tie-break that makes the order total.
func TestAnswerOrderFires(t *testing.T) {
	ctr := contributorFor(t)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	early := claimAt(t, ctr, base, 1)
	late := claimAt(t, ctr, base.Add(time.Hour), 2)
	el := func(c ranke.Claim) ranke.QueryResult {
		return ranke.QueryResult{Kind: ranke.KindClaimNative, ClaimId: c.ID(), ClaimNative: c}
	}

	t.Run("natural order reversed", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}}
		vs := check(t, q, el(late), el(early))
		require.True(t, firedOn(vs, "answer order"), "later created_at emitted first")
	})
	t.Run("control: natural order kept", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}}
		vs := check(t, q, el(early), el(late))
		require.False(t, firedOn(vs, "answer order"))
	})
	t.Run("stated key reversed", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive},
			Order: []ranke.OrderKey{{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortAsc}}}
		vs := check(t, q, el(late), el(early)) // heights 2 then 1, asked ascending
		require.True(t, firedOn(vs, "answer order"))
	})
	t.Run("control: stated key honoured, descending", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive},
			Order: []ranke.OrderKey{{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortDesc}}}
		vs := check(t, q, el(late), el(early))
		require.False(t, firedOn(vs, "answer order"))
	})
	t.Run("an answer carrying no claims is passed over", func(t *testing.T) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}}
		vs := check(t, q,
			ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: late.ID()},
			ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: early.ID()})
		require.False(t, firedOn(vs, "answer order"), "detail:id carries no fields to order by")
	})
}

// TestScopeMembershipFires: `R-QSCOPE` — only the scope's graph is read. Fed a claim
// that belongs to another branch, against a real two-branch archive, so the
// membership answer comes from the system rather than from a hand-built expectation.
func TestScopeMembershipFires(t *testing.T) {
	ctx := context.Background()
	u := mem.New()
	m, err := generator.Generate(ctx, u, generator.ToyBranches(1))
	require.NoError(t, err)
	require.Greater(t, len(m.Branches), 1, "needs two branches for one to be foreign")
	arc, err := ranke.NewArchive(ctx, u, m.Head)
	require.NoError(t, err)

	// A claim each branch holds exclusively, found through the membership port itself.
	heads := map[string]ranke.Id{}
	for _, name := range m.Branches {
		b, err := arc.GetBranch(ctx, name)
		require.NoError(t, err)
		heads[name] = b.Head()
	}
	foreign, host := exclusiveClaim(t, ctx, u, m, heads)

	q := ranke.Query{Select: ranke.Select{Branch: host}}
	el := ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: foreign}
	vs := VerifyAnswer(ctx, arc, u, q, []ranke.QueryResult{el})
	require.True(t, firedOn(vs, "scope membership"),
		"a claim outside branch %q was returned for it", host)

	t.Run("control: a claim of the branch passes", func(t *testing.T) {
		own := ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: heads[host]}
		vs := VerifyAnswer(ctx, arc, u, q, []ranke.QueryResult{own})
		require.False(t, firedOn(vs, "scope membership"))
	})
	t.Run("$universe is not checked, and says so in its rule", func(t *testing.T) {
		uq := ranke.Query{Select: ranke.Select{Branch: ranke.BranchUniverse, Head: m.Head}}
		vs := VerifyAnswer(ctx, arc, u, uq, []ranke.QueryResult{el})
		require.False(t, firedOn(vs, "scope membership"))
	})
}

// exclusiveClaim finds a claim one branch holds and another does not, returning it
// and the name of the branch it is foreign to.
func exclusiveClaim(t *testing.T, ctx context.Context, u ranke.Universe,
	m *generator.Manifest, heads map[string]ranke.Id) (ranke.Id, string) {
	t.Helper()
	candidates := append(append([]ranke.Id{}, m.Sources...), m.Derivations...)
	require.NotEmpty(t, candidates)
	for _, id := range candidates {
		for _, name := range m.Branches {
			in, err := u.ClaimsInBranches(ctx, map[string]ranke.Id{name: heads[name]}, []ranke.Id{id})
			require.NoError(t, err)
			if !in[0] {
				return id, name // foreign to this branch
			}
		}
	}
	t.Fatal("every generated claim is in every branch — the fixture cannot show a leak")
	return nil, ""
}

// TestCollectsEveryViolation: the graph verifier gathers failures rather than
// stopping at the first, because you want everything wrong with an archive. Same for
// an answer — three broken rules report three.
func TestCollectsEveryViolation(t *testing.T) {
	q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}, Limit: ranke.Limit{Results: 1}}
	vs := check(t, q,
		ranke.QueryResult{},                                    // untagged
		ranke.QueryResult{Kind: ranke.KindClaimNative},         // tag without payload
		ranke.QueryResult{Kind: ranke.KindReport, Report: nil}, // report unasked, and no payload
	)
	require.GreaterOrEqual(t, len(vs), 3, "one violation per broken check, not the first: %v", vs)
	require.True(t, firedOn(vs, "element tag"))
	require.True(t, firedOn(vs, "report placement"))
	require.True(t, firedOn(vs, "result bound"), "3 elements under a cap of 1")
}

// TestWithSkipRulesSilencesByName: the lever for a rule the fast gate cannot carry —
// left out deliberately and by name, not by being written not to run.
func TestWithSkipRulesSilencesByName(t *testing.T) {
	q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}}
	elements := []ranke.QueryResult{{}} // untagged

	vs := VerifyAnswer(context.Background(), nil, nil, q, elements, WithSkipRules("scope membership"))
	require.True(t, firedOn(vs, "element tag"))

	vs = VerifyAnswer(context.Background(), nil, nil, q, elements,
		WithSkipRules("scope membership", "element tag"))
	require.False(t, firedOn(vs, "element tag"), "skipped by name")
}
