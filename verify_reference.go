// package: ranke / verify
// type:    logic
// job:     the per-claim rules re-derived from a claim's direct references — `V-HEIGHT` height
// and `V-MONO` created_at, which the closure walk makes transitive
// limits:  judges one claim against its references only (-> verify for the walk and the registry)
package ranke

import (
	"context"
	"strconv"
	"time"
)

// referenceIds is the claim's direct references, in edge order.
func referenceIds(c Claim) []Id {
	edges := c.Edges()
	ids := make([]Id, len(edges))
	for i, e := range edges {
		ids[i] = e.Reference()
	}
	return ids
}

// verifyHeight re-derives the claim's generation number from its committed
// references and enforces §4.1 (`V-HEIGHT`): height == 1 + max(reference heights),
// 0 for an initial claim. The closure walk makes this single-level check transitive.
// A deleted reference takes its height with it, so the equality becomes a lower bound;
// the walk owns whether that absence is lawful (`R-DGAP`).
func verifyHeight(ctx context.Context, c Claim, u Universe) error {
	var want uint64
	complete := true
	if ids := referenceIds(c); len(ids) > 0 {
		present, err := u.HasClaims(ctx, ids)
		if err != nil {
			return WrapDetail(errVerify, "resolve reference presence", err)
		}
		held := make([]Id, 0, len(ids))
		for i, ok := range present {
			if ok {
				held = append(held, ids[i])
				continue
			}
			complete = false
		}
		var max uint64
		if len(held) > 0 {
			heights, err := u.GetClaimHeights(ctx, held)
			if err != nil {
				return WrapDetail(errVerify, "resolve reference heights", err)
			}
			for _, h := range heights {
				if h > max {
					max = h
				}
			}
		}
		want = max + 1
	}
	got := c.Node().Height()
	if complete && got != want {
		return WithDetail(ErrHeightMismatch, "got "+strconv.FormatUint(got, 10)+", want "+strconv.FormatUint(want, 10))
	}
	if !complete && got < want {
		return WithDetail(ErrHeightMismatch,
			"got "+strconv.FormatUint(got, 10)+", want at least "+strconv.FormatUint(want, 10)+" (a deleted reference's height is unrecoverable)")
	}
	return nil
}

// presentReferences is c's references whose bytes the Universe still holds. A rule
// re-derived from a reference can say nothing about one that has lawfully gone.
func presentReferences(ctx context.Context, c Claim, u Universe) ([]Id, error) {
	ids := referenceIds(c)
	if len(ids) == 0 {
		return nil, nil
	}
	present, err := u.HasClaims(ctx, ids)
	if err != nil {
		return nil, WrapDetail(errVerify, "resolve reference presence", err)
	}
	held := make([]Id, 0, len(ids))
	for i, ok := range present {
		if ok {
			held = append(held, ids[i])
		}
	}
	return held, nil
}

// verifyCreatedAtMonotone enforces `V-MONO`: created_at(v) ≥ created_at(u) over v's
// direct references, which the walk carries down the chain as it does for height.
// Equality passes — a contribution commits its claims at one instant (`R-C6REQUEST`).
//
// The dates are read off the referenced claims, the only place a Universe holds them;
// a layer that cannot serve the load fails the rule rather than skipping it. A DELETED
// reference is the exception: its date went with its bytes (`R-DGAP`).
func verifyCreatedAtMonotone(ctx context.Context, c Claim, u Universe) error {
	ids, err := presentReferences(ctx, c, u)
	if err != nil || len(ids) == 0 {
		return err
	}
	refs, err := u.GetClaims(ctx, ids, WithNotDiffMaterialized())
	if err != nil {
		return WrapDetail(errVerify, "resolve reference created_at", err)
	}
	if len(refs) != len(ids) {
		return WithDetail(errVerify, "reference created_at lookup answered "+strconv.Itoa(len(refs))+" of "+strconv.Itoa(len(ids)))
	}
	at := c.Node().CreatedAt()
	for i, ref := range refs {
		if ref == nil {
			return WithDetail(errVerify, "reference unresolved: "+ids[i].String())
		}
		if refAt := ref.Node().CreatedAt(); at.Before(refAt) {
			return WithDetail(ErrCreatedAtNotMonotone, dated(c.ID(), at)+" references "+dated(ids[i], refAt))
		}
	}
	return nil
}

// dated renders "<id> dated <stamp>" for a violation message.
func dated(id Id, at time.Time) string {
	return id.String() + " dated " + at.Format(iso8601Nano)
}
