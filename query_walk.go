// package: ranke / query walk
// type:    logic
// job:     the reference RQL traversal — walk a Select's Path from its root (forward, and reverse
// via a built closure-inversion index), returning the reached set and each claim's
// canonical route
// limits:  reachability only; filter/order/limit/shape live in query_default.go. Route ties break
// on the (created_at, id) total order so a path shape is byte-identical to a Cypher
// lowering
package ranke

import (
	"context"
	"path"
	"strings"
)

// queryTraverse walks the Select's Path a step at a time within closure(origin),
// returning the claims reached that conf admits and each route when needPaths is set.
// Only the first step passes through claims outside the scope, since every later one
// expands from what the previous collected.
func queryTraverse(ctx context.Context, u Universe, sel Select, origin Id, conf *confinement, needPaths bool, rc *reportCollector) ([]Claim, map[string][]Claim, error) {
	steps := sel.Path
	if steps == nil {
		steps = []PathStep{{}} // all edges, provenance, unbounded → full closure
	}

	startAt := reportStart(rc)
	frontier, err := walkStart(ctx, u, sel.Claim, origin, conf, rc)
	if err != nil {
		return nil, nil, err
	}
	rc.timed("native", "load-start", ReportInfo, startAt, "", map[string]any{"claims": len(frontier)})

	// A reverse step names referrers, which only an inverted closure yields. It is
	// built over the scope's graph, so a reverse step finds the referrers the
	// branch holds — from origin it would invert whatever graph Select.Head sits in.
	var incoming map[string][]incomingEdge
	if stepsNeedReverse(steps) {
		root := origin
		if conf != nil {
			root = conf.root
		}
		anchor, err := GetClaim(ctx, u, root)
		if err != nil {
			return nil, nil, WrapDetail(errQuery, "closure origin "+root.String(), err)
		}
		idxStart := reportStart(rc)
		incoming, err = buildIncoming(ctx, u, anchor, rc)
		if err != nil {
			return nil, nil, err
		}
		rc.timed("native", "reverse-index", ReportInfo, idxStart, "", map[string]any{"targets": len(incoming)})
	}

	routes := map[string][]Claim{}
	for _, c := range frontier {
		routes[c.ID().String()] = []Claim{c}
	}
	// An empty Path takes no step, so the frontier is the answer and the scope admits
	// it here rather than at a collection (`R-QSTEPS`, `R-QCSCOPE`).
	reached := admitted(frontier, conf)
	for i, step := range steps {
		stepStart := reportStart(rc)
		reached, err = queryWalkStep(ctx, u, frontier, step, incoming, routes, conf, needPaths, rc)
		if err != nil {
			// Only the last step's output answers the query, so a partial one is the
			// bounded answer (`R-QLIMIT`); an earlier step's frontier is not one.
			if i < len(steps)-1 {
				return nil, routes, err
			}
			return reached, routes, err
		}
		rc.timed("native", "step", ReportInfo, stepStart, "", map[string]any{"index": i, "edges": step.Edges, "dir": string(step.Dir), "depth": step.Max, "reached": len(reached)})
		frontier = reached
	}
	return reached, routes, nil
}

// admitted keeps the claims the scope's graph holds, in the order given.
func admitted(cs []Claim, conf *confinement) []Claim {
	if conf == nil {
		return cs
	}
	out := make([]Claim, 0, len(cs))
	for _, c := range cs {
		if conf.admits(c) {
			out = append(out, c)
		}
	}
	return out
}

// walkStart is the set the first step expands from: the claims Select.Claim anchors,
// each named once (`R-QANCHOR`), else every claim in closure(origin). A start outside
// the scope's graph stands — a read is the intersection, and collection decides.
func walkStart(ctx context.Context, u Universe, start []Id, origin Id, conf *confinement, rc *reportCollector) ([]Claim, error) {
	if len(start) > 0 {
		out := make([]Claim, 0, len(start))
		seen := map[string]bool{}
		for _, id := range start {
			if id == nil || seen[id.String()] {
				continue
			}
			seen[id.String()] = true
			c, err := GetClaim(ctx, u, id)
			if err != nil {
				return nil, WrapDetail(errQuery, "walk start "+id.String(), err)
			}
			out = append(out, c)
		}
		return out, nil
	}
	c, err := GetClaim(ctx, u, origin)
	if err != nil {
		return nil, WrapDetail(errQuery, "closure origin "+origin.String(), err)
	}
	return queryWalkStep(ctx, u, []Claim{c}, PathStep{Min: Hops(0)}, nil, map[string][]Claim{}, conf, false, rc)
}

// stepsNeedReverse reports whether any step walks edges backward (uses or
// connections), so the traversal must build a reverse index first.
func stepsNeedReverse(steps []PathStep) bool {
	for _, s := range steps {
		if s.Dir == DirUses || s.Dir == DirConnections {
			return true
		}
	}
	return false
}

// incomingEdge is one reverse-adjacency entry: a claim that references the
// target, and the type of the edge it did so with.
type incomingEdge struct {
	from     Claim
	edgeType string
}

// buildIncoming sweeps the forward closure from root and inverts its edges into
// a target-id → referrers map, which doubles as the scope-membership set.
func buildIncoming(ctx context.Context, u Universe, root Claim, rc *reportCollector) (map[string][]incomingEdge, error) {
	incoming := map[string][]incomingEdge{}
	seen := map[string]bool{root.ID().String(): true}
	queue := []Claim{root}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		for _, e := range cur.Edges() {
			ref := e.Reference()
			k := ref.String()
			incoming[k] = append(incoming[k], incomingEdge{from: cur, edgeType: e.Type()})
			if seen[k] {
				continue
			}
			seen[k] = true
			child, err := GetClaim(ctx, u, ref)
			if err != nil {
				return nil, WrapDetail(errQuery, "reverse-index "+k, err)
			}
			rc.log("native", "fetch", ReportTrace, k, nil)
			queue = append(queue, child)
		}
	}
	return incoming, nil
}

// queryWalkStep expands the frontier along one PathStep: level-synchronized BFS
// over *0..step.Max hops (0 = unbounded, frontier is hop 0) along Dir's edges,
// collecting step.Nodes matches. Equal routes break on (created_at, id).
func queryWalkStep(ctx context.Context, u Universe, frontier []Claim, step PathStep, incoming map[string][]incomingEdge, routes map[string][]Claim, conf *confinement, needPaths bool, rc *reportCollector) ([]Claim, error) {
	// Three sets, one meaning each. visited: walked through by this step. fixed:
	// route settled by an earlier, hence shorter, level. collected: in the result.
	visited := map[string]bool{}
	fixed := map[string]bool{}
	collected := map[string]bool{}
	minHops := step.MinHops()

	var out []Claim
	// reach records that a path of hop edges arrives at c. Arrival is independent
	// of routing: a path coming back to a frontier node still yields a result.
	reach := func(c Claim, hop int) {
		k := c.ID().String()
		if hop < minHops || collected[k] || !conf.admits(c) || !matchTypeList(step.Nodes, c.Node().Type()) {
			return
		}
		collected[k] = true
		out = append(out, c)
	}

	var level []Claim // the current BFS level; the frontier is hop 0
	for _, f := range frontier {
		k := f.ID().String()
		if visited[k] {
			continue
		}
		visited[k] = true
		level = append(level, f)
		reach(f, 0)
		fixed[k] = collected[k] // a hop-0 answer keeps its own route
	}

	forward := step.Dir == "" || step.Dir == DirProvenance || step.Dir == DirConnections
	reverse := step.Dir == DirUses || step.Dir == DirConnections

	for hop := 0; len(level) > 0; hop++ {
		if err := ctx.Err(); err != nil {
			return out, err // partial, so a bounded read keeps what it reached
		}
		if step.Max > 0 && hop >= step.Max {
			break
		}
		// Route per child is kept, not finalised, until the level ends — so a
		// second parent reaching that child at this level can still win the tie.
		type cand struct {
			claim Claim
			route []Claim // nil unless needPaths
		}
		best := map[string]cand{} // child id → best candidate so far
		var order []string        // children first seen this level, for stable finalisation
		// consider offers child as a route candidate; arrival is recorded first.
		consider := func(cur, child Claim) {
			k := child.ID().String()
			reach(child, hop+1)
			if fixed[k] {
				return // a shorter route already won
			}
			var route []Claim
			if needPaths {
				parent := routes[cur.ID().String()]
				route = make([]Claim, len(parent), len(parent)+1)
				copy(route, parent)
				route = append(route, child)
			}
			prev, ok := best[k]
			if !ok {
				best[k] = cand{child, route}
				order = append(order, k)
			} else if needPaths && lessRoute(route, prev.route) {
				best[k] = cand{child, route}
			}
		}
		for _, cur := range level {
			if forward {
				for _, e := range cur.Edges() {
					if !matchTypeList(step.Edges, e.Type()) {
						continue
					}
					ref := e.Reference()
					child, err := GetClaim(ctx, u, ref)
					if err != nil {
						// A ctx-respecting backend surfaces the deadline here rather than at
						// the hop-loop head, so this path carries the partial set too.
						return out, WrapDetail(errQuery, "traverse "+ref.String(), err)
					}
					rc.log("native", "fetch", ReportTrace, ref.String(), nil)
					consider(cur, child)
				}
			}
			if reverse {
				// Incoming edges come from the reverse index, already within the
				// scope closure — no fetch, and no way to leave the branch.
				for _, ie := range incoming[cur.ID().String()] {
					if !matchTypeList(step.Edges, ie.edgeType) {
						continue
					}
					rc.log("native", "reverse", ReportTrace, ie.from.ID().String(), nil)
					consider(cur, ie.from)
				}
			}
		}
		var next []Claim
		for _, k := range order {
			c := best[k]
			if needPaths {
				routes[k] = c.route
			}
			fixed[k] = true
			if visited[k] {
				continue // no-repeat: this step has already walked through it
			}
			visited[k] = true
			next = append(next, c.claim)
		}
		level = next
	}
	return out, nil
}

// lessRoute reports whether route a sorts before b under the query total order:
// shorter first, then node-by-node on (created_at, id), as the Cypher ORDER BY.
func lessRoute(a, b []Claim) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	for i := range a {
		ta, tb := a[i].Node().CreatedAt(), b[i].Node().CreatedAt()
		if !ta.Equal(tb) {
			return ta.Before(tb)
		}
		if ida, idb := a[i].ID().String(), b[i].ID().String(); ida != idb {
			return ida < idb
		}
	}
	return false
}

// matchTypeList reports whether typ satisfies a type-pattern list of path.Match
// globs over "class/sub", where a leading "-" excludes. Only negatives, or none
// at all, means "match unless excluded".
func matchTypeList(patterns []string, typ string) bool {
	if len(patterns) == 0 {
		return true
	}
	hasPositive, matchedPositive := false, false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "-")
		pat := strings.TrimPrefix(p, "-")
		ok, _ := path.Match(pat, typ)
		if neg {
			if ok {
				return false
			}
			continue
		}
		hasPositive = true
		if ok {
			matchedPositive = true
		}
	}
	return !hasPositive || matchedPositive
}
