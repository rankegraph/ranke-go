// Conformance scenario 02 — agent analyses two emails.
//
// An operator (the Sequencer's self, an Ed25519-keyed contributor)
// ingests two emails as source/email claims. An extraction agent
// (its own Ed25519 key) is itself contributed by the operator — the
// operator's key signs the agent's contributor claim, whose content
// is the agent's public key (§5.7). The agent then derives entities
// and relations from the emails: a summary, four entities (Alice,
// apples, Bob Sr, Bob Jr), and four relations (likes, knows, ignores,
// family).
//
// Two Bobs from two emails get distinct ids by content-addressing
// (different derivation/source edges → different node hashes). The
// "family" relation is symmetric: both members carry RelationFrom.
//
// Demonstrates the multi-contributor pattern (operator + agent) and
// the §3.5 derivation chain (every derived claim cites its source
// via a derivation/source edge). A referencing claim declares its
// Height (§4.1): 1 + the max height of everything it points at.

// package: main / scenario
// type:    cmd
// job:     build & persist the scenario-02 agent-analyses data bundle
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
	s := helpers.New("02 - agent analyses",
		time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC))

	// --- 1. Operator: an Ed25519-keyed root contributor (the Sequencer's
	// self, stored at bootstrap, so it does not join the batch). Its key is
	// derived deterministically from its identity so the bundle reproduces. ---
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

	// --- 2. Extraction agent — an Ed25519 contributor vouched for by the
	// operator: the operator's key signs it, its content is the agent's
	// public key. agentAKey is then bound to the agent for its OWN claims. ---
	agentAKey := must(ranke.LoadPrivateKey(helpers.KeyPath("agentA.pem")))
	agentClaim := must(ranke.ClaimBuilder{
		Type:          ranke.NodeContributor,
		InlineContent: agentAKey.Pubkey,
		Encoding:      ranke.EncodingOctetStream,
		Contributor:   operator,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(operator),
	}.Sign())
	agent := must(agentClaim.AsContributor(ctx, nil, agentAKey.Private))

	// --- 3. Ingest two source emails, attributed to the operator. ---
	emailApples := must(ranke.ClaimBuilder{
		Type:          ranke.TypeSource("email"),
		Encoding:      ranke.EncodingMessage("rfc822"),
		InlineContent: must(helpers.LoadSource("alice_to_bob__apples.eml")),
		Contributor:   operator,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(operator),
	}.Sign())
	emailFamily := must(ranke.ClaimBuilder{
		Type:          ranke.TypeSource("email"),
		Encoding:      ranke.EncodingMessage("rfc822"),
		InlineContent: must(helpers.LoadSource("alice_to_bob__family.eml")),
		Contributor:   operator,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(operator),
	}.Sign())

	// --- 4. Agent: summary derivation of the apples email. ---
	summary := must(ranke.ClaimBuilder{
		Type:          ranke.TypeDerivation("summary"),
		InlineContent: []byte("Alice expresses preference for apples."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agent,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agent, emailApples),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())

	// --- 5. Agent: extract entities (one Alice, one apples, two Bobs). ---
	alice := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("person"),
		InlineContent: []byte("Alice"),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agent,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agent, emailApples),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	apples := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("object"),
		InlineContent: []byte("apples"),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agent,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agent, emailApples),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	bobSr := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("person"),
		InlineContent: []byte("Bob"),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agent,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agent, emailApples),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	bobJr := must(ranke.ClaimBuilder{
		Type:          ranke.TypeEntity("person"),
		InlineContent: []byte("Bob Jr."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agent,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agent, emailFamily),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailFamily.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())

	// --- 6. Agent: extract relations. ---
	// Each relation/* edge carries an additional "conviction" field
	// in the -1..1 range — an application convention (paper §3.5
	// mentions "conviction values" as the kind of detail consumers
	// fetch at intermediate abstraction levels). +1.0 is strong
	// assertion, -1.0 strong negation (used in scenario 03), 0 is
	// neutral. Values are decimal-encoded strings since edge Fields
	// is map[string]string. They participate in the canonical
	// encoding — variant impls must match byte-for-byte.
	strong := map[string]string{"conviction": "1.0"}
	medium := map[string]string{"conviction": "0.5"}
	weak := map[string]string{"conviction": "0.3"}

	likes := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("likes"),
		InlineContent: []byte("Alice expresses preference for apples in the email."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agent,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agent, emailApples, alice, apples),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: emailApples.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("likes"), RelationDirection: ranke.RelationFrom, Fields: strong})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: apples.ID(), Type: ranke.TypeRelation("likes"), RelationDirection: ranke.RelationTo, Fields: strong})),
		},
	}.Sign())
	knows := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("knows"),
		InlineContent: []byte("Alice addresses Bob directly, implying they are acquainted."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agent,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agent, emailApples, alice, bobSr),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: emailApples.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationFrom, Fields: strong})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bobSr.ID(), Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationTo, Fields: strong})),
		},
	}.Sign())
	ignores := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("ignores"),
		InlineContent: []byte("Bob does not respond to Alice (inferred from absence of reply)."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agent,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agent, emailApples, bobSr, alice),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: emailApples.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bobSr.ID(), Type: ranke.TypeRelation("ignores"), RelationDirection: ranke.RelationFrom, Fields: weak})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("ignores"), RelationDirection: ranke.RelationTo, Fields: weak})),
		},
	}.Sign())
	family := must(ranke.ClaimBuilder{
		Type:          ranke.TypeRelation("family"),
		InlineContent: []byte("Bob Sr. and Bob Jr. share a surname; Alice's email implies kinship."),
		Encoding:      ranke.EncodingPlain,
		Contributor:   agent,
		CreatedAt:     s.NextTimestamp(time.Second),
		Height:        ranke.HeightOf(agent, emailFamily, bobSr, bobJr),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: emailFamily.ID(), Type: ranke.TypeDerivation("source")})),
			// Symmetric: both members are RelationFrom (§4.7).
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bobSr.ID(), Type: ranke.TypeRelation("family"), RelationDirection: ranke.RelationFrom, Fields: medium})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bobJr.ID(), Type: ranke.TypeRelation("family"), RelationDirection: ranke.RelationFrom, Fields: medium})),
		},
	}.Sign())

	// --- 7. Merge one contribution carrying every claim; the dev Sequencer
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
		agentClaim, emailApples, emailFamily, summary,
		alice, apples, bobSr, bobJr,
		likes, knows, ignores, family,
	}))

	// --- 8. Reload, verify every branch closure, dump ids. ---
	s.ReloadAndVerify(ctx, "main")
}
