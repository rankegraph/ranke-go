// package: ranke / sequencer
// type:    logic
// job:     the Sequencer contract — the sole writer of a Ranke-Archive: hands out read snapshots
// and advances the head by merging contributions
// limits:  interfaces only; the naive concrete implementation is a write-path mechanism
// (-> adapter/sequencer_mechanism)
package ranke

import "context"

// Sequencer is the single writer of a Ranke-Archive (RankeDB paper
// §Sequencer): it hands out immutable read snapshots and advances the
// head, k → k', by merging contributions. The concrete implementation
// (naive here, concurrent in a server) lives in an adapter.
//
// An implementation is safe to drive from several goroutines. Which one a caller
// holds decides how fast that goes, never whether it is allowed.
type Sequencer interface {
	// InGenesis reports no archive here yet, leaving Found the one operation.
	InGenesis() bool
	// Found creates the archive, once: the Sequencer's initial claim, the first
	// contributor carrying pubkey, and the branch binding it, so the list alone
	// reaches it. Returns its claim, which `V-SIG` lets only the Sequencer sign.
	Found(ctx context.Context, pubkey []byte, branch string) (Claim, error)
	// GetArchive returns the current immutable snapshot RA_k.
	GetArchive(ctx context.Context) (Archive, error)
	// GetContributor returns the contributor the Sequencer attests branch
	// advances with.
	GetContributor() Contributor
	// BookmarkId returns the id of a bookmark in this archive's list — the one
	// written at bootstrap. Any single bookmark id recovers the latest recorded
	// head (foundation paper §Backup), so this is what a bundle keeps to be
	// reopened later via OpenBookmarks.
	BookmarkId() Id
	// NewContribution opens a contribution against the current archive, under the
	// constraints opts declare (step 1).
	NewContribution(ctx context.Context, opts ...ContributionOption) (Contribution, error)
	// Merge commits a persisted contribution: it mints the branch table (step 6)
	// and takes the advance into effect against a new bookmark (step 7).
	Merge(ctx context.Context, c MergableContribution) (Receipt, error)
}

// Receipt is the outcome of a committed merge — the new head the archive
// advanced to.
type Receipt interface {
	Head() Id
}
