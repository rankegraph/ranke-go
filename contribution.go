// package: ranke / contribution
// type:    logic
// job:     the contribution contract — the staged → verified → mergable advance of a Ranke-Archive
// (RA_k → RA_k')
// limits:  the contract plus the shared wire drain; opening, verifying and merging a contribution
// are a Sequencer's (-> adapter/sequencer/dev, adapter/sequencer/concurrent)
package ranke

import (
	"context"
	"errors"
	"slices"
	"time"
)

// Constraints are what a contribution may do: the caller declares them and the
// Sequencer guarantees them, so an access system is built by composing them.
type Constraints struct {
	lifted       map[string]bool // reserved node types this contribution may carry
	referencable []string        // branches it may reference claims from
	creatable    []string        // branches it may bring into being
}

// ContributionOption sets one constraint.
type ContributionOption func(*Constraints)

// NewConstraints applies opts. The zero value is the strictest reading: every
// reserved type refused.
func NewConstraints(opts ...ContributionOption) Constraints {
	var c Constraints
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WithLiftedTypes admits node types otherwise reserved to the Sequencer, so a caller
// holding it can create what it would mint.
func WithLiftedTypes(types ...string) ContributionOption {
	return func(c *Constraints) {
		if c.lifted == nil {
			c.lifted = make(map[string]bool, len(types))
		}
		for _, t := range types {
			c.lifted[t] = true
		}
	}
}

// WithReferencableBranches names the branches a contribution may reference claims
// from (step 3). BranchArchive admits every branch, BranchUniverse the whole 𝒰.
func WithReferencableBranches(branches ...string) ContributionOption {
	return func(c *Constraints) { c.referencable = append(c.referencable, branches...) }
}

// WithCreatableBranches names the branches a contribution may bring into being, a
// branch the base does not carry being created by the merge that names it.
// TargetBranches admits any, the surface §Access holds a creation grant against.
func WithCreatableBranches(branches ...string) ContributionOption {
	return func(c *Constraints) { c.creatable = append(c.creatable, branches...) }
}

// AdmitBranch reports whether the contribution may write to branch, which the base
// carrying it settles: creating one is a right of its own (§Access, C over $branches).
func (c Constraints) AdmitBranch(ctx context.Context, base Archive, branch string) error {
	// Before the grant: a name outside the form is refused however wide the grant,
	// since one shaped like a reserved target would be shadowed at read time.
	if err := ValidateBranchName(branch); err != nil {
		return err
	}
	_, err := base.GetBranch(ctx, branch)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, ErrBranchNotFound):
		return err
	case slices.Contains(c.creatable, branch),
		slices.Contains(c.creatable, TargetBranches):
		return nil
	}
	return WithDetail(ErrBranchNotCreatable, branch)
}

// AdmitReferences enforces step 3's read requirement: every claim the contribution's
// own reference, other than each other, must lie in a scope it may read.
func (c Constraints) AdmitReferences(ctx context.Context, u Universe, base Archive, branch string, own []Claim) error {
	if c.unconfined() {
		return nil
	}
	mine := make(map[string]bool, len(own))
	for _, cl := range own {
		mine[cl.ID().String()] = true
	}
	var refs []Id
	outside := map[string]bool{}
	for _, cl := range own {
		for _, e := range cl.Edges() {
			k := e.Reference().String()
			if mine[k] || outside[k] {
				continue
			}
			outside[k] = true
			refs = append(refs, e.Reference())
		}
	}
	if len(refs) == 0 {
		return nil
	}

	allowed, err := c.allowedHeads(ctx, base, branch)
	if err != nil {
		return err
	}
	held, err := u.ClaimsInBranches(ctx, allowed, refs)
	if err != nil {
		return Wrap(errContributionRefs, err)
	}
	for i, ok := range held {
		if !ok {
			return WithDetail(ErrUnreadableReference, refs[i].String())
		}
	}
	return nil
}

// allowedHeads resolves each readable scope to the head whose closure it is, the
// branch being written included: a claim already on it needs no read grant.
func (c Constraints) allowedHeads(ctx context.Context, base Archive, branch string) (map[string]Id, error) {
	names := append([]string{branch}, c.referencable...)
	// The archive is every branch's superset, so it answers for all of them at once.
	if slices.Contains(names, BranchArchive) {
		return map[string]Id{BranchArchive: base.Head()}, nil
	}
	heads := make(map[string]Id, len(names))
	for _, name := range names {
		b, err := base.GetBranch(ctx, name)
		switch {
		case errors.Is(err, ErrBranchNotFound):
			continue // absent from the base, so it holds nothing to read
		case err != nil:
			return nil, err
		}
		heads[name] = b.Head()
	}
	return heads, nil
}

// unconfined reports BranchUniverse among the referencable branches, where every
// claim in 𝒰 is readable.
func (c Constraints) unconfined() bool {
	return slices.Contains(c.referencable, BranchUniverse)
}

// reservedTypes are the Sequencer's alone (`R-C2TYPE`): the branch table it mints at
// merge, and the limiting claims that restrict another claim.
var reservedTypes = map[string]bool{
	NodeBranches: true,
	NodeDelete:   true,
	NodeExpiry:   true,
}

// StrandedByDeletion reports which of own a walk from heads reaches only through a
// claim carrying delete_by (`R-DPLANNED`): deleting one removes the record its edges lived
// in, so a walk stops at the gap while what lay behind stays present.
//
// The search stays inside own — a claim is referenced only by claims made after it, so
// nothing already in the archive leads to one this contribution brings.
func StrandedByDeletion(ctx context.Context, u Universe, heads []Id, own []Claim) ([]Id, error) {
	mine := make(map[string]Claim, len(own))
	for _, c := range own {
		mine[c.ID().String()] = c
	}
	reached := map[string]bool{}
	queue := append([]Id{}, heads...)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := queue[0]
		queue = queue[1:]
		if id == nil || reached[id.String()] {
			continue
		}
		c, ok := mine[id.String()]
		if !ok {
			continue // already in the archive, and no path from there back into own
		}
		if scheduledForDeletion(c) {
			continue // its edges go with its bytes, so a walk stops here
		}
		reached[id.String()] = true
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}

	var orphaned []Id
	for _, c := range own {
		if !reached[c.ID().String()] && !scheduledForDeletion(c) {
			orphaned = append(orphaned, c.ID())
		}
	}
	return orphaned, nil
}

func scheduledForDeletion(c Claim) bool {
	_, err := c.Node().GetField(FieldDeleteBy)
	return err == nil
}

// CheckDeletable reports whether a claim of this type may schedule its own removal
// (`R-DSTRUCT`). Four subtypes may not, each being what another rule reads: a
// contributor's pubkey (`V-SIG`), the chain to the initial table (`V-ARCHIVE`), a
// gap's explanation (`R-DGAP`), a key's window (`R-DEXPIRY`). Subtypes beyond them
// are open vocabulary (`V-TYPE`), so any other claim MAY.
func CheckDeletable(class NodeClass, sub string, fields map[string]string) error {
	if class != NodeClassContribution || !undeletableSubtype[sub] {
		return nil
	}
	if _, ok := fields[FieldDeleteBy]; ok {
		return WithDetail(ErrStructureNotDeletable, string(class)+"/"+sub)
	}
	return nil
}

// undeletableSubtype is `R-DSTRUCT`'s closed set, by subtype.
var undeletableSubtype = map[string]bool{
	string(NodeSubtypeContributor): true,
	string(NodeSubtypeBranches):    true,
	"delete":                       true,
	"expiry":                       true,
}

// AdmitCreatedAt applies step 2's timestamp rule: a claim is dated at or before the
// base time t (§Timestamping). Both Sequencers call it, so the rule reads one way.
func AdmitCreatedAt(c Claim, base time.Time) error {
	if t := c.Node().CreatedAt(); t.After(base) {
		return WithDetail(ErrFutureDated,
			c.ID().String()+" dated "+t.Format(iso8601Nano)+", base "+base.Format(iso8601Nano))
	}
	return nil
}

// AdmitType reports whether a claim of nodeType may be added under these
// constraints.
func (c Constraints) AdmitType(nodeType string) error {
	if reservedTypes[nodeType] && !c.lifted[nodeType] {
		return WithDetail(ErrReservedType, nodeType)
	}
	return nil
}

// Contribution is an in-progress advance of a Ranke-Archive: open it against a
// base RA_k, fill it, then CompleteAndVerify seals and verifies it.
type Contribution interface {
	// Base is the (k, t) the contribution was opened against.
	Base() (head Id, t time.Time)
	// AddClaims fills the contribution (step 2), naming the branch the claims join.
	// Several may be named, and an empty one is an error. Passing the claims is what
	// lets step 2's rules reach every one of them.
	AddClaims(branch string, claims []Claim) error
	// AddWire fills from a WireMediaType stream, which declares its own branches.
	// It takes the reader, so a caller that checked Branches hands on the same one.
	AddWire(ctx context.Context, wr *WireReader) error
	// CompleteAndVerify closes the contribution over its base and verifies it
	// (steps 3–4), yielding a sealed VerifiedContribution.
	CompleteAndVerify(ctx context.Context) (VerifiedContribution, error)
}

// DrainWire fills c as records arrive: content lands in u under its hash, each
// claim stages under its branch, and an undeclared branch stops the fill.
func DrainWire(ctx context.Context, c Contribution, u Universe, wr *WireReader) error {
	for wr.Next() {
		switch rec := wr.Record(); rec.Kind {
		case WireContent:
			if err := u.PutContents(ctx, []ContentBlob{rec.Blob}); err != nil {
				return err
			}
		case WireClaim:
			if err := c.AddClaims(rec.Branch, []Claim{rec.Claim}); err != nil {
				return err
			}
		}
	}
	return wr.Err()
}

// VerifiedContribution is a sealed, verified contribution: its contents
// are fixed, so by immutability whatever verified stays valid.
type VerifiedContribution interface {
	// Ids are the claim ids the contribution adds.
	Ids() []Id
	// Persist writes the sealed closure to 𝒰 (step 5), yielding a
	// MergableContribution the Sequencer can merge.
	Persist(ctx context.Context) (MergableContribution, error)
}

// MergableContribution is ready for step 6: its claims are durably in 𝒰 and its
// head claim(s) are known.
type MergableContribution interface {
	// Heads are the open head ids per branch, which the new branch-table claim
	// references under those names.
	Heads() map[string][]Id
}
