# Changelog

What each release changed for someone depending on this repository.

## Unreleased

### Changed

- **A claim dated before 2026-05-03 is refused.** That is the foundation paper's date,
  the day the design a claim conforms to was founded: no archive predates it, so no
  claim was added before it, and every earlier timestamp is a default in place of the
  time `V-MONO` requires — year 1 where Go writes an unset `time.Time`, 1970 where a
  clock never started, whatever a broken counter drifts to from there. All three doors
  refuse it: `AssembleClaim`, where parts describe a record a projection rebuilt;
  `NewClaim`, where a caller states one (an UNSET `CreatedAt` still takes the clock, as
  before); and the closure verifier, which is the only door left once such bytes exist
  elsewhere. `ErrCreatedAtPredatesRanke` names it, `PredatesAnyClaim` is the predicate.

  **Every id in the published vector set moved.** Its cases were stamped 2023-11-14,
  which this rule refuses, so `cmd/vectors` dates them 2026-06-01 and each case carries
  a new serialization and a new pinned id — same 27 claims and 9 bookmarks, none added,
  removed or renamed. The set is published (ranke-graph v0.29.0, generated from
  v0.35.0-rc.1), which `expectedGenerator` now names, so an implementation vendoring
  the vectors takes them again and asserts against the new ids. The scenario bundles
  are unaffected — they were already dated 2026-05-19.

- **`Select.Claim` is `[]Id`, the set `R-QANCHOR` now admits.** A read anchors at one
  claim or at several, which fetches them by id in one query — the read a client makes
  to resolve `height` before signing, since `V-HEIGHT` fixes it from the claims the new
  one references and no server can fill it in. `ranke.Anchors(ids...)` builds the field,
  and on the wire `claim` stays a plain string for a single anchor and becomes an array
  for a set. A repeat names its claim once; the wire form refuses one outright, as the
  schema's `uniqueItems` states.
- **The two empties of `Select.Path` differ (`R-QSTEPS`).** An EMPTY path takes no step
  and returns the frontier itself — with a set anchor, exactly the claims named and
  nothing they cite — where a NIL path still returns that frontier's full outward
  closure. A Go caller spells the difference as `[]PathStep{}` against `nil`, and the
  wire as `"path": []` against an absent `path`; `EncodeQuery` keeps the two apart.
  Every backend answers alike: the neo4j lowering pins a single anchor by id, matches a
  set by membership, and carries no segment for an empty path (`R-QCCLAUSE`).

### Added

- A claim may be signed under **ECDSA over P-256**, the second scheme `V-SIGN` now
  names: `ES256` in the envelope's protected header, the pubkey framed as the
  multicodec `0x1200` (`p256-pub`) over the compressed point. Ed25519 is unchanged,
  and every existing claim keeps its id. `ParseKeypair` and the new
  `ParsePrivateKeyPEM` read a PKCS#8 PEM under either scheme, `EncodePublicKey` and
  `DecodePublicKey` frame and parse both, and the builder took a `crypto.Signer`
  already — so a key held where no Ed25519 type is published (Azure Key Vault, a
  Managed HSM) can now sign a claim this library accepts.
- `ErrEnvelopeScheme` — a claim names its scheme twice, in the protected header and
  in its pubkey's framing, and the two MUST agree (`V-SIGN`). A signature made under
  one scheme presented as the other's is refused, as is an envelope naming a scheme
  outside the two, which is refused as the envelope is read.
- `ParsePublicKeyPEM` / `LoadPublicKeyPEM` read a SubjectPublicKeyInfo PEM under
  either scheme, as `ParsePrivateKeyPEM` does for a PKCS#8 private key. The Ed25519
  pair stays for a caller wanting that type back.
- The published vector set gains an ES256 identity — `p256-contributor`, `p256-note`,
  and `rejected-scheme-disagreement`, a record naming EdDSA in its header while its
  contributor publishes a `p256-pub` key. `V-SIGN` therefore leaves
  `scripts/rule-vectors.allow` with a case rather than an excuse, taking the covered
  rules from 16 to 17. The set is published, generated at `v0.34.0-rc.1`, which
  `expectedGenerator` in `tests/vectors_test.go` now names: a downstream
  implementation receives the three cases, and a conformance run here is judged
  against them.
- `adapter/storage/azure` — an Azure Blob Storage backend, the object-store
  counterpart of `adapter/storage/s3`: `azure.New(client, container)` keys claims,
  content and bookmarks by their id strings as block blobs in one container, streams
  content off the download response, and stores each blob under a conditional write
  so a re-put of content-addressed bytes costs no upload. `WithConcurrency` sets the
  bulk fan-out, `ReadOnly` suppresses the capability probe's sentinel write for an
  immutable container. It adds the matrix's `azure` row, which runs against Azurite —
  `services/azurite.sh native up`, and `RANKE_AZURE_ENDPOINT` points a run at it.
- `Universe.Bookmarks() BookmarkStore` — a backend's 𝒰_hist, the second address
  scheme keyed on `id_seq(i, s)`. Every `Universe` implementation must answer it.
  The Universe owning the store is what lets a bookmark list inherit the layering,
  replication and backup of the storage beneath it.
- `Capabilities.Bookmarks` — whether a backend can hold an archive's locator. True
  for every in-tree adapter but neo4j, which is a projection you drop and reindex.
  A `stack` reports it when an authoritative layer has it; a `partition` when every
  shard does, the list being replicated to all of them.
- `UnsupportedBookmarks()` — the store a Universe reporting `Bookmarks` false hands
  out, answering `ErrUnsupported`.
- `BookmarkLocator`, with two arms carrying two contracts: `Seed(s)` for a list that
  starts at index 0 and is never pruned, and `At(id)` for a pruned one, opened from a
  surviving entry whose record yields the seed. `Open` resolves either against a
  Universe.
- `MintSeed()` — a fresh 128-bit list seed (`V-BMENV`), for whoever founds a list and
  keeps the value.
- `Sequencer.InGenesis()` and `Sequencer.Found(ctx, pubkey, branch)`. An archive now
  comes into being through `Found` alone, as one operation writing the Sequencer's
  initial claim, the first contributor under it, the empty table k₀, and a second
  table binding `branch` to that contributor. Only the second is bookmarked, so a
  crash part-way leaves no archive and a retry writes the same ids. It returns that
  contributor's claim — `V-SIG` lets only the Sequencer key sign one, so the caller
  hands over a public key and keeps its private half. A second call is refused, and
  `ErrSequencerGenesis` answers every other operation until it succeeds.

  The branch is what makes the contributor reachable. `V-ARCHIVEHEIGHT` allows k₀ one
  reference, so it cannot name the contributor, and a founded archive used to hold no
  branch at all: the claim sat in 𝒰 referenced by nothing, its id recoverable only
  from what `Found` returned. A caller who restarted before contributing had an
  archive nobody could ever write to.
- `ParseKeypair`, `ParseEd25519PrivateKeyPEM` and `ParseEd25519PublicKeyPEM` read a
  key from bytes, a key arriving as readily from an environment variable, a pipe or a
  paste as from a file. The three `Load*` functions keep their signatures and are now
  these over `os.ReadFile`. Without them a consumer re-implemented `pem.Decode` →
  `ParsePKCS8PrivateKey` → the Ed25519 assertion → `EncodePublicKey`, error wording
  included.
- An encrypted key opens, and an unencrypted one asks for nothing. `WithPassphrase`
  supplies one; `WithPassphraseFrom` fetches it only once the key proves encrypted, so
  a tool configured with `--key-passphrase prompt` and handed a plaintext key never
  prompts. `IsEncryptedKey` reports it outright, for a caller that wants to branch
  itself. Both options reach `ParseKeypair`, `ParseEd25519PrivateKeyPEM`,
  `LoadPrivateKey` and `LoadEd25519PrivateKeyPEM`.

  The passphrase is read where the key is loaded and nowhere else, so signing a hundred
  claims costs one. An encrypted key without a passphrase now says so: it used to fail
  as `asn1: structure error: tags don't match`, which names nothing an operator can act
  on. `ErrKeyFormat` does the same for `OPENSSH PRIVATE KEY`, which `ssh-keygen` writes
  and no passphrase converts.

  Decryption is PKCS#8 under PBES2 (`github.com/youmark/pkcs8`, a new dependency whose
  own reach is `x/crypto`'s pbkdf2 and scrypt) and RFC 1423's legacy header form is
  recognised well enough to name itself.
- Package `keysource` — the one grammar an app resolves a key argument through, so the
  rules that keep material off disk and off the command line are written once rather
  than in each tool. `Load(spec, in, opts...)` is the whole of what an app does at
  startup; `Parse` checks the spelling while touching nothing, and `Spec.Read` performs
  the I/O, so a rotating secret is fetched where it signs rather than at launch.

  The spellings are a bare path or `file:PATH`, `env:NAME`, `stdin`, and `prompt` under
  `WithTTY()`. Three refusals come with them: a key file others can read (ssh's rule,
  for ssh's reason), material passed where a source belongs — reported as *compromised
  and to be rotated*, since a command line reaches the process table, the shell history
  and any CI log — and a prompt without the opt-in, which is how a server is stopped
  from blocking on a terminal read. A scheme it does not serve is refused rather than
  read as a filename, so `evn:KEY` names the typo instead of a missing file.

  It yields bytes and knows nothing of keys, so the same grammar serves a passphrase
  that is not a ranke key at all.
- Package `queries` — the reads a caller needs before it can write anything, written
  as ordinary RQL so it serves as a worked example as much as a library.
  `Contributors(ctx, arc, branch)` lists a branch's contributors;
  `ContributorsByKey(ctx, arc, branch, pubkey)` returns those carrying a public key,
  one of whose ids its holder must reference to sign (`V-SIG`). Several can match —
  no rule makes a `pubkey` unique, so one key registered by two contributors is two
  identities carrying different provenance, and choosing between them is the
  caller's. RQL filters on a claim's shape rather than its content, so the key match
  is made over the set, which is people and agents rather than claims.
- `ValidateBranchName` and `ErrBranchName`. A branch name is at most 128 bytes over
  `[a-z0-9_]` with no leading `_`, the form `R-FIELDS` gives a name, checked by
  `Found` and by every contribution that creates a branch. The charset admits no `$`,
  so a branch can no longer take a reserved target's name and be shadowed by it at
  read time.
- `HeightResolver`, and the resolvers `FixedHeight(h)`, `HeightsIn(u)` and
  `HeightsFrom(claims ...Claim)`, plus `ClaimBuilder.WithHeightResolver(ctx, resolve)`.
  A claim's height is now answered by one mechanism: a resolver, asked about every
  reference the assembled claim carries — the contributor edge included. So a caller
  whose heights live somewhere other than a `Universe`, such as a database or claims
  already in memory, supplies the lookup instead of a store, and `HeightsFrom`
  reports an absent reference rather than treating it as height 0.

### Changed

- A contribution the Sequencer refuses now carries the rule it broke. `Failure` is an
  `error`, so both Sequencer adapters return it as the cause: `errors.Is` reaches the
  rule and `errors.As` recovers the claim id and its depth in the walk. They flattened
  it into the message before, leaving a client to parse text or guess.
- `ErrHeightMismatch`, `ErrContributorUnresolved`, `ErrKeyWindowField`,
  `ErrRefsBranchTable` and `ErrUnexplainedGap` are exported, joining the ten
  verification rules already public. Every rule a claim can break is now matchable by
  the caller that gets told about it.
- `ClaimBuilder.Height` is a `HeightResolver` where it was a `uint64`. A struct
  literal states `Height: FixedHeight(HeightOf(refs...))` where it stated
  `Height: HeightOf(refs...)`; the chained `WithHeight(h)` is unchanged, being
  `FixedHeight` under another name. Height therefore has one slot, so `WithHeight`,
  `WithAutoHeight` and `WithHeightResolver` no longer conflict — the last one set
  answers, and the error that reported the conflict is gone. Claim bytes and ids are
  untouched: the resolved value is what it always was.
- `make test/full` is a correctness gate: it no longer runs the performance benchmark
  or the 10k-claim scale set. Both are development tools —
  `make test/performance/N` and `bin/ranke-test` drive the benchmark, `RANKE_SCALE=1`
  the scale set — and CI, which runs this target, was spending most of its time on
  them. The target gained a `-race` pass over the concurrency suite on the
  service-free rows, which is what its writer count was always sized for.
- `make test/full` no longer honours `FULL_PERF_SIZE`; the variable is gone. Size the
  benchmark through `make test/performance/N`.
- `dev.NewSequencer` and `concurrent.NewSequencer` take a `BookmarkLocator` and read
  the store off the Universe: `NewSequencer(ctx, u, loc, self, clock)`. Both refuse a
  Universe reporting `Capabilities.Bookmarks` false at construction, joining
  `ErrUnsupported` so the refusal stays matchable. `dev` no longer derives a seed of
  its own, so a reproducible run states the one it wants.
- `NewBookmarks(u, seed)` and `OpenBookmarks(ctx, u, id)` name a Universe rather than
  a store, which is what stops a caller pairing one Universe's 𝒰_hist with another.
  `NewBookmarks` now reports an error, refusing an empty seed.
- A bookmark list's seed, locator and entry index are fixed at construction. `Append`
  mints nothing and writes none of them, so `Seed()` and `BookmarkId()` answer the
  same value from any goroutine and never answer nil.

### Removed

- Package `client`, the HTTP client for a RankeDB server. ranke-go is the library the
  server is built on, so a client for that server belongs in the server's own
  repository, beside the OpenAPI contract it is written against — here it tracked a
  spec it could not see, and its tests answered a stub of its own. It moves to
  ranke-db. Nothing in ranke-go used it, and no repository that depends on ranke-go
  imported it; an outside consumer takes it from ranke-db instead.

  The wire format stays: `codec_wire` is the contribution stream, a CBOR sequence
  (RFC 8742), and is transport-independent — what an HTTP body carries rather than
  something HTTP defines. What left with the client is the *result* framing, chosen by
  media type (`application/json-seq`, `application/cbor-seq`), which is the endpoint
  contract's business.
- `NewMemoryBookmarks` and `fs.NewBookmarks`, and `storage.NewBlobBookmarks` is
  unexported. A bookmark store now comes from the Universe holding it and nowhere
  else, so a detached 𝒰_hist — a file bookmark list over an in-memory universe —
  cannot be expressed. Configuration carrying an independent history section has
  nothing left to point at.

### Fixed

- A restart reopens the archive its bookmark list records. Both `NewSequencer`
  implementations used to mint k₀ and append it unconditionally, so a relaunch over a
  persistent 𝒰_hist published a second and empty head above the real archive and left
  it unreachable. The constructor now writes nothing and takes its state from the
  list, branch heads included — without those a resumed merge would republish a
  branch as its new claims alone, dropping what it reached before.
- A comparison on a time field is held to one spelling (`R-QTIMEOP`): a `V-TIME`
  timestamp on `created_at`, `delete_by`, `pubkey_valid_from` and
  `pubkey_expires_after`, an EDTF Level 1 value on `dated`, and `ErrQueryTimeOperand`
  for anything else. Loosely written bounds used to be compared as text against the
  stored fixed-width form, which named the wrong instant — equality matched nothing,
  `ge` skipped the second it asked for and `lt` included it. `FormatTimestamp` renders
  the form a caller holding a `time.Time` needs.
- A merge no longer consolidates a branch's previous head into a new one that already
  reaches it. Revising the branch head used to mint a `contribution/head` over the
  revision and the claim it revises, which explains nothing and is not what the
  papers' own worked example shows.
