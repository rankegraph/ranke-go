# Changelog

What each release changed for someone depending on this repository.

## Unreleased

### Added

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
