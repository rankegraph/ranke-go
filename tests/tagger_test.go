// Tagger tests — the archive-wide branch-tagging pass (ranke.TagArchive)
// driven over the mem backend, which is Tags-capable.
package tests

import (
	"context"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/adapter/storage/mem"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// TestTagArchive exercises the archive-wide tagging pass: after a commit,
// TagArchive descends the branch-table spine and stamps each branch's closure
// with a _b_<branch> membership tag, returning the head-id spine it walked.
func TestTagArchive(t *testing.T) {
	ctx := context.Background()
	clock := generator.NewClock(fixtureBase, time.Second)
	self := keyedContributor(t, ctx, clock, "sequencer")

	u := mem.New()
	require.True(t, u.Capabilities().Tags, "mem is Tags-capable")
	seq, err := devseq.NewSequencer(ctx, u, ranke.Seed([]byte(self.ID().String())), self, clock)
	require.NoError(t, err, "NewSequencer")
	_, err = helpers.Found(ctx, seq, "tagger")
	require.NoError(t, err, "Found")

	em, err := ranke.NewClaim(ranke.TypeSource("email"), self).
		WithInlineContent([]byte("From: a\r\nTo: b\r\n\r\nhi\r\n")).
		WithEncoding(ranke.EncodingMessage("rfc822")).
		WithCreatedAt(clock.Tick()).
		WithHeight(ranke.HeightOf(self)).
		Sign()
	require.NoError(t, err, "sign email")
	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{em})
	require.NoError(t, err, "contribute to main")

	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err, "GetArchive")

	// The commit already tagged (the Sequencer signals the storage), so an
	// incremental pass would find nothing to do. RetagAll walks it regardless —
	// this is a unit test of the walk, not of who triggers it. The spine is the
	// output of that walk, not an input.
	spine, err := ranke.TagArchive(ctx, arc, ranke.TagArchiveOptions{RetagAll: true})
	require.NoError(t, err, "TagArchive")
	require.NotEmpty(t, spine, "TagArchive produces the walked head-id spine")

	// The branch head joined "main" — its closure carries the _b_main tag.
	b, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err, "GetBranch main")
	found, _, err := ranke.GetTag(ctx, u, b.Head(), "_b_main")
	require.NoError(t, err, "GetTag")
	require.True(t, found, "the branch head is tagged as a member of main")
}
