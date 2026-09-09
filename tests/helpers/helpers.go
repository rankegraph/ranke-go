// package: tests/helpers / testkit
// type:    tool
// job:     shared helpers for driving a ranke.Sequencer from tests and test tooling
// limits:  the contract only — no fixtures, no assertions, no backend knowledge (-> tests, tests/backends)
package helpers

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"

	"github.com/rankegraph/ranke-go"
)

// Contribute drives one contribution through the Sequencer contract's seven steps —
// open, fill, verify, persist, merge, bookmark — returning the head it advanced to.
// opts carry the constraints the contribution opens under.
func Contribute(ctx context.Context, seq ranke.Sequencer, branch string, claims []ranke.Claim,
	opts ...ranke.ContributionOption) (ranke.Id, error) {
	// Tests contribute as a fully-privileged account: the whole archive is
	// referencable and the branch may be created. A test exercising either constraint
	// opens its own contribution.
	opts = append([]ranke.ContributionOption{
		ranke.WithReferencableBranches(ranke.BranchArchive),
		ranke.WithCreatableBranches(branch),
	}, opts...)
	c, err := seq.NewContribution(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if err := c.AddClaims(branch, claims); err != nil {
		return nil, err
	}
	v, err := c.CompleteAndVerify(ctx)
	if err != nil {
		return nil, err
	}
	m, err := v.Persist(ctx)
	if err != nil {
		return nil, err
	}
	r, err := seq.Merge(ctx, m)
	if err != nil {
		return nil, err
	}
	return r.Head(), nil
}

// FoundedKey is the deterministic key a label names, so a fixture's first
// contributor is the same identity on every run and across backends. Ed25519 keys
// come from a seed, so a fixed label is a fixed key.
func FoundedKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("ranke-test-first-contributor:" + label))
	return ed25519.NewKeyFromSeed(seed[:])
}

// Found brings seq's archive into being under the key label names, which every
// operation but Found itself now requires. It returns the first contributor,
// resolved with its own private key so a test may contribute as it.
func Found(ctx context.Context, seq ranke.Sequencer, label string) (ranke.Contributor, error) {
	priv := FoundedKey(label)
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	if err != nil {
		return nil, err
	}
	first, err := seq.Found(ctx, pubkey)
	if err != nil {
		return nil, err
	}
	return first.AsContributor(ctx, nil, priv)
}
