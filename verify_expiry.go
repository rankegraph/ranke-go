// package: ranke / verify
// type:    logic
// job:     `R-DEXPIRY`'s second sentence — the contribution/expiry edge that moves the end of a
// contributor's key window, resolved once per contributor per run
// limits:  finds the edge, never applies the window (-> verify for the comparison)
package ranke

import (
	"context"
	"sync"
	"time"
)

// expiryIndex resolves the earliest end any contribution/expiry edge imposes on a
// contributor (`R-DEXPIRY`), memoised for the run.
//
// A revocation is not reachable from the claim it revokes — both point AT the
// contributor, and edges run backwards in time. `R-C3LIMIT` is what makes it findable:
// the branch holding a claim holds every limiting claim against it, so the edge lies in
// the closure the run already walks, which is what this searches.
type expiryIndex struct {
	roots []Id

	mu sync.Mutex
	by map[string]*time.Time // contributor id → earliest imposed end, nil when none
}

// newExpiryIndex indexes over the closure roots the run walks.
func newExpiryIndex(roots []Id) *expiryIndex {
	return &expiryIndex{roots: roots, by: map[string]*time.Time{}}
}

// endFor returns the earliest pubkey_expires_after imposed on contributor, or nil where no
// expiry edge names it. One closure sweep per contributor, held for the rest of the run.
func (x *expiryIndex) endFor(ctx context.Context, u Universe, contributor Id) (*time.Time, error) {
	if x == nil || contributor == nil || u == nil {
		return nil, nil
	}
	key := contributor.String()
	x.mu.Lock()
	defer x.mu.Unlock()
	if end, ok := x.by[key]; ok {
		return end, nil
	}
	end, err := sweepForExpiry(ctx, u, x.roots, contributor)
	if err != nil {
		return nil, err
	}
	x.by[key] = end
	return end, nil
}

// sweepForExpiry walks the closure from roots for contribution/expiry edges naming
// contributor, returning the earliest end they carry. Several revocations may name one
// key — a second, earlier one only shortens the window further.
func sweepForExpiry(ctx context.Context, u Universe, roots []Id, contributor Id) (*time.Time, error) {
	var earliest *time.Time
	seen := map[string]struct{}{}
	queue := append([]Id{}, roots...)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		if cur == nil {
			continue
		}
		k := cur.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		c, err := GetClaim(ctx, u, cur, WithNotDiffMaterialized())
		if err != nil {
			continue // a gap the walk itself judges (`R-DGAP`); it carries no edges to read
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
			if e.Type() != EdgeTypeExpiry || !e.Reference().Equal(contributor) {
				continue
			}
			at, err := edgeBound(e, FieldPubkeyExpiresAfter)
			if err != nil {
				return nil, err
			}
			if at != nil && (earliest == nil || at.Before(*earliest)) {
				earliest = at
			}
		}
	}
	return earliest, nil
}

// edgeBound reads one RFC 3339 bound off an edge, absent reported as nil. `R-DEXPIRY`
// puts the date on the EDGE rather than its claim, which is what lets a successor
// contribution/contributor claim carry one: its own fields state its own key's window.
func edgeBound(e Edge, field string) (*time.Time, error) {
	v, err := e.GetField(field)
	if err != nil {
		return nil, nil
	}
	t, err := parseRFC3339Nano(v)
	if err != nil {
		return nil, WrapDetail(ErrKeyWindowField, field+"="+v, err)
	}
	return &t, nil
}
