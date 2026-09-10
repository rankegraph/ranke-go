// package: ranke / verify
// type:    logic
// job:     the configurable closure verifier — §5.10 per-claim integrity + authenticity over a
// graph, archive, or branch closure, as a live progress run
// limits:  does not fetch content bytes unless asked (WithExternalContent); does not persist or
// advance anything (-> universe, sequencer)
package ranke

import (
	"context"
	"errors"
	"io"
	"time"
)

// --- configuration ---

type verifyConfig struct {
	maxDepth        int             // 0 = unbounded
	maxClaims       int             // stop after n claims processed; 0 = unlimited
	createdAfter    time.Time       // prune claims created before this; zero = no bound
	trusted         func(Id) bool   // prune predicate; nil = trust nothing
	externalContent bool            // fetch + verify external content (default: inline only)
	stopAfter       int             // stop after n failures; 0 = verify everything
	onError         func(Failure)   // fired per failure, from the run goroutine
	skipRule        map[string]bool // verifyRules[].name to omit (WithSkipRules)
	expiry          *expiryIndex    // revocations found in this run's closure (`R-DEXPIRY`)
}

// VerifyOption configures a verification run.
type VerifyOption func(*verifyConfig)

// WithMaxDepth bounds the closure walk to depth n (0 = full closure).
func WithMaxDepth(n int) VerifyOption { return func(c *verifyConfig) { c.maxDepth = n } }

// WithMaxClaims caps the walk at n claims processed (0 = unlimited).
func WithMaxClaims(n int) VerifyOption { return func(c *verifyConfig) { c.maxClaims = n } }

// WithCreatedAfter prunes any claim created before t. The closure walks toward
// older references, so this bounds verification to a recent window.
func WithCreatedAfter(t time.Time) VerifyOption { return func(c *verifyConfig) { c.createdAfter = t } }

// WithTrusted prunes the walk at any claim for which fn returns true, backed by
// whatever the caller has: a DB, a bloom filter, the Sequencer's committed set.
func WithTrusted(fn func(Id) bool) VerifyOption { return func(c *verifyConfig) { c.trusted = fn } }

// WithExternalContent also fetches and verifies externalized content
// (default: inline content only — external blobs can be gigabytes).
func WithExternalContent() VerifyOption { return func(c *verifyConfig) { c.externalContent = true } }

// WithStopAfter stops the walk once n failures are found (1 = fail fast,
// 0 = verify everything).
func WithStopAfter(n int) VerifyOption { return func(c *verifyConfig) { c.stopAfter = n } }

// WithOnError registers a callback fired as each failure is found. It runs
// on the run's goroutine, so it must be cheap and concurrency-safe.
func WithOnError(fn func(Failure)) VerifyOption { return func(c *verifyConfig) { c.onError = fn } }

// WithSkipRules omits the verification rules named by VerifyRule.Name (see
// VerifyRuleSet), so a scan can drop expensive rules. Unknown names are ignored.
func WithSkipRules(names ...string) VerifyOption {
	return func(c *verifyConfig) {
		if c.skipRule == nil {
			c.skipRule = make(map[string]bool, len(names))
		}
		for _, n := range names {
			c.skipRule[n] = true
		}
	}
}

// VerifyRule describes a registered verification rule: Name is the stable
// identifier (WithSkipRules), Rule the statement printed on violation.
type VerifyRule struct{ Name, Rule string }

// VerifyRuleSet lists the verification rules, in application order — the
// menu a caller picks from when deciding what to skip.
func VerifyRuleSet() []VerifyRule {
	out := make([]VerifyRule, len(verifyRules))
	for i, r := range verifyRules {
		out[i] = VerifyRule{Name: r.name, Rule: r.rule}
	}
	return out
}

func newVerifyConfig(opts ...VerifyOption) *verifyConfig {
	c := &verifyConfig{}
	for _, o := range opts {
		o(c)
	}
	return c
}

// --- the walk ---

// runVerification walks the closure from roots in the background, verifying each
// claim (§5.10) against the one Universe u, and returns a live handle. rootCheck,
// if set, validates each depth-0 root — an Archive requires a branch-table head.
func runVerification(ctx context.Context, roots []Id, u Universe, cfg *verifyConfig, rootCheck func(Claim) error) *verificationRun {
	// A revocation lies in the closure rather than under the claim it revokes, so the
	// run's roots are what makes it findable (`R-DEXPIRY`, `R-C3LIMIT`).
	cfg.expiry = newExpiryIndex(roots)
	run := newRun()
	go func() {
		defer run.finish()

		type item struct {
			id    Id
			depth int
			// dated: the edge reaching it carried a delete_by (`R-DPLANNED`).
			dated bool
		}
		seen := map[string]struct{}{}
		queue := make([]item, 0, len(roots))
		for _, id := range roots {
			queue = append(queue, item{id: id})
		}
		// A mark may lie further along the walk than the gap it explains, so gaps are
		// held and settled once the closure is exhausted (`R-DGAP`).
		gaps := map[string]Failure{}
		marked := map[string]bool{}

		stop := func() bool { return cfg.stopAfter > 0 && len(run.failures) >= cfg.stopAfter }
		processed := 0

		for len(queue) > 0 {
			if err := ctx.Err(); err != nil {
				run.abort(err)
				return
			}
			cur := queue[0]
			queue = queue[1:]

			k := cur.id.String()
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}

			if cfg.trusted != nil && cfg.trusted(cur.id) {
				continue // pruned: already trusted/committed
			}

			// The raw CBOR is the exact bytes the id was signed over, so
			// verification works from it: its node preimage and delta edges.
			raws, err := u.GetClaimsRaw(ctx, []Id{cur.id})
			if errors.Is(err, ErrNotFound) {
				// Deleted: explained by the edge that reached it, or held for a mark.
				if !cur.dated {
					gaps[k] = Failure{ID: cur.id, Depth: cur.depth, Err: Wrap(ErrUnexplainedGap, err)}
				}
				continue
			}
			if err != nil {
				run.fail(Failure{ID: cur.id, Depth: cur.depth, Err: err}, cfg.onError)
				if stop() {
					return
				}
				continue
			}
			raw := raws[0]
			c, err := DecodeClaim(cur.id, raw)
			if err != nil {
				run.fail(Failure{ID: cur.id, Depth: cur.depth, Err: err}, cfg.onError)
				if stop() {
					return
				}
				continue
			}

			if !cfg.createdAfter.IsZero() && c.Node().CreatedAt().Before(cfg.createdAfter) {
				continue // pruned: older than the created_at bound
			}

			if cur.depth == 0 && rootCheck != nil {
				if err := rootCheck(c); err != nil {
					run.fail(Failure{ID: cur.id, Depth: 0, Err: err}, cfg.onError)
					if stop() {
						return
					}
				}
			}

			if err := verifyClaim(ctx, c, raw, cfg, u); err != nil {
				run.fail(Failure{ID: cur.id, Depth: cur.depth, Err: err}, cfg.onError)
				if stop() {
					return
				}
			} else {
				run.pass()
			}

			processed++
			if cfg.maxClaims > 0 && processed >= cfg.maxClaims {
				return // hit the work cap
			}

			// A contribution/delete claim marks its target, explaining that gap
			// (`R-DREQUEST`).
			if c.Node().Type() == NodeDelete {
				for _, e := range c.Edges() {
					if e.Type() == EdgeTypeDelete {
						marked[e.Reference().String()] = true
					}
				}
			}

			// Every reference is walked; one that does not resolve fails above unless a
			// gap explains it (`V-REF`). Delta edges arrive via a diff predecessor.
			if cfg.maxDepth == 0 || cur.depth < cfg.maxDepth {
				for _, e := range c.Edges() {
					queue = append(queue, item{
						id:    e.Reference(),
						depth: cur.depth + 1,
						dated: e.HasField(FieldDeleteBy),
					})
				}
			}
		}

		// Every mark has now been seen, and an unexplained gap is data loss.
		for k, f := range gaps {
			if marked[k] {
				continue
			}
			run.fail(f, cfg.onError)
			if stop() {
				return
			}
		}
	}()
	return run
}

// verifyClaim runs every verifyRules entry against one claim (§5.10). A new
// invariant is one rule function plus one registry entry.
func verifyClaim(ctx context.Context, c Claim, raw []byte, cfg *verifyConfig, u Universe) error {
	signer, err := resolveSigner(ctx, c, u)
	if err != nil {
		return WrapDetail(errVerify, "resolve signer", err)
	}
	pubkey, err := resolveClaimPubkey(ctx, signer, signer == c, u)
	if err != nil {
		return WrapDetail(errVerify, "resolve pubkey", err)
	}
	t := &claimUnderVerification{claim: c, raw: raw, pubkey: pubkey, signer: signer, cfg: cfg, u: u}
	fail := func(r verifyRule, err error) error {
		return WrapDetail(errVerify, r.name+" — "+r.rule, err) // name the rule + its statement
	}
	// Per claim, and per content carrier (the node).
	for _, r := range verifyRules {
		if cfg.skipRule[r.name] {
			continue
		}
		if r.claim != nil {
			if err := r.claim(ctx, t); err != nil {
				return fail(r, err)
			}
		}
		if r.content != nil {
			if err := r.content(ctx, c.Node(), t); err != nil {
				return fail(r, err)
			}
		}
	}
	// Per edge — each edge is also a content carrier.
	for _, e := range c.Edges() {
		for _, r := range verifyRules {
			if cfg.skipRule[r.name] {
				continue
			}
			if r.content != nil {
				if err := r.content(ctx, e, t); err != nil {
					return fail(r, err)
				}
			}
			if r.edge != nil {
				if err := r.edge(ctx, e, t); err != nil {
					return fail(r, err)
				}
			}
		}
	}
	return nil
}

// verifyArchiveHead runs the archive-scoped rules against an archive's head
// claim; wired as runVerification's depth-0 root check by archive.Verify.
func verifyArchiveHead(ctx context.Context, head Claim, u Universe, cfg *verifyConfig) error {
	t := &claimUnderVerification{claim: head, cfg: cfg, u: u}
	for _, r := range verifyRules {
		if r.archive == nil || cfg.skipRule[r.name] {
			continue
		}
		if err := r.archive(ctx, t); err != nil {
			return WrapDetail(errVerify, r.name+" — "+r.rule, err)
		}
	}
	return nil
}

// claimUnderVerification is the read-only surface a rule inspects: the decoded
// claim, its stored raw CBOR, the signing pubkey and the claim carrying it, the
// config, the Universe.
type claimUnderVerification struct {
	claim  Claim
	raw    []byte
	pubkey []byte
	signer Claim // the contributor whose key signed, or the claim itself when initial
	cfg    *verifyConfig
	u      Universe
}

// verifyRule is one verification invariant: a short name, the rule stated in
// words so a violation explains itself, and up to four optional scoped checks:
//
//	claim   — once per claim
//	content — per content carrier: the node, and each edge
//	edge    — per edge, e.g. what it references
//	archive — once against an archive's head (archive.Verify)
type verifyRule struct {
	name    string
	rule    string
	claim   func(ctx context.Context, t *claimUnderVerification) error
	content func(ctx context.Context, cc contentCarrier, t *claimUnderVerification) error
	edge    func(ctx context.Context, e Edge, t *claimUnderVerification) error
	archive func(ctx context.Context, t *claimUnderVerification) error
}

// verifyRules is the ordered rule set. Register an invariant here; its
// statement, scope, and implementation all live on this one entry.
var verifyRules = []verifyRule{
	{name: "id", rule: "a claim's id is the hash of the envelope it is stored as (`V-ID`)", claim: ruleID},
	{name: "signature", rule: "a claim's envelope is signed by its contributor's key (`V-ENV`, `V-SIG`)", claim: ruleSignature},
	// The record-only statement about a first table's height runs ahead of the general
	// re-derivation, so a violation is named by the rule that FIXES the value.
	{name: "first branch-table height", rule: "an archive's first branch table, standing on its contributor edge alone, has height 1 (`V-ARCHIVEHEIGHT`)", claim: ruleArchiveFirstTableHeight},
	{name: "§4.1 height", rule: "height = 1 + max(reference heights), and 0 for an initial claim (`V-HEIGHT`)", claim: ruleHeight},
	{name: "created_at monotonicity", rule: "a claim is dated no earlier than every claim it references (`V-MONO`)", claim: ruleCreatedAtMonotone},
	{name: "type classes", rule: "the node's class and every edge's class is one of the fixed set, the subtype being open vocabulary (`V-TYPE`)", claim: ruleTypeClasses},
	{name: "relation direction", rule: "a relation/* edge carries relation_direction 1 or -1, an edge of any other class 0 (`V-REL`)", edge: ruleRelationDirection},
	{name: "edge order", rule: "a claim's edges are inlined ascending by id(e), compared as byte strings (`V-EORDER`)", claim: ruleEdgeOrder},
	{name: "delete mark target", rule: "a contribution/delete claim carries a contribution/delete edge naming the claim it deletes (`R-DREQUEST`)", claim: ruleDeleteMarkShape},
	{name: "content integrity", rule: "content with a content_hash matches it and content_size, inline content being committed by the claim id (`V-CONTENT`)", content: ruleContent},
	{name: "content encoding", rule: "a node or edge that carries content declares an encoding (media type) (`V-CONTENT`)", content: ruleContentEncoding},
	{name: "branch-table reference", rule: "a branch-table (contribution/branches) claim may be referenced only by another branch-table claim, and only through its contribution/diff or contribution/branches edge (`V-TABLEREF`)", edge: ruleBranchTableReference},
	{name: "archive head", rule: "an archive's head claim is a branch table (contribution/branches) (`V-ARCHIVE`)", archive: ruleArchiveHead},
	{name: "key validity", rule: "a claim is dated within its contributor key's validity window, as an expiry edge against that contributor shortens it (`R-DEXPIRY`)", claim: ruleKeyWindow},
	{name: "delete_by carried", rule: "an edge carries exactly the delete_by its referenced claim declares (`R-DPLANNED`)", edge: ruleDeleteByCopied},
	{name: "structure not deletable", rule: "a contribution/{contributor,branches,delete,expiry} claim takes no delete_by (`R-DSTRUCT`)", claim: ruleStructureNotDeletable},
}

// ruleID: the id is H over the envelope as stored (`V-ID`).
func ruleID(_ context.Context, t *claimUnderVerification) error {
	return t.claim.verifyID(t.raw)
}

// ruleSignature: the envelope's signature is the contributor's (`V-ENV`, `V-SIG`).
func ruleSignature(_ context.Context, t *claimUnderVerification) error {
	return t.claim.verifySignature(t.pubkey, t.raw)
}

// ruleKeyWindow: a signature proves who signed, the window that they still could (`R-DEXPIRY`).
func ruleKeyWindow(ctx context.Context, t *claimUnderVerification) error {
	return verifyKeyWindow(ctx, t.claim, t.signer, t.u, t.cfg.expiry)
}

// verifyKeyWindow checks c's created_at against the closed window its signer declares.
// Either bound may stand alone, and a signer declaring neither never expires.
//
// A contributor claim declares the window rather than sitting in it: a key valid from
// next year is introduced by a claim written today, and one already rotated out
// declares an expiry behind its own date.
// A revocation shortens the end: an expiry edge naming the signer carries an earlier
// pubkey_expires_after, and the window ends at whichever comes first (`R-DEXPIRY`).
func verifyKeyWindow(ctx context.Context, c, signer Claim, u Universe, x *expiryIndex) error {
	if signer == nil || signer.ID().Equal(c.ID()) {
		return nil
	}
	at := c.Node().CreatedAt()
	if from, err := keyBound(signer, FieldPubkeyValidFrom); err != nil {
		return err
	} else if from != nil && at.Before(*from) {
		return WithDetail(ErrKeyNotYetValid, c.ID().String()+" dated "+at.Format(iso8601Nano))
	}
	until, err := keyBound(signer, FieldPubkeyExpiresAfter)
	if err != nil {
		return err
	}
	revoked, err := x.endFor(ctx, u, signer.ID())
	if err != nil {
		return err
	}
	if revoked != nil && (until == nil || revoked.Before(*until)) {
		until = revoked
	}
	if until != nil && at.After(*until) {
		return WithDetail(ErrKeyExpired, c.ID().String()+" dated "+at.Format(iso8601Nano))
	}
	return nil
}

// keyBound reads one RFC 3339 bound off a signer, absent reported as nil.
func keyBound(signer Claim, field string) (*time.Time, error) {
	v, err := signer.Node().GetField(field)
	if err != nil {
		return nil, nil // the field is unset, so it bounds nothing
	}
	t, err := parseRFC3339Nano(v)
	if err != nil {
		return nil, WrapDetail(ErrKeyWindowField, field+"="+v, err)
	}
	return &t, nil
}

// ruleHeight: §4.1 committed height matches the reference structure (`V-HEIGHT`).
func ruleHeight(ctx context.Context, t *claimUnderVerification) error {
	return verifyHeight(ctx, t.claim, t.u)
}

// ruleCreatedAtMonotone: `V-MONO` — created_at runs forward along every reference.
func ruleCreatedAtMonotone(ctx context.Context, t *claimUnderVerification) error {
	return verifyCreatedAtMonotone(ctx, t.claim, t.u)
}

// ruleContent (per content carrier): content that carries a content_hash must
// match it and content_size, external only when the run is configured for it.
func ruleContent(ctx context.Context, cc contentCarrier, t *claimUnderVerification) error {
	return verifyContentRef(ctx, cc, t.cfg, t.u)
}

// ruleContentEncoding (per content carrier): a node or edge that carries
// content must declare an encoding (a MIME media type).
func ruleContentEncoding(_ context.Context, cc contentCarrier, _ *claimUnderVerification) error {
	if cc.ContentKind() == ContentNone {
		return nil // no content — no encoding required
	}
	if cc.Encoding() == "" {
		return errContentWithoutEncoding
	}
	return nil
}

// ruleBranchTableReference (per edge) is `V-TABLEREF`: only a branch table's lineage
// edges may reach one.
func ruleBranchTableReference(ctx context.Context, e Edge, t *claimUnderVerification) error {
	// A table reaches its predecessor through the chain `R-C6MERGE` builds, and
	// through nothing else: any other edge would take the spine off its own layer.
	if t.claim.Node().Type() == NodeBranches &&
		(e.Type() == EdgeTypeDiff || e.Type() == EdgeTypeBranches) {
		return nil
	}
	ref, err := GetClaim(ctx, t.u, e.Reference())
	if errors.Is(err, ErrNotFound) {
		return nil // dangling refs are caught elsewhere; judge only resolvable ones
	}
	if err != nil {
		return err
	}
	if ref.Node().Type() == NodeBranches {
		return WithDetail(ErrRefsBranchTable, t.claim.Node().Type()+" → "+e.Reference().String())
	}
	return nil
}

// ruleDeleteByCopied (per edge) is `R-DPLANNED`: every edge referencing a claim that
// carries delete_by copies that date, so once the bytes are deleted the gap stays
// explained wherever the claim is reached. The contributor signs the copy; this is where
// it is held to it, while the referenced bytes are still there to compare against.
func ruleDeleteByCopied(ctx context.Context, e Edge, t *claimUnderVerification) error {
	ref, err := GetClaim(ctx, t.u, e.Reference())
	if errors.Is(err, ErrNotFound) {
		return nil // already deleted, so the edge's own copy is the only record left
	}
	if err != nil {
		return err
	}
	due, _ := ref.Node().GetField(FieldDeleteBy)
	carried, _ := e.GetField(FieldDeleteBy)
	// Exactly what the claim says: omitting a schedule leaves a gap unexplained, and
	// asserting one the claim never made announces a gap that will not come.
	if carried != due {
		return WithDetail(ErrDeleteByNotCopied,
			e.Reference().String()+" declares "+quoteOrNone(due)+", edge carries "+quoteOrNone(carried))
	}
	return nil
}

// quoteOrNone renders a schedule for an error, an absent one as a word.
func quoteOrNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// ruleStructureNotDeletable (`R-DSTRUCT`): the four subtypes another rule reads do
// not schedule their own removal. An application's own contribution/* claim may.
func ruleStructureNotDeletable(_ context.Context, t *claimUnderVerification) error {
	n := t.claim.Node()
	fields := map[string]string{}
	if due, err := n.GetField(FieldDeleteBy); err == nil {
		fields[FieldDeleteBy] = due
	}
	return CheckDeletable(NodeClass(n.TypeClass()), n.TypeSub(), fields)
}

// ruleArchiveHead (per archive): an archive's head is a contribution/branches
// claim (`V-ARCHIVE`).
func ruleArchiveHead(_ context.Context, t *claimUnderVerification) error {
	if t.claim.Node().Type() != NodeBranches {
		return WithDetail(errNotBranchTable, "got "+t.claim.Node().Type())
	}
	return nil
}

// verifyID checks the id names the bytes stored under it: id = H(S(env(v)))
// (`V-ID`), which every claim satisfies — id_seq(i,s) addresses bookmarks, and a
// bookmark is no claim.
func (c *claim) verifyID(raw []byte) error {
	recomputed, err := hashContent(raw)
	if err != nil {
		return WrapDetail(errVerify, "hash", err)
	}
	if !recomputed.Equal(c.node.id) {
		return WithDetail(ErrIDMismatch, "stored as "+recomputed.String())
	}
	return nil
}

// verifySignature checks the envelope's signature against the contributor's pubkey
// (`V-SIG`). What it proves is authorship of the stored bytes, since that is what the
// envelope covers.
func (c *claim) verifySignature(pubkey, raw []byte) error {
	return verifyEnvelope(pubkey, raw)
}

// resolveSigner returns the claim whose content is the key that signed c's id (§5.7,
// `V-ROOT`, `V-SIG`): c itself for an initial claim, else the contributor its
// contribution/contributor edge names. The claim's fields bound the key's validity too.
func resolveSigner(ctx context.Context, c Claim, u Universe) (Claim, error) {
	if len(c.Edges()) == 0 {
		return c, nil // an initial claim carries its own key
	}
	var target Id
	for _, e := range c.Edges() {
		if e.TypeClass() == EdgeClassContribution && e.TypeSub() == "contributor" {
			target = e.Reference()
			break
		}
	}
	if target == nil {
		return nil, errNoContributorEdge
	}
	cc, err := GetClaim(ctx, u, target)
	if err != nil {
		return nil, WrapDetail(ErrContributorUnresolved, target.String(), err)
	}
	return cc, nil
}

// resolveClaimPubkey reads the signing key off the claim that carries it (`V-SIG`).
func resolveClaimPubkey(ctx context.Context, signer Claim, own bool, u Universe) ([]byte, error) {
	rdr, err := signer.GetContent(ctx, u)
	if err != nil {
		if !own {
			return nil, WrapDetail(ErrContributorUnresolved, signer.ID().String(), err)
		}
		return nil, err
	}
	if rc, ok := rdr.(io.ReadCloser); ok {
		defer rc.Close()
	}
	return io.ReadAll(rdr)
}

// contentCarrier is the content-addressing surface shared by Node and Edge —
// enough to verify a content reference without the concrete type.
type contentCarrier interface {
	ContentKind() ContentKind
	GetContentHash() Id
	GetContentSize() uint64
	GetInlineContent() ([]byte, error)
	Encoding() string
}

// verifyContentRef checks external content integrity for one node/edge (`V-CONTENT`),
// when the run opts into it. The claim id already commits inline content.
func verifyContentRef(ctx context.Context, cc contentCarrier, cfg *verifyConfig, u Universe) error {
	hash := cc.GetContentHash()
	if hash == nil {
		return nil // inline (verified by the id) or no content
	}
	if !cfg.externalContent || u == nil {
		return nil // external content: skipped by default (may be huge)
	}
	rc, err := u.StreamContent(ctx, hash, cc.GetContentSize())
	if err != nil {
		return err
	}
	defer rc.Close()
	vr, err := NewVerifyingReader(rc, hash, cc.GetContentSize())
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, vr) // the reader verifies as it streams
	return err
}

// --- entry points ---

// Verify walks this graph's closure from every open head and verifies each
// claim (§5.10) against the graph's Universe.
func (g *graph) Verify(opts ...VerifyOption) VerificationRun {
	cfg := newVerifyConfig(opts...)
	return runVerification(context.Background(), g.Heads(), g.u, cfg, nil)
}

// Verify walks the archive's closure from its head and verifies each claim.
// It requires the head to be a contribution/branches claim (a branch table).
func (a *archive) Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error) {
	cfg := newVerifyConfig(opts...)
	rootCheck := func(c Claim) error { return verifyArchiveHead(ctx, c, a.u, cfg) }
	return runVerification(ctx, []Id{a.bth.ID()}, a.u, cfg, rootCheck), nil
}

// Verify walks the branch's subgraph from its referenced root and verifies
// each claim.
func (b *branch) Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error) {
	cfg := newVerifyConfig(opts...)
	return runVerification(ctx, []Id{b.Reference()}, b.u, cfg, nil), nil
}
