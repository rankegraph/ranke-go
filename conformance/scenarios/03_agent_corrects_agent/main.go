// Conformance scenario 03 — agentB corrects agentA via contradiction.
//
// The Ranke-Graph is append-only (§5.4): you don't delete a wrong
// claim, you ADD claims that contradict it. This scenario shows the
// pattern in practice.
//
// agentA reads a short email and infers "Alice and Bob are siblings"
// — encoded as a relation/sibling claim with conviction=+1.0. A month
// later agentB reviews the same source, realises Bob is Alice's
// employer (not her brother), and issues TWO new claims:
//
//   1. The SAME (Alice, sibling, Bob) relation, by agentB, with
//      conviction=-1.0 — a structural negation of agentA's claim.
//   2. A new (Alice, employed_by, Bob) relation with conviction=+1.0
//      — the corrected reading.
//
// Both extractions remain in U. Consumers resolve the contradiction
// based on contributor trust + recency + their own policy. The
// graph documents the disagreement, it does not decide.
//
// Maps to spec §2.2 ("contradictions in the evidence base are
// themselves evidence") and §3.5 (conviction values). A referencing
// claim declares its Height (§4.1): 1 + the max reference height.

// package: main / scenario
// type:    cmd
// job:     build & persist the scenario-03 agent-corrects-agent data bundle
// limits:  doesn't verify variant reproductions; that's the run.sh harness (-> conformance/helpers)
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"time"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/adapter/storage/fs"
	"github.com/rankegraph/ranke-go/conformance/helpers"
	testhelpers "github.com/rankegraph/ranke-go/tests/helpers"
)

func must[T any](v T, rest ...any) T { return helpers.Must(v, rest...) }

func main() {
	ctx := context.Background()
	s := helpers.New("03 - agent corrects agent",
		time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC))

	strong := map[string]string{"conviction": "1.0"}
	negate := map[string]string{"conviction": "-1.0"}

	// --- 1. Operator (the Sequencer's Ed25519-keyed self, stored at
	// bootstrap) plus two extraction agents, each vouched for by the
	// operator with the agent's public key as content (§5.7). ---
	opSeed := sha256.Sum256([]byte("operator@example.com"))
	opPriv := ed25519.NewKeyFromSeed(opSeed[:])
	opPub := must(ranke.EncodePublicKey(opPriv.Public()))
	operatorClaim := must(ranke.ClaimBuilder{
		Type:          ranke.NodeContributor,
		InlineContent: opPub,
		Encoding:      ranke.EncodingOctetStream,
		CreatedAt:     s.NextTimestamp(time.Second),
		SigningKey:    opPriv,
	}.Sign())
	operator := must(operatorClaim.AsContributor(ctx, nil, opPriv))

	agentAKey := must(ranke.LoadPrivateKey(helpers.KeyPath("agentA.pem")))
	agentAClaim := must(ranke.ClaimBuilder{
		Type:          ranke.NodeContributor,
		InlineContent: agentAKey.Pubkey,
		Encoding:      ranke.EncodingOctetStream,
		Contributor:   operator,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(operator),
	}.Sign())
	agentA := must(agentAClaim.AsContributor(ctx, nil, agentAKey.Private))

	agentBKey := must(ranke.LoadPrivateKey(helpers.KeyPath("agentB.pem")))
	agentBClaim := must(ranke.ClaimBuilder{
		Type:          ranke.NodeContributor,
		InlineContent: agentBKey.Pubkey,
		Encoding:      ranke.EncodingOctetStream,
		Contributor:   operator,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(operator),
	}.Sign())
	agentB := must(agentBClaim.AsContributor(ctx, nil, agentBKey.Private))

	// --- 2. Source email — ambiguous reference to "brother". ---
	email := must(ranke.ClaimBuilder{
		Type:          ranke.TypeSource("email"),
		Encoding:      ranke.EncodingMessage("rfc822"),
		InlineContent: []byte("From: alice@example.com\r\nTo: bob@example.com\r\n\r\nBob, please tell my brother to call me. — Alice\r\n"),
		Contributor:   operator,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(operator),
	}.Sign())

	// --- 3. Entities (Alice + Bob), extracted by agentA. ---
	alice := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("person"),
		InlineContent: []byte("Alice"),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agentA,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agentA, email),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: email.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	bob := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("person"),
		InlineContent: []byte("Bob"),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agentA,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agentA, email),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: email.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())

	// --- 4. agentA's original (wrong) extraction: Alice — sibling → Bob. ---
	siblingA := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("sibling"),
		InlineContent: []byte("Alice mentions 'my brother' in a note to Bob; agentA infers they are siblings."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agentA,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agentA, email, alice, bob),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: email.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("sibling"), RelationDirection: ranke.RelationFrom, Fields: strong})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bob.ID(), Type: ranke.TypeRelation("sibling"), RelationDirection: ranke.RelationTo, Fields: strong})),
		},
	}.Sign())

	// --- 5. A month later, agentB reviews and corrects. ---
	// First a NEGATION — identical relation shape but conviction=-1.0,
	// attributed to agentB. Says: "agentA's claim is wrong."
	siblingNeg := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("sibling"),
		InlineContent: []byte("Re-reading the source: 'my brother' refers to a third party, not Bob. agentA's sibling claim is incorrect."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agentB,
		CreatedAt:     s.NextTimestamp(31 * 24 * time.Hour),
		Height:        ranke.HeightOf(agentB, email, alice, bob),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: email.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("sibling"), RelationDirection: ranke.RelationFrom, Fields: negate})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bob.ID(), Type: ranke.TypeRelation("sibling"), RelationDirection: ranke.RelationTo, Fields: negate})),
		},
	}.Sign())

	// Then the CORRECTION — Alice is employed by Bob, conviction=+1.0.
	employedBy := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("employed_by"),
		InlineContent: []byte("Cross-referenced employment records: Alice works for Bob's company."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agentB,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agentB, email, alice, bob),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: email.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("employed_by"), RelationDirection: ranke.RelationFrom, Fields: strong})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bob.ID(), Type: ranke.TypeRelation("employed_by"), RelationDirection: ranke.RelationTo, Fields: strong})),
		},
	}.Sign())

	// --- 6. Merge one contribution carrying every claim; the dev Sequencer
	// verifies, auto-consolidates the open heads, mints the branch table and
	// bookmarks it, advancing branch "main". ---
	u := must(fs.New(helpers.UniverseDir))
	// The list seed is the operator's own id: deterministic, so a rerun lands on the
	// same id_seq slots and the bundle stays byte-identical.
	seq := must(devseq.NewSequencer(ctx, u, ranke.Seed([]byte(operator.ID().String())), operator, s))
	// Found brings the archive into being and registers its first contributor under
	// the Sequencer, which is the only key that can sign one in (`V-SIG`): bob hands
	// over a public key and keeps its private half.
	bobKey := must(ranke.LoadPrivateKey(helpers.KeyPath("bob.pem")))
	must(seq.Found(ctx, bobKey.Pubkey))
	helpers.WriteBookmarkId(seq)
	must(testhelpers.Contribute(ctx, seq, "main", []ranke.Claim{
		agentAClaim, agentBClaim, email,
		alice, bob, siblingA, siblingNeg, employedBy,
	}))

	// --- 7. Reload, verify every branch closure, dump ids. ---
	s.ReloadAndVerify(ctx, "main")
}
