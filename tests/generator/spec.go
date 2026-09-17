// package: tests/generator / testkit
// type:    tool
// job:     Spec — the full knob set the generator builds from, plus SpecForSize which derives every
// knob from a single `size`
// limits:  pure configuration + derivation, no building (-> generate.go); knobs the ranke API
// cannot yet express are marked BLOCKED and ignored by the builder
package generator

import "time"

// Spec is the complete, fine-tunable description of a graph to generate; the
// same Spec yields the same archive, ids included. Start from SpecForSize and
// override single fields to stress one corner — every other corner stays in.
type Spec struct {
	// Seed fixes every derived key (via keyForIndex), every blob, and every id.
	Seed int64
	// Size is the headline knob SpecForSize scales the rest from, kept for the
	// manifest; the builder reads the derived fields.
	Size int

	// Base is the clock's starting instant, fixed for reproducibility.
	Base time.Time
	// Step is how far the clock advances per claim.
	Step time.Duration

	// --- population knobs (all counts) ---

	// Contributors is how many distinct signing contributors author claims
	// (besides the operator the Sequencer signs branch tables with).
	Contributors int
	// Sources is the count of source/* claims (ingested artifacts).
	Sources int
	// Derivations is the count of derivation/* claims, each citing one or more
	// sources via derivation edges.
	Derivations int
	// Entities is the count of entity/* claims, each citing a source.
	Entities int
	// HandMadeEntities is the count of entity/* claims carrying NO derivation
	// edge — what a person typing an entity in produces, with nothing to cite.
	HandMadeEntities int
	// Relations is the count of relation/* claims, each wiring two entities
	// with a from/to edge pair.
	Relations int

	// --- shape knobs ---

	// DiffChainLen is the length of the longest contribution/diff chain: one
	// base source plus this many revisions overlaying it.
	DiffChainLen int
	// ExternalBlobs is how many source claims carry external (hash-addressed)
	// content in the Universe rather than inline bytes.
	ExternalBlobs int
	// TinyBlobBytes / LargeBlobBytes bound the inline content sizes, so the
	// archive holds both a tiny-blob and a large-blob corner.
	TinyBlobBytes  int
	LargeBlobBytes int
	// OversizedFieldBytes is the length of a large field value placed on one
	// claim (0 disables the corner), within the ADT's 64 KiB field-value cap.
	OversizedFieldBytes int
	// DatedSources is how many of the leading source/note claims carry `dated`
	// (`V-DATED`), an EDTF Level 1 value; 0 leaves every claim without one.
	DatedSources int
	// MaxEdgeDegree is the fan-out of the highest-degree derivation.
	MaxEdgeDegree int

	// KeyExpiries is how many contribution/expiry claims to mint, each with an
	// edge to a contributor and a pubkey_expires_after field (paper §Types).
	KeyExpiries int
	// Deletes is how many contribution/delete tombstones to mint, each naming a
	// claim by id as a removed-bytes gap (paper §Types).
	Deletes int

	// Branches is how many named branches the contributions spread across;
	// 0 derives a count from the claim total. "main" is always the first.
	Branches int
}

// SpecForSize derives a full Spec from one size knob, linearly, with every
// corner present at either scale. Shape knobs grow sublinearly, so a large
// archive is mostly breadth.
func SpecForSize(seed int64, size int) Spec {
	if size < 1 {
		size = 1
	}
	return Spec{
		Seed: seed,
		Size: size,
		Base: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Step: time.Second,

		Contributors:     clampMin(size/10, 2),
		Sources:          size * 2,
		Derivations:      size,
		Entities:         size,
		HandMadeEntities: clampMin(size/5, 1),
		Relations:        size,

		DiffChainLen:        clampMin(ilog2(size)*2, 3),
		ExternalBlobs:       clampMin(size/5, 1),
		TinyBlobBytes:       8,
		LargeBlobBytes:      clampMin(size*64, 4096),
		OversizedFieldBytes: 60 * 1024, // large, under the 64 KiB field cap
		MaxEdgeDegree:       clampMin(ilog2(size)+2, 3),

		KeyExpiries: clampMin(size/20, 1),
		// Deletes off by default: purging bytes is a RankeDB-level concern.
		Deletes: 0,
	}
}

// spineClaims is the near-constant branch-table/head overhead of an archive.
const spineClaims = 350

// SpecForNodes scales an archive to roughly nodes total claims, as the CLI's
// --size (1k, 1m, …) does: content claims run ~5.2× the size unit, plus spine. The
// divisor below stays 5, so the count overshoots by about 4% — Size is a dimension,
// not a promise (-> tests/performance/harness.go).
func SpecForNodes(seed int64, nodes int) Spec {
	s := SpecForSize(seed, clampMin((nodes-spineClaims)/5, 1))
	s.Size = nodes // the headline reflects the requested node count, not the unit
	return s
}

func clampMin(v, min int) int {
	if v < min {
		return min
	}
	return v
}

// ilog2 returns floor(log2(n)) for n>=1, else 0 — a cheap sublinear scale.
func ilog2(n int) int {
	l := 0
	for n > 1 {
		n >>= 1
		l++
	}
	return l
}
