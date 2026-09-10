// package: ranke / claim_type_branch
// type:    logic
// job:     the Branch view over a contribution/branch edge — a named pointer into an archive's branch
// table — and the form a branch name takes
// limits:  the branch-table logic (materialising the diff chain) lives in the Archive (-> archive);
// a Branch only navigates the subgraph its edge references
package ranke

import (
	"context"
	"io"
	"strconv"
)

// branchNameMax bounds a branch name, as `R-FIELDS` bounds a field name: the label
// rides in every table of the spine, so an unbounded one is carried forever.
const branchNameMax = 128

// ValidateBranchName holds a branch label to the form `R-FIELDS` gives a name —
// `[a-z0-9_]`, no leading underscore, at most 128 bytes. The charset admits no `$`,
// so a branch can never take a reserved target's name (BranchArchive, BranchUniverse,
// TargetBranches) and be shadowed by it at read time.
func ValidateBranchName(name string) error {
	switch {
	case name == "":
		return WithDetail(ErrBranchName, "empty")
	case len(name) > branchNameMax:
		return WithDetail(ErrBranchName, strconv.Itoa(len(name))+" bytes")
	case name[0] == '_':
		return WithDetail(ErrBranchName, name)
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return WithDetail(ErrBranchName, name)
		}
	}
	return nil
}

// Branch is one entry of an archive's branch table — the named
// contribution/branch edge itself, plus navigation into its subgraph.
type Branch interface {
	Edge

	// Name is the branch name — the edge's "name" field.
	Name() string
	// Head is the root of the branch's subgraph — the edge's Reference().
	Head() Id

	// Prev is the previous revision of this branch: the branch pointing at the
	// head's contribution/diff predecessor, nil at the first revision.
	Prev(ctx context.Context) (Branch, error)

	// Subgraph access — membership and lookup within the branch's closure,
	// resolved against the Universe the branch was read from.
	HasClaim(ctx context.Context, id Id) (bool, error)
	GetClaim(ctx context.Context, id Id) (Claim, error)
	GetClaimContent(ctx context.Context, id Id) (io.Reader, error)

	// Verify runs a (possibly long-running) verification over the branch's
	// closure. See verify.go for options.
	Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error)
}

// branch wraps the named contribution/branch edge with the Universe its
// subgraph is read from; the embedded Edge promotes Reference/Type/GetField/….
type branch struct {
	Edge
	u Universe
}

// newBranch wraps a contribution/branch edge as a Branch scoped to u.
func newBranch(u Universe, e Edge) Branch {
	return &branch{Edge: e, u: u}
}

func (b *branch) Name() string {
	name, _ := b.GetField(FieldName)
	return name
}

// Head is the branch's subgraph root — the edge's reference.
func (b *branch) Head() Id { return b.Reference() }

func (b *branch) HasClaim(ctx context.Context, id Id) (bool, error) {
	if id == nil {
		return false, errNilID
	}
	return InClosure(ctx, b.u, b.Name(), []Id{b.Reference()}, id)
}

// GetClaim resolves id in this branch, verifying scope through the closure.
func (b *branch) GetClaim(ctx context.Context, id Id) (Claim, error) {
	if id == nil {
		return nil, errNilID
	}
	return GetFromClosure(ctx, b.u, b.Name(), []Id{b.Reference()}, id)
}

func (b *branch) GetClaimContent(ctx context.Context, id Id) (io.Reader, error) {
	c, err := b.GetClaim(ctx, id) // scope-checked; then read from the claim in hand
	if err != nil {
		return nil, err
	}
	return c.GetContent(ctx, b.u)
}

// Prev returns the branch of the same name at the head's contribution/diff
// predecessor, nil at the first revision.
func (b *branch) Prev(ctx context.Context) (Branch, error) {
	head, err := GetClaim(ctx, b.u, b.Reference())
	if err != nil {
		return nil, err
	}
	diff := head.Edges(EdgeFilterType{Type: EdgeTypeDiff})
	if len(diff) == 0 {
		return nil, nil
	}
	prevEdge, err := NewEdge(EdgeConfig{
		Reference: diff[0].Reference(),
		TypeClass: EdgeClassContribution,
		TypeSub:   string(EdgeSubtypeBranch),
		Fields:    map[string]string{FieldName: b.Name()},
	})
	if err != nil {
		return nil, err
	}
	return newBranch(b.u, prevEdge), nil
}

// Verify lives in verify.go.
