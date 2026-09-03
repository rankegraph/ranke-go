// package: tests/generator / testkit
// type:    tool
// job:     Generate — build a deterministic kitchen-sink Ranke-Archive from a Spec, driving the dev
// Sequencer's write path; return a Manifest naming every corner
// limits:  builds via the public ranke API + the dev testkit adapters; the caller supplies the
// Universe (-> adapter); asserts nothing (-> tests).
package generator

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"github.com/rankegraph/ranke-go"
	devseq "github.com/rankegraph/ranke-go/adapter/sequencer/dev"
	"github.com/rankegraph/ranke-go/tests/helpers"
)

var errGenerate = errors.New("generator")

// liftLimiting admits the limiting types into the generator's contributions. Only a
// Sequencer mints these in production, so a fixture builder standing in for one has
// to say so — the archives it emits must contain them to be worth testing against.
var liftLimiting = ranke.WithLiftedTypes(ranke.NodeDelete, ranke.NodeExpiry)

// Manifest is the map of a generated archive: the head to open it at, plus the
// ids of the notable claims, so a test can target one corner directly.
type Manifest struct {
	Spec Spec

	Head         ranke.Id   // archive head k′ — open with ranke.NewArchive(ctx, u, Head)
	Contributors []ranke.Id // the signing contributors (index 0 is the operator)
	Sources      []ranke.Id
	Derivations  []ranke.Id
	Entities     []ranke.Id
	HandMade     []ranke.Id // entity claims carrying no derivation edge
	Relations    []ranke.Id

	DiffChainHead ranke.Id   // head of the longest contribution/diff chain
	ExternalBlobs []ranke.Id // source claims whose content is external (hash-addressed)
	TinyBlob      ranke.Id   // the source whose content is TinyBlobBytes long
	HighDegree    ranke.Id   // the derivation citing the most sources
	Expiries      []ranke.Id // contribution/expiry claims (key expiry)
	Deletes       []ranke.Id // contribution/delete tombstones

	ClaimCount int      // claims contributed (excludes Sequencer-minted heads/tables)
	Revisions  int      // contributions merged = branch-table revisions
	Branches   []string // branch names the archive carries
}

// Generate builds the archive described by spec into u over the dev testkit
// (deterministic clock, blocking Sequencer), returning its Manifest. Same
// (u-kind, spec) → identical ids on every run.
func Generate(ctx context.Context, u ranke.Universe, spec Spec) (*Manifest, error) {
	clock := NewClock(spec.Base, spec.Step)

	// Contributor 0 is the operator the Sequencer signs branch tables with.
	op, err := contributor(ctx, spec.Seed, 0, clock.Tick())
	if err != nil {
		return nil, fmt.Errorf("%w: operator: %w", errGenerate, err)
	}
	seq, err := devseq.NewSequencer(ctx, u, ranke.Seed([]byte(op.ID().String())), op, clock)
	if err != nil {
		return nil, fmt.Errorf("%w: sequencer: %w", errGenerate, err)
	}
	// The archive's first contributor, keyed off the spec seed so the whole run stays
	// reproducible. It is registered under the operator, where the generated
	// contributors below are initial claims of their own.
	if _, err := helpers.Found(ctx, seq, "generator/"+strconv.FormatInt(spec.Seed, 10)); err != nil {
		return nil, fmt.Errorf("%w: found: %w", errGenerate, err)
	}

	b := &builder{ctx: ctx, u: u, spec: spec, clock: clock}
	b.contributors(op)
	b.sources()
	b.diffChain()
	b.derivations()
	b.entities()
	b.handMadeEntities()
	b.relations()
	b.expiries()
	b.deletes()
	if b.err != nil {
		return nil, b.err
	}

	// A real archive grows over many contributions, not one: split the batch
	// into contributions of varying size (small common, large rare)
	branches := branchNames(len(b.batch), spec.Seed, spec.Branches)
	var head ranke.Id
	revisions := 0
	for i, chunk := range contributionChunks(b.batch, spec.Seed, len(branches)) {
		// Round-robin contributions across the branches (main takes the first,
		// so the shared base lands there);
		head, err = helpers.Contribute(ctx, seq, branches[i%len(branches)], chunk, liftLimiting)
		if err != nil {
			return nil, fmt.Errorf("%w: merge: %w", errGenerate, err)
		}
		revisions++
	}

	m := b.manifest
	m.Spec = spec
	m.Head = head
	m.ClaimCount = len(b.batch)
	m.Revisions = revisions
	m.Branches = branches
	return &m, nil
}

// contributionChunks splits a dependency-ordered batch into contributions of
// exponentially-distributed size — small common, large rare — deterministic per
// seed. Mean size grows with the batch to bound the contribution count, since
// the dev Sequencer re-verifies each contribution's closure.
func contributionChunks(batch []ranke.Claim, seed int64, minChunks int) [][]ranke.Claim {
	n := len(batch)
	if n == 0 {
		return nil
	}
	// Branches take contributions in turn, so a small batch splits evenly.
	if minChunks > 1 && n >= minChunks {
		size := (n + minChunks - 1) / minChunks
		var chunks [][]ranke.Claim
		for i := 0; i < n; i += size {
			end := min(i+size, n)
			chunks = append(chunks, batch[i:end])
		}
		return chunks
	}
	rng := rand.New(rand.NewSource(seed))
	mean := 4.0
	if m := float64(n) / 150; m > mean {
		mean = m // cap the count near ~150 for large graphs
	}
	var chunks [][]ranke.Claim
	for i := 0; i < n; {
		size := 1 + int(rng.ExpFloat64()*mean)
		if i+size > n {
			size = n - i
		}
		chunks = append(chunks, batch[i:i+size])
		i += size
	}
	return chunks
}

// builder accumulates the contribution batch, threading the shared clock so
// created_at is monotone in reference order. The first error stops the run.
type builder struct {
	ctx   context.Context
	u     ranke.Universe
	spec  Spec
	clock *Clock

	contribs      []ranke.Contributor
	srcClaims     []ranke.Claim // sources available as derivation inputs
	entClaims     []ranke.Claim // entity claims, kept as objects so relations can read their heights
	batch         []ranke.Claim
	manifest      Manifest
	oversizedDone bool // the oversized-field corner is applied once
	tinyDone      bool // likewise the tiny-content corner
	err           error
}

// fail records the first error; subsequent build steps become no-ops.
func (b *builder) fail(err error) {
	if b.err == nil && err != nil {
		b.err = err
	}
}

// who round-robins claims across the contributor set, so the archive is multi-authored.
func (b *builder) who(i int) ranke.Contributor {
	return b.contribs[i%len(b.contribs)]
}

// add appends a claim to the batch and returns it (nil-safe under error).
func (b *builder) add(c ranke.Claim, err error) ranke.Claim {
	if b.err != nil {
		return nil
	}
	if err != nil {
		b.fail(err)
		return nil
	}
	b.batch = append(b.batch, c)
	return c
}

// contributors builds the operator plus spec.Contributors-1 more initial claims
// carrying keys. The Sequencer stores the operator, so only the others batch.
func (b *builder) contributors(op ranke.Contributor) {
	b.contribs = []ranke.Contributor{op}
	b.manifest.Contributors = []ranke.Id{op.ID()}
	for i := 1; i < b.spec.Contributors; i++ {
		c, err := contributor(b.ctx, b.spec.Seed, i, b.clock.Tick())
		if err != nil {
			b.fail(fmt.Errorf("%w: contributor %d: %w", errGenerate, i, err))
			return
		}
		b.contribs = append(b.contribs, c)
		b.manifest.Contributors = append(b.manifest.Contributors, c.ID())
		b.add(c, nil)
	}
}

// sources builds spec.Sources source/* claims: the first ExternalBlobs are
// hash-addressed, then inline notes and further external blobs alternate.
func (b *builder) sources() {
	for i := 0; i < b.spec.Sources && b.err == nil; i++ {
		who := b.who(i)
		var c ranke.Claim
		switch {
		case i < b.spec.ExternalBlobs:
			c = b.externalSource(who, i)
		case i%2 == 0:
			c = b.inlineSource(who, i)
		default:
			// Large data goes in external content per the ADT convention:
			// inline bytes ride in the claim record, past a caching backend.
			c = b.externalSource(who, i)
		}
		if c != nil {
			b.srcClaims = append(b.srcClaims, c)
			b.manifest.Sources = append(b.manifest.Sources, c.ID())
		}
	}
}

// inlineSource builds a source/note claim with legible inline text. The first
// one also carries the oversized field value, within the ADT's field cap.
func (b *builder) inlineSource(who ranke.Contributor, i int) ranke.Claim {
	text := textFor(b.spec.Seed, "src", i)
	tiny := !b.tinyDone && b.spec.TinyBlobBytes > 0
	if tiny {
		text = tinyFor(b.spec.Seed, "src", i, b.spec.TinyBlobBytes)
		b.tinyDone = true
	}
	cb := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte(text)).
		WithEncoding(ranke.EncodingPlain). // a note is legible text
		WithCreatedAt(b.clock.Tick())
	if !b.oversizedDone && b.spec.OversizedFieldBytes > 0 {
		// Field values are text, so hex-encode to valid UTF-8.
		big := hex.EncodeToString(fill(b.spec.Seed, "field", i, b.spec.OversizedFieldBytes/2))
		cb = cb.WithField("note", big)
		b.oversizedDone = true
	}
	if i < b.spec.DatedSources {
		cb = cb.WithDatedEDTF(datedFor(i))
	}
	c := b.add(cb.WithHeight(ranke.HeightOf(who)).Sign())
	if tiny && c != nil {
		b.manifest.TinyBlob = c.ID()
	}
	return c
}

// externalSource builds a source/* claim whose content lives in the Universe
// under its hash, putting the bytes there too.
func (b *builder) externalSource(who ranke.Contributor, i int) ranke.Claim {
	blob := fill(b.spec.Seed, "ext", i, b.spec.LargeBlobBytes)
	hash, err := ranke.HashContent(blob)
	if err != nil {
		b.fail(fmt.Errorf("%w: external hash %d: %w", errGenerate, i, err))
		return nil
	}
	if err := b.u.PutContents(b.ctx, []ranke.ContentBlob{{Hash: hash, Content: blob}}); err != nil {
		b.fail(fmt.Errorf("%w: put external content %d: %w", errGenerate, i, err))
		return nil
	}
	c := b.add(ranke.NewClaim(ranke.TypeSource("blob"), who).
		WithExternalContent(hash, uint64(len(blob))).
		WithEncoding(ranke.EncodingOctetStream). // deterministic binary blob
		WithCreatedAt(b.clock.Tick()).
		WithHeight(ranke.HeightOf(who)).
		Sign())
	if c != nil {
		b.manifest.ExternalBlobs = append(b.manifest.ExternalBlobs, c.ID())
	}
	return c
}

// diffChain builds one base derivation plus DiffChainLen contribution/diff
// overlays. Revision belongs on a derivation, refined over contributions.
func (b *builder) diffChain() {
	if b.err != nil || len(b.srcClaims) == 0 {
		return
	}
	src := b.srcClaims[0] // the chain distils this source
	de, err := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
	if err != nil {
		b.fail(fmt.Errorf("%w: diff-chain provenance edge: %w", errGenerate, err))
		return
	}
	base := b.add(ranke.NewClaim(ranke.TypeDerivation("summary"), b.who(0)).
		WithInlineContent([]byte(textFor(b.spec.Seed, "diff", 0))).
		WithEncoding(ranke.EncodingPlain). // a summary is legible text
		WithEdges(de).
		WithCreatedAt(b.clock.Tick()).
		WithHeight(ranke.HeightOf(b.who(0), src)).
		Sign())
	prev := base
	for r := 1; r <= b.spec.DiffChainLen && prev != nil && b.err == nil; r++ {
		// Each revision restates its derivation/source edge under a stable name, so
		// it stays one edge through the chain rather than accumulating.
		re, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: src.ID(),
			Type:      ranke.TypeDerivation("source"),
			Fields:    map[string]string{ranke.FieldName: "source"},
		})
		if err != nil {
			b.fail(fmt.Errorf("%w: diff-chain revision edge %d: %w", errGenerate, r, err))
			return
		}
		prev = b.add(ranke.NewClaim(ranke.TypeDerivation("summary"), b.who(r)).
			WithDiff(prev.ID()).
			WithEdges(re).
			WithField("rev", strconv.Itoa(r)).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.who(r), prev, src)).
			Sign())
	}
	if prev != nil {
		b.manifest.DiffChainHead = prev.ID()
	}
}

// derivations builds spec.Derivations derivation/* claims citing sources: the
// first cites up to MaxEdgeDegree at once, the rest one each.
func (b *builder) derivations() {
	for i := 0; i < b.spec.Derivations && len(b.srcClaims) > 0 && b.err == nil; i++ {
		degree := 1
		if i == 0 {
			degree = min(b.spec.MaxEdgeDegree, len(b.srcClaims))
		}
		edges := make([]ranke.Edge, 0, degree)
		refs := []ranke.Claim{b.who(i)}
		for d := 0; d < degree; d++ {
			src := b.srcClaims[(i+d)%len(b.srcClaims)]
			e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
			if err != nil {
				b.fail(fmt.Errorf("%w: derivation edge %d/%d: %w", errGenerate, i, d, err))
				return
			}
			edges = append(edges, e)
			refs = append(refs, src)
		}
		c := b.add(ranke.NewClaim(ranke.TypeDerivation("summary"), b.who(i)).
			WithInlineContent([]byte(textFor(b.spec.Seed, "deriv", i))).
			WithEncoding(ranke.EncodingPlain). // a summary is legible text
			WithEdges(edges...).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(refs...)).
			Sign())
		if c != nil {
			b.manifest.Derivations = append(b.manifest.Derivations, c.ID())
			if i == 0 {
				b.manifest.HighDegree = c.ID()
			}
		}
	}
}

// handMadeEntities builds spec.HandMadeEntities entity/* claims carrying no
// derivation edge — the shape a person typing an entity in produces, legal since the
// derivation requirement went. Attribution still holds: the contributor edge is
// auto-built, so these are attributed without citing anything.
func (b *builder) handMadeEntities() {
	for i := 0; i < b.spec.HandMadeEntities && b.err == nil; i++ {
		c := b.add(ranke.NewClaim(ranke.TypeEntity("person"), b.who(i)).
			WithInlineContent([]byte("hand-made " + nameFor(b.spec.Seed, i))).
			WithEncoding(ranke.EncodingPlain).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.who(i))).
			Sign())
		if c != nil {
			b.manifest.HandMade = append(b.manifest.HandMade, c.ID())
		}
	}
}

// entities builds spec.Entities entity/* claims, each with a derivation edge to a
// source — the citation an extraction pipeline has and a person does not.
func (b *builder) entities() {
	for i := 0; i < b.spec.Entities && len(b.srcClaims) > 0 && b.err == nil; i++ {
		src := b.srcClaims[i%len(b.srcClaims)]
		de, err := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
		if err != nil {
			b.fail(fmt.Errorf("%w: entity provenance edge %d: %w", errGenerate, i, err))
			return
		}
		c := b.add(ranke.NewClaim(ranke.TypeEntity("person"), b.who(i)).
			WithInlineContent([]byte(nameFor(b.spec.Seed, i))).
			WithEncoding(ranke.EncodingPlain). // a person entity's content is a name
			WithEdges(de).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.who(i), src)).
			Sign())
		if c != nil {
			b.manifest.Entities = append(b.manifest.Entities, c.ID())
			b.entClaims = append(b.entClaims, c)
		}
	}
}

// relations builds spec.Relations relation/* claims, each reifying an assertion
// between two entities with a from-edge, a to-edge and a derivation edge.
func (b *builder) relations() {
	ents := b.manifest.Entities
	if len(ents) < 2 || len(b.srcClaims) == 0 {
		return
	}
	for i := 0; i < b.spec.Relations && b.err == nil; i++ {
		fromClaim := b.entClaims[(2*i)%len(b.entClaims)]
		toClaim := b.entClaims[(2*i+1)%len(b.entClaims)]
		from := ents[(2*i)%len(ents)]
		to := ents[(2*i+1)%len(ents)]
		src := b.srcClaims[i%len(b.srcClaims)]
		de, e1 := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
		fromE, e2 := ranke.NewEdge(ranke.EdgeConfig{Reference: from, Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationFrom})
		toE, e3 := ranke.NewEdge(ranke.EdgeConfig{Reference: to, Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationTo})
		if e1 != nil || e2 != nil || e3 != nil {
			b.fail(fmt.Errorf("%w: relation edges %d: %v/%v/%v", errGenerate, i, e1, e2, e3))
			return
		}
		c := b.add(ranke.NewClaim(ranke.TypeRelation("knows"), b.who(i)).
			WithEdges(de, fromE, toE).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.who(i), src, fromClaim, toClaim)).
			Sign())
		if c != nil {
			b.manifest.Relations = append(b.manifest.Relations, c.ID())
		}
	}
}

// expiries mints spec.KeyExpiries contribution/expiry claims, each attributed to
// the operator, edged to a contributor, with a pubkey_expires_after (§Types).
func (b *builder) expiries() {
	if len(b.contribs) < 2 {
		return // no non-operator contributor to expire
	}
	// A fixed future instant, deterministic like everything else. RFC3339 alone drops
	// the fraction, which `V-TIME` requires at nanosecond precision.
	expiresAt := b.spec.Base.Add(365 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000000000Z")
	for i := 0; i < b.spec.KeyExpiries && b.err == nil; i++ {
		// Never expire the operator's key — it signs the branch tables.
		target := b.contribs[1+(i%(len(b.contribs)-1))]
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: target.ID(), Type: ranke.EdgeTypeExpiry})
		if err != nil {
			b.fail(fmt.Errorf("%w: expiry edge %d: %w", errGenerate, i, err))
			return
		}
		c := b.add(ranke.NewClaim(ranke.NodeExpiry, b.contribs[0]).
			WithEdges(e).
			WithField(ranke.FieldPubkeyExpiresAfter, expiresAt).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.contribs[0], target)).
			Sign())
		if c != nil {
			b.manifest.Expiries = append(b.manifest.Expiries, c.ID())
		}
	}
}

// deletes mints spec.Deletes contribution/delete claims, each attributed to the
// operator, edged to the claim whose bytes were removed. The paper defines
// contribution/delete as both a node and an edge type (§Type Vocabulary).
func (b *builder) deletes() {
	for i := 0; i < b.spec.Deletes && len(b.srcClaims) > 0 && b.err == nil; i++ {
		target := b.srcClaims[i%len(b.srcClaims)]
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: target.ID(), Type: ranke.EdgeTypeDelete})
		if err != nil {
			b.fail(fmt.Errorf("%w: delete edge %d: %w", errGenerate, i, err))
			return
		}
		c := b.add(ranke.NewClaim(ranke.NodeDelete, b.contribs[0]).
			WithEdges(e).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.contribs[0], target)).
			Sign())
		if c != nil {
			b.manifest.Deletes = append(b.manifest.Deletes, c.ID())
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
