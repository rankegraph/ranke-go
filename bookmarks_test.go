package ranke

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- Bookmarks: Append at the next free index, the O(log n) searches that settle a
// list's range from any one of its entries, and what a damaged slot does to them
// (bookmarks.go). ---

// bmList stands up a Universe holding self, an empty 𝒰_hist, and a writer over both.
func bmList(t *testing.T) (context.Context, Universe, BookmarkStore, Contributor, *Bookmarks) {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	return ctx, u, u.Bookmarks(), self, bmOver(t, u, []byte("bm-list-seed"))
}

// bmOver is NewBookmarks where the seed is already settled, which every test's is.
func bmOver(t *testing.T, u Universe, seed []byte) *Bookmarks {
	t.Helper()
	b, err := NewBookmarks(u, seed)
	require.NoError(t, err)
	return b
}

// appendN appends n bookmarks under self, each recording a branch table of its own,
// and returns the recorded heads in index order.
func appendN(t *testing.T, ctx context.Context, u Universe, b *Bookmarks, self Contributor, base time.Time, n int) []Id {
	t.Helper()
	ids := make([]Id, n)
	for i := range n {
		head := bmHead(t, self, base.Add(time.Duration(i)*time.Second))
		putClaims(t, u, head)
		bm, err := b.Append(ctx, self, head.ID())
		require.NoError(t, err)
		require.Equal(t, uint64(i), bm.Index(),
			"Append derives the next index itself, never taking one from the caller")
		ids[i] = head.ID()
	}
	return ids
}

// plant writes a bookmark for (i, seed) recording a fresh branch table straight into
// hist, so a test can build a list of any shape — pruned, holed — that Append cannot.
func plant(t *testing.T, ctx context.Context, u Universe, hist BookmarkStore, self Contributor, seed []byte, i int) Id {
	t.Helper()
	// A month past the floor, so even the negative index a slot test plants dates a
	// record a claim may carry.
	head := bmHead(t, self, firstPossibleClaim.AddDate(0, 1, 0).Add(time.Duration(i)*time.Second))
	putClaims(t, u, head)
	raw, err := SignBookmark(self, uint64(i), seed, head.ID())
	require.NoError(t, err)
	slot, err := IdSeq(uint64(i), seed)
	require.NoError(t, err)
	require.NoError(t, hist.Put(ctx, slot, raw))
	return head.ID()
}

// TestBookmarksAppendAndLatest is the basic round trip: what Append writes, Latest,
// GetAtIndex and Len read back — on the writer's own settled instance and on an
// independent reader that has to search for it.
func TestBookmarksAppendAndLatest(t *testing.T) {
	ctx, u, _, self, w := bmList(t)
	ids := appendN(t, ctx, u, w, self, time.Now(), 3)

	n, err := w.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	latest, err := w.Latest(ctx)
	require.NoError(t, err)
	require.True(t, latest.Head().Equal(ids[2]))
	require.Equal(t, uint64(2), latest.Index())

	r, err := OpenBookmarks(ctx, u, w.BookmarkId())
	require.NoError(t, err)
	rn, err := r.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, rn, "an independent reader must search to the writer's own answer")
	for i, id := range ids {
		bm, err := r.GetAtIndex(ctx, i)
		require.NoError(t, err)
		require.True(t, bm.Head().Equal(id), "index %d", i)
	}
	require.NoError(t, r.Verify(ctx), "the list the writer built holds together")
}

// TestBookmarksLatestSearchBoundaries exercises the doubling/bisection search
// (§Bookmarks) at the counts most likely to expose an off-by-one: empty, exactly one,
// exactly at a doubling boundary, and one past it. Each reader is opened at the
// list's TOP entry, so the search runs downward as well as up.
func TestBookmarksLatestSearchBoundaries(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4, 5, 7, 8, 9, 16, 17} {
		t.Run("n="+strconv.Itoa(n), func(t *testing.T) {
			ctx, u, _, self, w := bmList(t)
			if n == 0 {
				// The seed is settled but index 0 was never written, so the list is
				// empty and the search has nothing to land on.
				latest, err := w.Latest(ctx)
				require.NoError(t, err)
				require.Nil(t, latest.Head())
				ln, err := w.Len(ctx)
				require.NoError(t, err)
				require.Equal(t, 0, ln)
				return
			}
			ids := appendN(t, ctx, u, w, self, time.Now(), n)

			top, err := IdSeq(uint64(n-1), w.Seed())
			require.NoError(t, err)
			r, err := OpenBookmarks(ctx, u, top)
			require.NoError(t, err)
			latest, err := r.Latest(ctx)
			require.NoError(t, err)
			require.True(t, latest.Head().Equal(ids[n-1]), "the search must land on the true last entry")
			require.Equal(t, uint64(n-1), latest.Index())

			ln, err := r.Len(ctx)
			require.NoError(t, err)
			require.Equal(t, n, ln)

			_, err = r.GetAtIndex(ctx, n)
			require.ErrorIs(t, err, errBookmarkIndexRange,
				"one past the end must be refused, not found by an overshooting search")
		})
	}
}

// TestBookmarksAppendResumesFromASearchedIndex: an instance opened cold over an
// already-populated list — no settled range of its own — must still compute the right
// next index before its first Append, not only once it has written something itself.
func TestBookmarksAppendResumesFromASearchedIndex(t *testing.T) {
	ctx, u, _, self, w := bmList(t)
	appendN(t, ctx, u, w, self, time.Now(), 3) // indices 0, 1, 2

	resumed := bmOver(t, u, w.Seed()) // cold: never appended, nothing settled
	head := bmHead(t, self, time.Now().Add(100*time.Second))
	putClaims(t, u, head)
	bm, err := resumed.Append(ctx, self, head.ID())
	require.NoError(t, err)
	require.Equal(t, uint64(3), bm.Index(), "a cold instance must search before computing the next slot")

	final, err := bmOver(t, u, w.Seed()).Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, final)
}

// TestOpenBookmarksFindsTheLowerBound: 𝒰_hist may purge an entry, so a valid list's
// range can start above 0. Opening it finds that lower bound rather than assuming 0 —
// which is the difference between reading a pruned list and calling it six entries
// long, or empty.
func TestOpenBookmarksFindsTheLowerBound(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	hist := u.Bookmarks()
	seed := []byte("pruned-list-seed")

	var heads []Id
	for i := 3; i <= 5; i++ {
		heads = append(heads, plant(t, ctx, u, hist, self, seed, i))
	}

	// Opened at index 4, in the middle: neither bound is the entry it started from.
	open, err := IdSeq(4, seed)
	require.NoError(t, err)
	r, err := OpenBookmarks(ctx, u, open)
	require.NoError(t, err)

	n, err := r.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, n, "the list holds indices 3..5, so it is three entries long")

	latest, err := r.Latest(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), latest.Index())
	require.True(t, latest.Head().Equal(heads[2]))

	_, err = r.GetAtIndex(ctx, 2)
	require.ErrorIs(t, err, errBookmarkIndexRange, "index 2 was purged, so nothing answers for it")
	require.NoError(t, r.Verify(ctx), "a pruned list is still contiguous, so it holds together")
}

// TestOpenBookmarksRecoversFromAnyEntry is the round trip §Backup promises: one
// bookmark id alone, with no seed given out of band, recovers the list a writer
// built — and the entry it is opened at holds no privilege, index 0 included.
func TestOpenBookmarksRecoversFromAnyEntry(t *testing.T) {
	ctx, u, _, self, w := bmList(t)
	ids := appendN(t, ctx, u, w, self, time.Now(), 3)
	require.NotNil(t, w.BookmarkId(), "the id worth persisting is settled before any append")

	for i := range ids {
		slot, err := IdSeq(uint64(i), w.Seed())
		require.NoError(t, err)
		r, err := OpenBookmarks(ctx, u, slot)
		require.NoError(t, err)
		require.Equal(t, w.Seed(), r.Seed(), "the seed is read back out of the bookmark itself")

		latest, err := r.Latest(ctx)
		require.NoError(t, err)
		require.True(t, latest.Head().Equal(ids[2]), "opened at index %d, the latest head is the same", i)
	}
}

// TestOpenBookmarksRejectsWhatIsNoBookmark: an id that names no bookmark of a list
// must be refused outright, never treated as an empty list — which would read a
// present archive as one that has never been written to.
func TestOpenBookmarksRejectsWhatIsNoBookmark(t *testing.T) {
	ctx, u, hist, self, _ := bmList(t)

	_, err := OpenBookmarks(ctx, u, nil)
	require.ErrorIs(t, err, errBookmarkNilSlot)

	// An empty slot: nothing there says which list it would open.
	empty, err := IdSeq(0, []byte("never-written-seed"))
	require.NoError(t, err)
	_, err = OpenBookmarks(ctx, u, empty)
	require.ErrorIs(t, err, errBookmarkOpen)

	// Bytes that are not a signed record at all.
	require.NoError(t, hist.Put(ctx, empty, []byte("not cbor at all")))
	_, err = OpenBookmarks(ctx, u, empty)
	require.ErrorIs(t, err, errBookmarkForm)

	// A well-formed bookmark under an id its own (i, s) does not key: the seed a
	// reader would take from it opens a different list than the one it sits in.
	head := bmHead(t, self, time.Now())
	putClaims(t, u, head)
	raw, err := SignBookmark(self, 0, []byte("real-seed"), head.ID())
	require.NoError(t, err)
	wrong, err := IdSeq(0, []byte("another-lists-seed"))
	require.NoError(t, err)
	require.NoError(t, hist.Put(ctx, wrong, raw))
	_, err = OpenBookmarks(ctx, u, wrong)
	require.ErrorIs(t, err, errBookmarkSlot,
		"a bookmark whose own (i, s) does not reproduce the slot it sits under must be refused")
}

// TestOpenBookmarksSaysSoWhenItsAnchorGoes: a reader's entry is its only way into the
// list, since the seed comes out of that record. Once it is gone the reader cannot
// locate the list at all, and saying so is the alternative to reporting a live archive
// as one that was never written to.
func TestOpenBookmarksSaysSoWhenItsAnchorGoes(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	hist := u.Bookmarks()
	seed := []byte("anchor-goes-seed")
	plant(t, ctx, u, hist, self, seed, 0)
	head := plant(t, ctx, u, hist, self, seed, 1)

	slot1, err := IdSeq(1, seed)
	require.NoError(t, err)
	r, err := OpenBookmarks(ctx, u, slot1)
	require.NoError(t, err)
	latest, err := r.Latest(ctx)
	require.NoError(t, err)
	require.True(t, latest.Head().Equal(head))

	// 𝒰_hist may overwrite a slot, so the entry it was opened at can stop answering
	// for index 1 — here by holding a bookmark that keys index 9 instead.
	relocated, err := SignBookmark(self, 9, seed, head)
	require.NoError(t, err)
	require.NoError(t, hist.Put(ctx, slot1, relocated))

	_, err = r.Len(ctx)
	require.ErrorIs(t, err, errBookmarkOpen)
}

// TestBookmarksSlotMismatchReadsAsAbsence is the read-path half of `V-BMSLOT`: a
// relocated bookmark is absence at the slot it was found at, so the search stops
// there rather than reporting a head the list never recorded at that index.
func TestBookmarksSlotMismatchReadsAsAbsence(t *testing.T) {
	ctx, u, hist, self, w := bmList(t)
	appendN(t, ctx, u, w, self, time.Now(), 1) // the one legitimate entry, index 0

	// A valid bookmark for index 5, filed by hand at index 1's slot.
	head := bmHead(t, self, time.Now().Add(10*time.Second))
	putClaims(t, u, head)
	raw, err := SignBookmark(self, 5, w.Seed(), head.ID())
	require.NoError(t, err)
	slot1, err := IdSeq(1, w.Seed())
	require.NoError(t, err)
	require.NoError(t, hist.Put(ctx, slot1, raw))

	r := bmOver(t, u, w.Seed())
	_, err = r.GetAtIndex(ctx, 1)
	require.ErrorIs(t, err, errBookmarkIndexRange, "a bookmark declaring index 5 must not answer for slot 1")

	latest, err := r.Latest(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(0), latest.Index(), "the list ends at the last entry that keys its own slot")
}

// TestBookmarksReportsADamagedSlot: a slot holds a well-formed bookmark for (i, s) or
// it does not, and one that does not is damage rather than a hole. Read as absence it
// would end the search early, leaving Latest to report a stale head as current.
func TestBookmarksReportsADamagedSlot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, ctx context.Context, u Universe, hist BookmarkStore, self Contributor, slot Id)
	}{
		{"undecodable", func(t *testing.T, ctx context.Context, _ Universe, hist BookmarkStore, _ Contributor, slot Id) {
			require.NoError(t, hist.Put(ctx, slot, []byte("not cbor at all")))
		}},
		{"unresolvable signer", func(t *testing.T, ctx context.Context, u Universe, hist BookmarkStore, _ Contributor, slot Id) {
			// Signed under a contributor this archive never registered, so no pubkey
			// answers for it: well-formed as a record, not as a bookmark of this list.
			other := contributor(t)
			head := bmHead(t, other, time.Now().Add(10*time.Second))
			putClaims(t, u, head)
			raw, err := SignBookmark(other, 1, []byte("damaged-seed"), head.ID())
			require.NoError(t, err)
			require.NoError(t, hist.Put(ctx, slot, raw))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			u := NewMemoryUniverse()
			self := contributor(t)
			putClaims(t, u, self)
			hist := u.Bookmarks()
			seed := []byte("damaged-seed")
			legitimate := plant(t, ctx, u, hist, self, seed, 0)

			slot1, err := IdSeq(1, seed)
			require.NoError(t, err)
			tc.plant(t, ctx, u, hist, self, slot1)

			r := bmOver(t, u, seed)
			_, err = r.Latest(ctx)
			require.ErrorIs(t, err, errBookmarkSlotRead,
				"a damaged slot must be reported, not read as the end of the list")

			// Asked for the slot directly, the damage is what is reported. Collapsing it
			// into a range error would name the wrong problem: the entry IS there.
			_, err = bmOver(t, u, seed).GetAtIndex(ctx, 1)
			require.ErrorIs(t, err, errBookmarkSlotRead)
			require.NotErrorIs(t, err, errBookmarkIndexRange,
				"the record is present and unreadable, not outside the list's range")

			// And the whole-list walk reports damage rather than the gap it would leave.
			_, err = bmOver(t, u, seed).GetBulk(ctx, 0, 2)
			require.ErrorIs(t, err, errBookmarkSlotRead)
			require.NotErrorIs(t, err, errBookmarkIndexRange)

			// The legitimate entry below the damage is still readable on its own.
			fresh := bmOver(t, u, seed)
			bm, err := fresh.GetAtIndex(ctx, 0)
			require.NoError(t, err)
			require.True(t, bm.Head().Equal(legitimate))
		})
	}
}

// TestOpenBookmarksRefusesAGapAboveTheTop is `V-BMGAPLESS` at initial load: the search
// settles a top by finding the first miss above it, and an entry present past that
// miss says the range is not contiguous. The probe is what turns a settled boundary
// into a falsifiable claim rather than an assumption.
func TestOpenBookmarksRefusesAGapAboveTheTop(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	hist := u.Bookmarks()
	seed := []byte("gap-above-seed")

	plant(t, ctx, u, hist, self, seed, 0)
	plant(t, ctx, u, hist, self, seed, 2) // index 1 is the hole

	open, err := IdSeq(0, seed)
	require.NoError(t, err)
	_, err = OpenBookmarks(ctx, u, open)
	require.ErrorIs(t, err, errBookmarkGap)
}

// TestOpenBookmarksGapProbeIsBounded pins the incompleteness the probe is designed
// with: the index space is unbounded, so a fixed number of reads cannot settle
// contiguity. An entry beyond the probe's reach is not found, and the list opens.
// Verify is where the whole range is checked (-> TestBookmarksVerifyFindsAnInnerGap).
func TestOpenBookmarksGapProbeIsBounded(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	hist := u.Bookmarks()
	seed := []byte("gap-out-of-reach-seed")

	plant(t, ctx, u, hist, self, seed, 0)
	plant(t, ctx, u, hist, self, seed, gapProbe+2) // one past the last index probed

	open, err := IdSeq(0, seed)
	require.NoError(t, err)
	r, err := OpenBookmarks(ctx, u, open)
	require.NoError(t, err, "a gap this far above the top is out of a bounded probe's reach")
	n, err := r.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

// TestBookmarksVerifyFindsAnInnerGap: the searches assume presence runs monotonically,
// so a hole inside a range can survive them — the doubling step can jump clean over
// it. Verify walks every index instead, which is what makes it the complete answer and
// why it is not on the read path.
func TestBookmarksVerifyFindsAnInnerGap(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	hist := u.Bookmarks()
	seed := []byte("inner-gap-seed")

	// The doubling probes 1, 2, 4, 8 and then bisects, so index 3 is never read and
	// the probe above the settled top of 8 reaches only as far as 13.
	for _, i := range []int{0, 1, 2, 4, 5, 6, 7, 8} { // index 3 is the hole it steps over
		plant(t, ctx, u, hist, self, seed, i)
	}

	open, err := IdSeq(0, seed)
	require.NoError(t, err)
	r, err := OpenBookmarks(ctx, u, open)
	require.NoError(t, err)
	latest, err := r.Latest(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(8), latest.Index(), "the cheap path reads the top it can reach")

	require.ErrorIs(t, r.Verify(ctx), errBookmarkGap, "the whole-range walk is what finds index 3")
}

// TestBookmarksVerifyChecksEveryHead is `V-BMREF` over a whole list: one entry
// recording something other than a branch table fails the list, wherever it sits.
func TestBookmarksVerifyChecksEveryHead(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	hist := u.Bookmarks()
	seed := []byte("bad-head-seed")

	plant(t, ctx, u, hist, self, seed, 0)
	note, err := NewClaim(TypeSource("note"), self).
		WithInlineContent([]byte("not a head")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(self)).
		Sign()
	require.NoError(t, err)
	putClaims(t, u, note)
	raw, err := SignBookmark(self, 1, seed, note.ID())
	require.NoError(t, err)
	slot1, err := IdSeq(1, seed)
	require.NoError(t, err)
	require.NoError(t, hist.Put(ctx, slot1, raw))

	open, err := IdSeq(0, seed)
	require.NoError(t, err)
	r, err := OpenBookmarks(ctx, u, open)
	require.NoError(t, err)
	require.ErrorIs(t, r.Verify(ctx), errBookmarkReference)
}

// TestBookmarksGetBulkRange: the half-open range, and the two ways to ask for one
// that is not a range at all.
func TestBookmarksGetBulkRange(t *testing.T) {
	ctx, u, hist, self, w := bmList(t)
	ids := appendN(t, ctx, u, w, self, time.Now(), 4)

	r, err := OpenBookmarks(ctx, u, w.BookmarkId())
	require.NoError(t, err)
	got, err := r.GetBulk(ctx, 1, 3)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.True(t, got[0].Head().Equal(ids[1]))
	require.True(t, got[1].Head().Equal(ids[2]))

	_, err = r.GetBulk(ctx, -1, 2)
	require.ErrorIs(t, err, errBookmarkRangeInvalid)
	_, err = r.GetBulk(ctx, 3, 1)
	require.ErrorIs(t, err, errBookmarkRangeInvalid)

	// An empty half-open range is well formed, and reads no slot: from == toExcluding
	// asks for nothing, including past the end where a read would have errored.
	for _, from := range []int{0, 2, 9} {
		got, err := r.GetBulk(ctx, from, from)
		require.NoError(t, err)
		require.Empty(t, got)
	}

	// A negative index has no slot, and uint64(-1) is a real one: a record planted
	// there must not answer for it.
	planted := plant(t, ctx, u, hist, self, w.Seed(), -1)
	require.NotNil(t, planted)
	_, err = r.GetAtIndex(ctx, -1)
	require.ErrorIs(t, err, errBookmarkIndexRange, "a negative index keys no slot at all")
}

// TestBookmarksAppendRequiresItsParts: Append signs per call, so a missing head or a
// missing signer is refused by the signing it delegates to, rather than reaching
// 𝒰_hist as an unattributed record.
func TestBookmarksAppendRequiresItsParts(t *testing.T) {
	ctx, u, _, self, w := bmList(t)
	head := bmHead(t, self, time.Now())
	putClaims(t, u, head)

	_, err := w.Append(ctx, self, nil)
	require.ErrorIs(t, err, errBookmarkNoHead)
	_, err = w.Append(ctx, nil, head.ID())
	require.ErrorIs(t, err, errBookmarkNoSigner)
}

// TestBookmarksKeyMaterialIsImmutable is the regression guard for the write this
// design removed: Append used to assign b.seed on every call and b.id on its first,
// while Seed() and BookmarkId() read both under no lock. Settled at construction,
// they are safe to read from any goroutine — and under -race this is what says so.
func TestBookmarksKeyMaterialIsImmutable(t *testing.T) {
	ctx, u, _, self, w := bmList(t)
	seed, id := w.Seed(), w.BookmarkId()
	require.NotEmpty(t, seed, "a constructed list carries its seed")
	require.NotNil(t, id, "and the locator that reopens it")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		appendN(t, ctx, u, w, self, time.Now(), 4)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 200 {
			require.Equal(t, seed, w.Seed())
			require.True(t, id.Equal(w.BookmarkId()))
		}
	}()
	wg.Wait()

	require.Equal(t, seed, w.Seed(), "four appends later, the same seed")
	require.True(t, id.Equal(w.BookmarkId()), "and the same locator")
}

// TestBookmarkLocatorArms: both arms settle the seed before the cursor exists — Seed
// carries it, At reads it out of the verified record its id names. Only the At arm
// opens a PRUNED list, where index 0 is gone and no seed alone would find the range.
func TestBookmarkLocatorArms(t *testing.T) {
	ctx, u, _, self, w := bmList(t)
	appendN(t, ctx, u, w, self, time.Now(), 3)

	bySeed, err := Seed(w.Seed()).Open(ctx, u)
	require.NoError(t, err)
	n, err := bySeed.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, n, "the seed arm reaches a list that starts at 0")

	pruned := NewMemoryUniverse()
	putClaims(t, pruned, self)
	seed := []byte("locator-pruned-seed")
	for i := 4; i <= 5; i++ {
		plant(t, ctx, pruned, pruned.Bookmarks(), self, seed, i)
	}
	slot, err := IdSeq(5, seed)
	require.NoError(t, err)

	byId, err := At(slot).Open(ctx, pruned)
	require.NoError(t, err)
	require.Equal(t, seed, byId.Seed(), "the seed comes out of the record the id names")
	pn, err := byId.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, pn, "indices 4..5 survive, so the list is two entries long")

	// The zero locator names no list at all, rather than defaulting to one.
	_, err = BookmarkLocator{}.Open(ctx, u)
	require.ErrorIs(t, err, errBookmarkNoSeed)
}
