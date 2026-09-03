package matrix_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/helpers"
	"github.com/rankegraph/ranke-go/tests/matrix"
	"github.com/rankegraph/ranke-go/tests/rql"
)

// TestHeightIsAField: height filters through Where like any other field, across an
// archive built over two contributions. A claim's height is its depth, so both
// sources sit at 1 whichever revision added them.
func TestHeightIsAField(t *testing.T) {
	eachRevisionBackend(t, func(t *testing.T, u ranke.Universe, rev revisions) {
		both := []string{rev.first.String(), rev.second.String()}
		sources := ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}}

		atOne := reached(t, u, rev.head2, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch},
			Where: &ranke.Where{And: []ranke.Where{sources,
				{Field: "height", Test: &ranke.Comparison{Le: 1}}}},
		})
		require.ElementsMatch(t, both, atOne, "both sources are at height 1")

		belowOne := reached(t, u, rev.head2, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch},
			Where: &ranke.Where{And: []ranke.Where{sources,
				{Field: "height", Test: &ranke.Comparison{Lt: 1}}}},
		})
		require.Empty(t, belowOne, "no source sits below height 1")
	})
}

// revisions names the two-revision toy: a claim per revision, plus each revision's
// archive head.
type revisions struct {
	first, second ranke.Id
	head1, head2  ranke.Id
}

// eachRevisionBackend runs check over every row against the two-revision toy —
// matrix.Each over a recipe no generator spec can express.
func eachRevisionBackend(t *testing.T, check func(*testing.T, ranke.Universe, revisions)) {
	t.Helper()
	matrix.Each(t, matrix.Recipe[revisions]{
		Key:   "two-revision toy",
		Build: buildRevisions,
	}, check)
}

// buildRevisions commits two source claims in two contributions, so the branch has
// two revisions whose membership differs by exactly one claim. It writes at build
// time; the tests reading it afterwards do not, which is what lets them share it.
func buildRevisions(ctx context.Context, u ranke.Universe) (revisions, error) {
	clock := generator.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Second)

	priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	if err != nil {
		return revisions{}, err
	}
	opClaim, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream).
		WithField(ranke.FieldName, "revisions").
		WithCreatedAt(clock.Tick()).
		Sign(priv)
	if err != nil {
		return revisions{}, err
	}
	op, err := opClaim.AsContributor(ctx, nil, priv)
	if err != nil {
		return revisions{}, err
	}

	seq, err := devseq.NewSequencer(ctx, u, ranke.Seed([]byte(op.ID().String())), op, clock)
	if err != nil {
		return revisions{}, err
	}
	if _, err := helpers.Found(ctx, seq, "height"); err != nil {
		return revisions{}, err
	}

	note := func(body string) (ranke.Claim, error) {
		return ranke.NewClaim(ranke.TypeSource("note"), op).
			WithInlineContent([]byte(body)).
			WithEncoding(ranke.EncodingPlain).
			WithCreatedAt(clock.Tick()).
			WithHeight(ranke.HeightOf(op)).
			Sign()
	}

	// Both claims are minted before either is committed, so the clock ticks in the
	// order that fixes these ids.
	first, err := note("the first claim")
	if err != nil {
		return revisions{}, err
	}
	second, err := note("the second claim")
	if err != nil {
		return revisions{}, err
	}

	// Two contributions, so the archive carries two revisions of "main".
	head1, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{first})
	if err != nil {
		return revisions{}, err
	}
	head2, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{second})
	if err != nil {
		return revisions{}, err
	}
	return revisions{first: first.ID(), second: second.ID(), head1: head1, head2: head2}, nil
}
