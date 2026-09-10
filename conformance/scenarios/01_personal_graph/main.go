// Conformance scenario 01 — personal knowledge graph.
//
// Alice (Ed25519 keypair, signed) ingests two emails, then extracts
// a small knowledge graph (summary + entities + relations) over
// them. The result is persisted as a data/ bundle (universe + B_h +
// sorted ids), and a conformant variant implementation must
// reproduce the same bundle byte-for-byte.
//
// Every claim is built inline so the reader sees the exact
// ClaimBuilder + Sign call for each one — no local helpers hiding
// the pattern. A claim that references others declares its Height
// (§4.1): 1 + the max height of everything it points at (the
// contributor included).
//
// Run from anywhere:
//
//	conformance/scenarios/01_personal_graph/run.sh

// package: conformance/scenarios/01_personal_graph / scenario
// type:    cmd
// job:     build & persist the scenario-01 personal-graph data bundle
// limits:  doesn't verify variant reproductions; that's the run.sh harness (-> conformance/helpers)
package main

import (
	"context"
	"time"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/adapter/storage/fs"
	"github.com/rankegraph/ranke-go/conformance/helpers"
	testhelpers "github.com/rankegraph/ranke-go/tests/helpers"
)

// must aliases helpers.Must so error-checked calls read with a
// single word at every site.
func must[T any](v T, rest ...any) T { return helpers.Must(v, rest...) }

func main() {
	ctx := context.Background()
	s := helpers.New("01 - personal knowledge graph",
		time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC))

	// --- 1. Alice as initial claim, signed by her Ed25519 key. Her content
	// is her multikey-encoded public key (§5.7). She is the Sequencer's
	// self, stored at bootstrap, so she does not join the contribution batch. ---
	aliceKey := must(ranke.LoadPrivateKey(helpers.KeyPath("alice.pem")))
	aliceClaim := must(ranke.ClaimBuilder{
		Type:          ranke.NodeContributor,
		InlineContent: aliceKey.Pubkey,
		Encoding:      ranke.EncodingOctetStream,
		CreatedAt:     s.NextTimestamp(time.Second),
		SigningKey:    aliceKey.Private,
	}.Sign())
	alice := must(aliceClaim.AsContributor(ctx, nil, aliceKey.Private))

	// --- 2. Ingest the two source emails. ---
	emailApples := must(ranke.ClaimBuilder{
		Type:          ranke.TypeSource("email"),
		Encoding:      ranke.EncodingMessage("rfc822"),
		InlineContent: must(helpers.LoadSource("alice_to_bob__apples.eml")),
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice)),
	}.Sign())
	emailFamily := must(ranke.ClaimBuilder{
		Type:          ranke.TypeSource("email"),
		Encoding:      ranke.EncodingMessage("rfc822"),
		InlineContent: must(helpers.LoadSource("alice_to_bob__family.eml")),
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice)),
	}.Sign())

	// --- 3. A summary derivation of the apples email. ---
	summary := must(ranke.ClaimBuilder{
		Type:          ranke.TypeDerivation("summary"),
		InlineContent: []byte("Alice tells Bob she likes apples."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice, emailApples)),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())

	// --- 4. "Alice likes apples" — entities + their relation, made together. ---
	aliceEntity := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("person"),
		InlineContent: []byte("Alice"),
		Encoding:      ranke.EncodingPlain,
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice, emailApples)),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	applesEntity := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("object"),
		InlineContent: []byte("apples"),
		Encoding:      ranke.EncodingPlain,
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice, emailApples)),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	likes := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("likes"),
		InlineContent: []byte("Alice expresses preference for apples."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice, emailApples, aliceEntity, applesEntity)),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference: emailApples.ID(),
				Type:      ranke.TypeDerivation("source"),
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         aliceEntity.ID(),
				Type:              ranke.TypeRelation("likes"),
				RelationDirection: ranke.RelationFrom,
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         applesEntity.ID(),
				Type:              ranke.TypeRelation("likes"),
				RelationDirection: ranke.RelationTo,
			})),
		},
	}.Sign())

	// --- 5. "Alice knows Bob" — Alice already built; add Bob and the relation. ---
	bobSr := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("person"),
		InlineContent: []byte("Bob"),
		Encoding:      ranke.EncodingPlain,
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice, emailApples)),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	knows := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("knows"),
		InlineContent: []byte("Alice addresses Bob directly."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice, emailApples, aliceEntity, bobSr)),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference: emailApples.ID(),
				Type:      ranke.TypeDerivation("source"),
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         aliceEntity.ID(),
				Type:              ranke.TypeRelation("knows"),
				RelationDirection: ranke.RelationFrom,
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         bobSr.ID(),
				Type:              ranke.TypeRelation("knows"),
				RelationDirection: ranke.RelationTo,
			})),
		},
	}.Sign())

	// --- 6. "Bob and Bob Jr are family" (symmetric) — Bob already built. ---
	bobJr := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("person"),
		InlineContent: []byte("Bob Jr."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice, emailFamily)),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailFamily.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	familyRel := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("family"),
		InlineContent: []byte("Bob and Bob Jr. share kinship per Alice's reference."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   alice,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.FixedHeight(ranke.HeightOf(alice, emailFamily, bobSr, bobJr)),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference: emailFamily.ID(),
				Type:      ranke.TypeDerivation("source"),
			})),
			// Symmetric: both members carry RelationFrom (§4.7).
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         bobSr.ID(),
				Type:              ranke.TypeRelation("family"),
				RelationDirection: ranke.RelationFrom,
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         bobJr.ID(),
				Type:              ranke.TypeRelation("family"),
				RelationDirection: ranke.RelationFrom,
			})),
		},
	}.Sign())

	// --- 7. Compose the bundle (filesystem Universe under data/universe/, its
	// bookmark list co-located there under a key prefix, one bookmark id under
	// data/branches/B_h) and merge one contribution carrying every claim. The dev
	// Sequencer runs the paper's seven steps — verify, auto-consolidate the open
	// heads, mint the branch table, bookmark it — advancing branch "main". Real
	// deployments would stack a mem cache on top, or swap S3 in below; this keeps
	// it flat so the bundle is just a directory. ---
	u := must(fs.New(helpers.UniverseDir))
	// The list seed is alice's own id: deterministic, so a rerun lands on the same
	// id_seq slots and the bundle stays byte-identical.
	seq := must(devseq.NewSequencer(ctx, u, ranke.Seed([]byte(alice.ID().String())), alice, s))
	// Found brings the archive into being and registers its first contributor under
	// the Sequencer, which is the only key that can sign one in (`V-SIG`): bob hands
	// over a public key and keeps its private half.
	bobKey := must(ranke.LoadPrivateKey(helpers.KeyPath("bob.pem")))
	must(seq.Found(ctx, bobKey.Pubkey, "main"))
	helpers.WriteBookmarkId(seq)
	head := must(testhelpers.Contribute(ctx, seq, "main", []ranke.Claim{
		emailApples, emailFamily, summary,
		aliceEntity, applesEntity, likes,
		bobSr, knows, bobJr, familyRel,
	}))
	_ = head

	// --- 8. Reload, verify every branch closure, dump ids. ---
	s.ReloadAndVerify(ctx, "main")
}
