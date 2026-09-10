// package: ranke / graph
// type:    logic
// job:     a Ranke-Graph handle RG ⊆ 𝒰 — stages claims into a Universe under the atomic-creation
// rule, tracks open heads, consolidates
// limits:  claims live in the Universe (-> universe); does not bind graphs to branches
// (-> archive); verification lives in verify.go
package ranke

import (
	"bytes"
	"context"
	"sort"
	"time"
)

// Graph is a Ranke-Graph handle: a subset RG ⊆ 𝒰 (spec §4.5). It holds the
// open-head frontier; membership is closure(heads, 𝒰), resolved on demand.
type Graph interface {
	// AddClaims stores claims in the Universe and advances the open-head
	// frontier: each claim becomes a head, the ids it references drop out.
	AddClaims(ctx context.Context, claims ...Claim) error
	// ContainsClaim reports whether id is in the closure of an open head.
	ContainsClaim(ctx context.Context, id Id) (bool, error)
	// GetClaim loads the claim at id if it is reachable from an open head.
	GetClaim(ctx context.Context, id Id) (Claim, error)
	// Heads returns the open heads — the frontier claims (§4.5). A single
	// head makes the graph a closure RG_h.
	Heads() []Id
	// IsConsolidated reports whether one claim already reaches everything the graph
	// needs reached — the open frontier and whatever Cite named.
	IsConsolidated() bool
	// Cite makes ids roots the next Consolidate wraps alongside the open heads, for a
	// claim that must be reachable from the head in its own right — one sitting behind
	// a claim scheduled for deletion (§Deletion).
	Cite(ids ...Id)
	// Consolidate wraps every open head in one head claim, adds it, returns it.
	// createdAt defaults to now and must satisfy monotonicity (§4.3).
	Consolidate(ctx context.Context, contributor Contributor, createdAt ...time.Time) (Claim, error)
	// Verify walks the closure from every open head and verifies each claim
	// (§5.10 integrity + authenticity), returning a live run.
	Verify(opts ...VerifyOption) VerificationRun
}

// graph is the concrete Graph: a Universe plus the open-head frontier, and whatever
// Cite named as needing a head reference of its own.
type graph struct {
	u     Universe
	heads map[string]Id
	cited map[string]Id
}

func (g *graph) Cite(ids ...Id) {
	if g.cited == nil {
		g.cited = map[string]Id{}
	}
	for _, id := range ids {
		if id != nil {
			g.cited[id.String()] = id
		}
	}
}

// roots are the claims a head claim must reach: the open frontier, plus what Cite
// named, in a stable order so consolidation reproduces.
func (g *graph) roots() []Id {
	out := g.Heads()
	for _, id := range g.cited {
		if _, open := g.heads[id.String()]; !open {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(idBytes(out[i]), idBytes(out[j])) < 0
	})
	return out
}

// NewGraph opens an empty graph over u; nil u gets an ephemeral memory Universe.
func NewGraph(ctx context.Context, u Universe) (Graph, error) {
	if u == nil {
		u = NewMemoryUniverse()
	}
	return &graph{u: u, heads: map[string]Id{}}, nil
}

// NewGraphFromClosure opens a graph at an existing head in universe; per
// §Closures the id alone recovers RG_head = closure(head, 𝒰) on demand.
func NewGraphFromClosure(ctx context.Context, head Id, universe Universe) (Graph, error) {
	if universe == nil {
		return nil, errNilUniverse
	}
	if head == nil {
		return nil, errNilID
	}
	return &graph{u: universe, heads: map[string]Id{head.String(): head}}, nil
}

// AddClaims stores each claim and moves the ids it references out of the
// frontier — they are interior now.
func (g *graph) AddClaims(ctx context.Context, claims ...Claim) error {
	for _, cl := range claims {
		if cl == nil {
			return errNilClaim
		}
		if err := PutClaim(ctx, g.u, cl); err != nil {
			return WrapDetail(errGraphAddClaim, cl.ID().String(), err)
		}
		g.heads[cl.ID().String()] = cl.ID()
		for _, e := range cl.Edges() {
			delete(g.heads, e.Reference().String())
		}
	}
	return nil
}

// ContainsClaim asks the Universe whether id is in the closure of an open head.
func (g *graph) ContainsClaim(ctx context.Context, id Id) (bool, error) {
	if id == nil {
		return false, nil
	}
	return InClosure(ctx, g.u, BranchUniverse, g.Heads(), id)
}

// GetClaim loads the claim at id from the closure of the open heads.
func (g *graph) GetClaim(ctx context.Context, id Id) (Claim, error) {
	return GetFromClosure(ctx, g.u, BranchUniverse, g.Heads(), id)
}

func (g *graph) Heads() []Id {
	out := make([]Id, 0, len(g.heads))
	for _, id := range g.heads {
		out = append(out, id)
	}
	return out
}

func (g *graph) IsConsolidated() bool {
	return len(g.roots()) == 1
}

func (g *graph) Consolidate(ctx context.Context, contributor Contributor, createdAt ...time.Time) (Claim, error) {
	heads := g.roots()
	if len(heads) == 0 {
		return nil, errEmptyGraph
	}
	if len(heads) == 1 {
		// Already consolidated — consolidate(RG) = RG (§Consolidation).
		return GetClaim(ctx, g.u, heads[0])
	}
	edges := make([]Edge, 0, len(heads))
	// refs is the height basis (§4.1): the consolidated heads plus the contributor.
	refs := make([]Claim, 0, len(heads)+1)
	refs = append(refs, contributor)
	for _, h := range heads {
		hc, err := GetClaim(ctx, g.u, h)
		if err != nil {
			return nil, WrapDetail(errConsolidate, "load head", err)
		}
		refs = append(refs, hc)
		// The head is in hand, so its edge is built carrying whatever the head declares.
		e, err := NewEdge(EdgeConfig{Reference: h, Referenced: hc, Type: EdgeTypeHead})
		if err != nil {
			return nil, WrapDetail(errConsolidate, "build head edge", err)
		}
		edges = append(edges, e)
	}
	head, err := ClaimBuilder{
		Type:        NodeHead,
		Contributor: contributor,
		Edges:       edges,
		CreatedAt:   firstNonZero(createdAt),
		Height:      FixedHeight(HeightOf(refs...)),
	}.Sign()
	if err != nil {
		return nil, err
	}
	if err := g.AddClaims(ctx, head); err != nil {
		return nil, WrapDetail(errConsolidate, "add head", err)
	}
	return head, nil
}

// firstNonZero returns the first non-zero time in ts, else the zero time.
func firstNonZero(ts []time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// Verify lives in verify.go (the configurable closure verifier).
