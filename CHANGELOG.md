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
- `Sequencer.InGenesis()` and `Sequencer.Found(ctx, pubkey)`. An archive now comes
  into being through `Found` alone, as one operation writing the Sequencer's initial
  claim, the first contributor under it, the empty branch table k₀ and its bookmark.
  It returns that contributor's claim — `V-SIG` lets only the Sequencer key sign one,
  so the caller hands over a public key and keeps its private half. A second call is
  refused, and `ErrSequencerGenesis` answers every other operation until it succeeds.

### Changed

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
