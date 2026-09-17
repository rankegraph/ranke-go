// package: ranke / query
// type:    logic
// job:     DefaultQuery — the reference RQL executor a byte-store Universe delegates to: a
// forward-closure walk (reverse via closure inversion) with filter/order/limit/shape
// limits:  performance-ignorant; a graph-native backend overrides with a native lowering
// (-> adapter/storage/neo4j)
package ranke

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// closureOrigin is where the walk starts: Select.Head, else the scope's head. It
// bounds nothing — a Head narrows a read, the scope still confines it.
func closureOrigin(sel Select, scope Scope) (Id, error) {
	if sel.Head != nil {
		return sel.Head, nil
	}
	if scope.Head != nil {
		return scope.Head, nil
	}
	return nil, ErrQueryNoHead
}

// confinement is the scope's graph as a membership test — closure(root), which for
// a branch scope is that branch. Nil admits everything, which is $universe.
//
// A read is the INTERSECTION of this and closure(Select.Head): walking from root
// instead would lose the narrowing. tagKey and members are two tactics for one
// answer, and neither costs admits any I/O.
type confinement struct {
	root    Id
	tagKey  string          // branch tag, when tags decide membership
	height  uint64          // table height the tag is compared against
	members map[string]bool // swept closure, when they do not
}

// admits reports whether the scope's graph holds cl. The tag carries the height the
// claim joined at, so <= height is membership and point-in-time at once.
func (c *confinement) admits(cl Claim) bool {
	if c == nil {
		return true
	}
	if cl == nil {
		return false
	}
	if c.tagKey == "" {
		return c.members[cl.ID().String()]
	}
	v := cl.Tag(c.tagKey)
	if v == "" {
		return false
	}
	at, err := strconv.ParseUint(v, 10, 64)
	return err == nil && at <= c.height
}

// confine builds the scope's membership test, nil when the scope confines nothing.
// Tags answer per claim where the Universe keeps them, else the closure is swept.
func confine(ctx context.Context, u Universe, scope Scope) (*confinement, error) {
	if scope.Head == nil {
		return nil, nil
	}
	tagged, err := tagConfinement(ctx, u, scope)
	if err != nil || tagged != nil {
		return tagged, err
	}
	members := map[string]bool{}
	queue := []Id{scope.Head}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		if cur == nil || members[cur.String()] {
			continue
		}
		members[cur.String()] = true
		c, err := GetClaim(ctx, u, cur)
		if err != nil {
			return nil, WrapDetail(errQuery, "scope closure "+cur.String(), err)
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}
	return &confinement{root: scope.Head, members: members}, nil
}

// tagConfinement is the branch-tag membership test, nil when tags cannot decide: no
// tags kept, no named branch, or this branch never tagged. Tagging is an accelerator
// a read may not depend on and an absent tag reads as non-membership, so the head's
// own tag — the tagger stamps a branch from there in one pass — is the proof.
func tagConfinement(ctx context.Context, u Universe, scope Scope) (*confinement, error) {
	named := scope.Branch != "" && scope.Branch != BranchUniverse && scope.Branch != BranchArchive
	if !named || !u.Capabilities().Tags {
		return nil, nil
	}
	key := BranchTagKey(scope.Branch)
	found, _, err := GetTag(ctx, u, scope.Head, key)
	if err != nil || !found {
		return nil, err
	}
	return &confinement{root: scope.Head, tagKey: key, height: scope.Height}, nil
}

// frontier is the set a read starts from: the anchors `R-QANCHOR` names, else the
// origin. An anchor supersedes the Head, which matches `R-QHEAD`'s intersection while
// `R-QANCHOR`'s MUST holds — that each anchor lies inside the closure, unchecked here.
func frontier(sel Select, origin Id) []Id {
	if len(sel.Claim) > 0 {
		return sel.Claim
	}
	return []Id{origin}
}

// DefaultQuery is the reference implementation of Universe.Query, reading only through
// the public Universe API: walk the closure, materialise, filter, order, limit, shape.
func DefaultQuery(ctx context.Context, u Universe, q Query, scope Scope) (ResultStream, error) {
	start := time.Now()
	ctx, rc, createdReport := beginReport(ctx, q.Execution.Report, start)
	rc.log("native", "select", ReportInfo, "", map[string]any{"branch": q.Select.Branch})
	// outer outlives the budget, so a deadline reached under it is the caller's
	// cancellation and a deadline reached only under ctx is limit.time running out.
	outer := ctx
	if q.Limit.Time > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Limit.Time)
		defer cancel()
	}

	origin, err := closureOrigin(q.Select, scope)
	if err != nil {
		return nil, err
	}
	conf, err := confine(ctx, u, scope)
	if err != nil {
		return nil, err
	}
	needPaths := q.Output.Shape == ShapePath
	sel := q.Select
	if sel.Path == nil {
		sel.Claim = frontier(sel, origin)
		sel.Path = []PathStep{{Min: Hops(0)}} // the frontier's outward closure (`R-QSTEPS`)
		needPaths = false
	} else if len(sel.Path) == 0 {
		// An empty Path takes no step, so the frontier is the answer: the anchors, or
		// the closure itself where a read names none (`R-QSTEPS`, `R-QANCHOR`).
		needPaths = false
	}
	reached, routes, err := queryTraverse(ctx, u, sel, origin, conf, needPaths, rc)
	// limit.time running out bounds the answer rather than failing it (`R-QLIMIT`),
	// so the read keeps what it reached and says it was cut short. The caller's own
	// cancellation is still an error, which is what outer distinguishes.
	bounded := err != nil && q.Limit.Time > 0 && outer.Err() == nil &&
		errors.Is(err, context.DeadlineExceeded)
	if err != nil && !bounded {
		return nil, err
	}
	if bounded {
		rc.log("native", "limit", ReportInfo, "",
			map[string]any{"time": q.Limit.Time.String(), "truncated": true})
	}
	// Shaping what the traversal reached runs under outer: the budget bounds the
	// read, and the answer it produced still has to be returned.
	return finishReached(outer, u, q, reached, routes, bounded, rc, createdReport)
}

// finishReached is the reference post-traversal pipeline (Where, sort, limit, shape)
// over an already-generated set; it finalises the report this call created. bounded
// carries a truncation the traversal already suffered, which limit.results then joins.
func finishReached(ctx context.Context, u Universe, q Query, reached []Claim, routes map[string][]Claim, bounded bool, rc *reportCollector, createdReport bool) (ResultStream, error) {
	filterStart := reportStart(rc)
	var filtered []Claim
	for _, c := range reached {
		if !evalWhere(q.Where, c) {
			continue
		}
		filtered = append(filtered, c)
	}
	rc.timed("native", "filter", ReportInfo, filterStart, "", map[string]any{"in": len(reached), "kept": len(filtered)})

	sortStart := reportStart(rc)
	sortResults(filtered, q.Order)
	rc.timed("native", "sort", ReportInfo, sortStart, "", map[string]any{"ordered": len(q.Order) > 0})

	truncated := bounded
	if q.Limit.Results > 0 && len(filtered) > q.Limit.Results {
		filtered = filtered[:q.Limit.Results]
		truncated = true
	}
	if truncated {
		rc.log("native", "limit", ReportInfo, "", map[string]any{"results": q.Limit.Results, "truncated": true})
	}

	needPaths := q.Output.Shape == ShapePath
	idsOnly := q.Output.Detail == DetailID
	results := make([]QueryResult, 0, len(filtered))
	for _, c := range filtered {
		r := QueryResult{ClaimId: c.ID(), Kind: KindClaimId}
		if needPaths {
			route := routes[c.ID().String()]
			r.PathId = routeIds(route)
			r.Kind = KindPathId
		}
		if !idsOnly { // DetailID asks for identity alone
			r.ClaimNative = c
			r.Kind = KindClaimNative
			if needPaths {
				r.PathNative = routes[c.ID().String()]
				r.Kind = KindPathNative
			}
		}
		results = append(results, r)
	}
	if err := EncodeResults(results, q.Output); err != nil {
		return nil, err
	}

	rc.log("native", "results", ReportInfo, "", map[string]any{"results": len(results)})
	var report *QueryReport
	if createdReport {
		// Finalised before the report joins the sequence, so Results counts the results
		// it reports on and not itself.
		report = rc.finalize(len(results), truncated)
	}
	return &sliceStream{results: AppendReport(results, report)}, nil
}

// --- Where evaluation ------------------------------------------------------

// evalWhere evaluates the boolean tree against one claim; an empty node passes.
func evalWhere(w *Where, c Claim) bool {
	if w == nil {
		return true
	}
	switch {
	case len(w.And) > 0:
		for i := range w.And {
			if !evalWhere(&w.And[i], c) {
				return false
			}
		}
		return true
	case len(w.Or) > 0:
		for i := range w.Or {
			if evalWhere(&w.Or[i], c) {
				return true
			}
		}
		return false
	case w.Not != nil:
		return !evalWhere(w.Not, c)
	case w.Test != nil:
		v, ok := queryFieldValue(c, w.Field)
		if !ok {
			return false // a claim lacking the field never matches
		}
		return evalComparison(*w.Test, v)
	default:
		return true
	}
}

// queryFieldValue resolves a claim field by name — the seam between RQL and the node
// model. Reserved fields have dedicated accessors; the rest are open node fields.
func queryFieldValue(c Claim, field string) (any, bool) {
	n := c.Node()
	switch field {
	case "id":
		return c.ID().String(), true
	case "type":
		return n.Type(), true
	case "encoding":
		return n.Encoding(), n.ContentKind() != ContentNone
	case "created_at":
		return n.CreatedAt(), true
	case "dated":
		return n.Dated(), n.Dated() != ""
	case "content_size":
		// content_size and encoding are emitted only with content (§Content).
		return n.GetContentSize(), n.ContentKind() != ContentNone
	case "height":
		return n.Height(), true
	default:
		v, err := n.GetField(field)
		if err != nil {
			return nil, false
		}
		return v, true
	}
}

// evalComparison applies the one operator set on cmp to the actual field value.
func evalComparison(cmp Comparison, actual any) bool {
	switch {
	case cmp.Eq != nil:
		return valuesEqual(actual, cmp.Eq)
	case cmp.Ne != nil:
		return !valuesEqual(actual, cmp.Ne)
	case cmp.Lt != nil:
		c, ok := compareOrdered(actual, cmp.Lt)
		return ok && c < 0
	case cmp.Le != nil:
		c, ok := compareOrdered(actual, cmp.Le)
		return ok && c <= 0
	case cmp.Gt != nil:
		c, ok := compareOrdered(actual, cmp.Gt)
		return ok && c > 0
	case cmp.Ge != nil:
		c, ok := compareOrdered(actual, cmp.Ge)
		return ok && c >= 0
	case cmp.In != nil:
		for _, w := range cmp.In {
			if valuesEqual(actual, w) {
				return true
			}
		}
		return false
	case cmp.Glob != "":
		ok, _ := path.Match(cmp.Glob, toStringValue(actual))
		return ok
	}
	return false
}

// valuesEqual compares two values with light coercion: numbers by magnitude,
// times as instants, everything else by string form (covers strings, bools).
func valuesEqual(a, b any) bool {
	if af, aok := asFloat(a); aok {
		if bf, bok := asFloat(b); bok {
			return af == bf
		}
	}
	if at, aok := asTime(a); aok {
		if bt, bok := asTime(b); bok {
			return at.Equal(bt)
		}
	}
	return toStringValue(a) == toStringValue(b)
}

// compareOrdered returns -1/0/1 and whether the two values are order-comparable
// (both numeric, both times/RFC3339, or both strings).
func compareOrdered(a, b any) (int, bool) {
	if af, aok := asFloat(a); aok {
		if bf, bok := asFloat(b); bok {
			switch {
			case af < bf:
				return -1, true
			case af > bf:
				return 1, true
			default:
				return 0, true
			}
		}
	}
	if at, aok := asTime(a); aok {
		if bt, bok := asTime(b); bok {
			switch {
			case at.Before(bt):
				return -1, true
			case at.After(bt):
				return 1, true
			default:
				return 0, true
			}
		}
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return strings.Compare(as, bs), true
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

func asTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		if p, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return p, true
		}
		if p, err := time.Parse(time.RFC3339, t); err == nil {
			return p, true
		}
	}
	return time.Time{}, false
}

func toStringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// --- ordering, content, stream ---------------------------------------------

// sortResults orders claims by the order keys, then naturally by (created_at, id).
func sortResults(claims []Claim, keys []OrderKey) {
	sort.SliceStable(claims, func(i, j int) bool {
		ci, cj := claims[i], claims[j]
		for _, k := range keys {
			vi, oki := queryFieldValue(ci, k.Field)
			vj, okj := queryFieldValue(cj, k.Field)
			if oki != okj {
				return oki // present before absent, regardless of direction
			}
			if !oki {
				continue // both lack this key — try the next
			}
			c := compareValues(vi, vj, k.Compare)
			if c == 0 {
				continue
			}
			if k.Dir == SortDesc {
				return c > 0
			}
			return c < 0
		}
		// Natural order: (created_at, id) — a total order for stable ties.
		ti, tj := ci.Node().CreatedAt(), cj.Node().CreatedAt()
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return ci.ID().String() < cj.ID().String()
	})
}

// compareValues compares two field values under the collation: numeric coerces to float
// (string when either isn't), lexical by string, the default numeric-aware then string.
func compareValues(a, b any, col Collation) int {
	switch col {
	case CompareNumeric:
		if af, aok := asFloat(a); aok {
			if bf, bok := asFloat(b); bok {
				switch {
				case af < bf:
					return -1
				case af > bf:
					return 1
				default:
					return 0
				}
			}
		}
		return strings.Compare(toStringValue(a), toStringValue(b))
	case CompareLexical:
		return strings.Compare(toStringValue(a), toStringValue(b))
	case CompareTemporal:
		am, aok := temporalMidpointMs(a)
		bm, bok := temporalMidpointMs(b)
		switch {
		case aok && bok:
			switch {
			case am < bm:
				return -1
			case am > bm:
				return 1
			default:
				return 0
			}
		case aok != bok:
			// Neither timestamp nor valid EDTF sorts with the field-absent claims
			// (R-QTEMPORAL) — push it after the one that parses.
			if aok {
				return -1
			}
			return 1
		default:
			return 0
		}
	default:
		if c, ok := compareOrdered(a, b); ok {
			return c
		}
		return strings.Compare(toStringValue(a), toStringValue(b))
	}
}

// temporalMidpointMs is v's span midpoint in ms (`R-QTEMPORAL`): a time.Time is its
// own zero-width instant, a string parses as EDTF Level 1.
func temporalMidpointMs(v any) (int64, bool) {
	switch x := v.(type) {
	case time.Time:
		return floorDivInt64(x.UnixNano(), int64(time.Millisecond)), true
	case string:
		return edtfMidpointMs(x)
	default:
		return 0, false
	}
}

// sliceStream is the reference ResultStream: a materialised slice, one element at a
// time, the report among them where one was asked for.
type sliceStream struct {
	results []QueryResult
	i       int
}

func (s *sliceStream) Next() bool {
	if s.i < len(s.results) {
		s.i++
		return true
	}
	return false
}
func (s *sliceStream) Result() QueryResult  { return s.results[s.i-1] }
func (s *sliceStream) Report() *QueryReport { return ReportOf(s.results) }
func (s *sliceStream) Err() error           { return nil }
func (s *sliceStream) Close() error         { return nil }

// GetFromClosure returns the claim at id when a reference-edge walk from heads reaches
// it within scope branch, else ErrNotFound. heads are the scope roots, unioned.
func GetFromClosure(ctx context.Context, u Universe, branch string, heads []Id, id Id) (Claim, error) {
	if id == nil {
		return nil, errNilID
	}
	// Fast path, real branches only: the tagger stamps every member with _b_<branch>,
	// so a present tag proves membership in O(1); a missing one falls through to the walk.
	if branch != "" && branch != BranchUniverse && branch != BranchArchive {
		if c, err := GetClaim(ctx, u, id); err == nil && c.Tag(BranchTagKey(branch)) != "" {
			return c, nil
		}
	}
	seen := map[string]struct{}{}
	queue := make([]Id, 0, len(heads))
	for _, h := range heads {
		if h != nil {
			queue = append(queue, h)
		}
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		k := cur.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		if cur.Equal(id) {
			return GetClaim(ctx, u, id) // reached — materialise and return, no expansion
		}
		c, err := GetClaim(ctx, u, cur, WithNotDiffMaterialized())
		if err != nil {
			return nil, err // a gap on the path to id is a real error
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}
	return nil, ErrNotFound
}

// InClosure reports whether id is reachable within scope branch from any of
// heads — GetFromClosure without materialising the claim.
func InClosure(ctx context.Context, u Universe, branch string, heads []Id, id Id) (bool, error) {
	_, err := GetFromClosure(ctx, u, branch, heads, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
