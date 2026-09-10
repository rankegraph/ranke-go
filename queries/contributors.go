// package: queries / contributors
// type:    logic
// job:     the reads a caller needs before it can write anything — a branch's contributors, and the
// one carrying a given public key
// limits:  reads an Archive snapshot and nothing else; every helper is ordinary RQL, so this is a
// worked example as much as a library (-> query for the language itself)
package queries

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/rankegraph/ranke-go"
)

var errContributors = errors.New("queries.Contributors")

// Contributors returns every `contribution/contributor` claim branch reaches, in the
// order the engine yields them. ranke.BranchArchive names the archive entire.
func Contributors(ctx context.Context, arc ranke.Archive, branch string) ([]ranke.Claim, error) {
	stream, err := arc.Query(ctx, ranke.Query{
		Select: ranke.Select{Branch: branch},
		Where: &ranke.Where{
			Field: "type",
			Test:  &ranke.Comparison{Eq: string(ranke.NodeContributor)},
		},
	})
	if err != nil {
		return nil, ranke.Wrap(errContributors, err)
	}
	defer stream.Close()

	var out []ranke.Claim
	for stream.Next() {
		r := stream.Result()
		if r.Kind == ranke.KindReport {
			continue
		}
		c, err := arc.GetClaim(ctx, r.ClaimId)
		if err != nil {
			return nil, ranke.Wrap(errContributors, err)
		}
		out = append(out, c)
	}
	if err := stream.Err(); err != nil {
		return nil, ranke.Wrap(errContributors, err)
	}
	return out, nil
}

// ContributorsByKey returns the contributor claims in branch carrying pubkey, one of
// whose ids its holder must reference to sign (`V-SIG`). Several can match: no rule
// makes a `pubkey` unique, so one key registered twice is two identities carrying
// different provenance. Name the branch you write to — a claim signs under one its own
// branch reaches, where ranke.BranchArchive answers what the archive holds anywhere.
func ContributorsByKey(ctx context.Context, arc ranke.Archive, branch string, pubkey []byte) ([]ranke.Claim, error) {
	all, err := Contributors(ctx, arc, branch)
	if err != nil {
		return nil, err
	}
	var out []ranke.Claim
	for _, c := range all {
		body, err := arc.GetClaimContent(ctx, c.ID())
		if err != nil {
			return nil, ranke.Wrap(errContributors, err)
		}
		held, err := io.ReadAll(body)
		if err != nil {
			return nil, ranke.Wrap(errContributors, err)
		}
		if bytes.Equal(held, pubkey) {
			out = append(out, c)
		}
	}
	return out, nil
}
