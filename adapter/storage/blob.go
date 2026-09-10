// Byte-oriented backends (mem, fs, sqlite, s3, …) differ only in how they store
// bytes by key, so the shared claim/content/copy machinery lives here.

// package: adapter/storage / blobstore
// type:    logic
// job:     the BlobStore seam — three byte primitives (Get/Put/Has) become a full ranke.Universe
// limits:  no storage of its own; concrete blob backends live in sub-packages (-> adapter/fs, adapter/mem)
package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/rankegraph/ranke-go"
)

var (
	errNilID    = errors.New("adapter: nil id")
	errNilClaim = errors.New("adapter: nil claim or id")
	errNilHash  = errors.New("adapter: nil content hash")
)

// BlobStore is the minimal backing store a Universe adapter must provide: opaque
// bytes by string key, one namespace for claim ids and content hashes alike.
type BlobStore interface {
	// Get returns the bytes stored under key, or ranke.ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
	// Put stores data under key, overwriting any bytes there — the overwrite is
	// what lets a read-through repair restore a corrupted entry.
	Put(ctx context.Context, key string, data []byte) error
	// Has reports whether key is present.
	Has(ctx context.Context, key string) (bool, error)
	// Delete removes the bytes at key, absence being no error. A store whose
	// Capabilities report Delete false returns ranke.ErrUnsupported.
	Delete(ctx context.Context, key string) error
	// Capabilities reports the backend's nature — durable medium, delete,
	// enumerate (see ranke.Capabilities) — which becomes the Universe's.
	Capabilities() ranke.Capabilities
	// Close releases any resources the store holds.
	Close() error
}

// Streamer is the optional BlobStore extension for backends that hand back a raw
// byte stream, letting StreamContent verify on the fly instead of buffering.
type Streamer interface {
	// Open returns a raw, unverified byte stream for key; the caller verifies it.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// NewBlobUniverse adapts a BlobStore into a full ranke.Universe: claim codec,
// content integrity, bulk loops, and the default copy walkers.
//
//deadcode:keep
func NewBlobUniverse(store BlobStore, opts ...BlobUniverseOption) ranke.Universe {
	u := &blobUniverse{store: store, concurrency: 1, tier: ranke.StorageTierAuthoritative, heights: ranke.NewHeightCache(),
		marks: newBlobBookmarks(store)}
	for _, o := range opts {
		o(u)
	}
	if u.concurrency > 1 {
		// One semaphore for the whole Universe — the concurrency cap is a
		// TOTAL across every (possibly concurrent) call, not per call.
		u.sem = make(chan struct{}, u.concurrency)
	}
	return u
}

// BlobUniverseOption configures the Universe wrapper.
type BlobUniverseOption func(*blobUniverse)

// WithConcurrency caps in-flight per-key store operations as a TOTAL over the
// whole Universe; n<=1 keeps ops sequential. Network backends fan out with it.
func WithConcurrency(n int) BlobUniverseOption {
	return func(u *blobUniverse) {
		if n > 0 {
			u.concurrency = n
		}
	}
}

// WithTier sets the write-durability role in a stack (Capabilities.Tier);
// verbatim claims and unbounded content make any tier valid. Default authoritative.
func WithTier(t ranke.StorageTier) BlobUniverseOption {
	return func(u *blobUniverse) { u.tier = t }
}

type blobUniverse struct {
	store       BlobStore
	concurrency int
	// tier is the write role reported as Capabilities.Tier for a stack to route by.
	tier ranke.StorageTier
	// sem bounds store operations across the whole Universe, shared by every forEach.
	sem chan struct{}
	// heights memoises id→height, warmed by GetClaims/PutClaims traffic.
	heights *ranke.HeightCache
	// marks is 𝒰_hist over the same store, its keys held apart by a prefix.
	marks ranke.BookmarkStore
}

// forEach runs fn for each index in [0,n), up to u.sem at a time. fn writes to
// its own slot i; the first error is returned and cancels the rest.
func (u *blobUniverse) forEach(ctx context.Context, n int, fn func(context.Context, int) error) error {
	if u.sem == nil {
		for i := 0; i < n; i++ {
			if err := fn(ctx, i); err != nil {
				return err
			}
		}
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ferr error
	)
launch:
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done(): // cancelled (or a sibling failed) while waiting for a slot
			break launch
		case u.sem <- struct{}{}: // acquire a shared slot
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-u.sem }()
			if err := fn(ctx, i); err != nil {
				mu.Lock()
				if ferr == nil {
					ferr = err
					cancel()
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ferr != nil {
		return ferr
	}
	return ctx.Err()
}

//deadcode:keep
func (u *blobUniverse) Close() error { return u.store.Close() }

//deadcode:keep
func (u *blobUniverse) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	err := u.forEach(ctx, len(ids), func(ctx context.Context, i int) error {
		id := ids[i]
		if id == nil {
			return errNilID
		}
		data, err := u.store.Get(ctx, id.String())
		if err != nil {
			return err
		}
		c, err := ranke.DecodeClaim(id, data)
		if err != nil {
			return err
		}
		out[i] = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Warm the height cache from the raw claims, before materialisation.
	u.heights.NoteClaims(out...)
	// Materialise diff overlays after the parallel fetch, on complete claims.
	return ranke.DefaultMaterialize(ctx, u, out, opts...)
}

//deadcode:keep
func (u *blobUniverse) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	if err := u.forEach(ctx, len(cs), func(ctx context.Context, i int) error {
		c := cs[i]
		if c == nil || c.ID() == nil {
			return errNilClaim
		}
		data, err := c.Envelope()
		if err != nil {
			return err
		}
		return u.store.Put(ctx, c.ID().String(), data)
	}); err != nil {
		return err
	}
	// Every stored claim is one we hold in hand — warm the height cache.
	u.heights.NoteClaims(cs...)
	return nil
}

//deadcode:keep
func (u *blobUniverse) HasClaims(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	return u.hasAll(ctx, ids)
}

//deadcode:keep
func (u *blobUniverse) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	err := u.forEach(ctx, len(refs), func(ctx context.Context, i int) error {
		ref := refs[i]
		if ref.Hash == nil {
			return errNilHash
		}
		data, err := u.store.Get(ctx, ref.Hash.String())
		if err != nil {
			return err
		}
		if err := ranke.VerifyContent(ref.Hash, ref.ContentSize, data); err != nil {
			return err
		}
		out[i] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

//deadcode:keep
func (u *blobUniverse) StreamContent(ctx context.Context, hash ranke.Id, size uint64) (io.ReadCloser, error) {
	if hash == nil {
		return nil, errNilHash
	}
	// True streaming when the store offers a raw stream, else buffer in memory.
	if s, ok := u.store.(Streamer); ok {
		raw, err := s.Open(ctx, hash.String())
		if err != nil {
			return nil, err
		}
		r, err := ranke.NewVerifyingReader(raw, hash, size)
		if err != nil {
			_ = raw.Close()
			return nil, err
		}
		return r, nil
	}
	data, err := u.store.Get(ctx, hash.String())
	if err != nil {
		return nil, err
	}
	return ranke.NewVerifyingReader(io.NopCloser(bytes.NewReader(data)), hash, size)
}

//deadcode:keep
func (u *blobUniverse) PutContents(ctx context.Context, blobs []ranke.ContentBlob) error {
	for _, bl := range blobs {
		if bl.Hash == nil {
			return errNilHash
		}
		if err := u.store.Put(ctx, bl.Hash.String(), bl.Content); err != nil {
			return err
		}
	}
	return nil
}

//deadcode:keep
func (u *blobUniverse) HasContents(ctx context.Context, hashes []ranke.Id) ([]bool, error) {
	return u.hasAll(ctx, hashes)
}

// hasAll asks the flat key namespace for each id, tolerating nil entries.
// DeleteClaims removes each id's bytes through the store, refusing outright where the
// medium cannot delete — a WORM bucket must not report a deletion it did not make.
func (u *blobUniverse) DeleteClaims(ctx context.Context, ids []ranke.Id) error {
	return u.deleteAll(ctx, ids)
}

// DeleteContents removes each blob, on DeleteClaims' terms. Claims and content share
// one key space here, so one helper serves both.
func (u *blobUniverse) DeleteContents(ctx context.Context, hashes []ranke.Id) error {
	return u.deleteAll(ctx, hashes)
}

// deleteAll removes every key, gated once on the capability rather than per key.
func (u *blobUniverse) deleteAll(ctx context.Context, ids []ranke.Id) error {
	if !u.store.Capabilities().Delete {
		return ranke.ErrUnsupported
	}
	return u.forEach(ctx, len(ids), func(ctx context.Context, i int) error {
		if ids[i] == nil {
			return errNilHash
		}
		return u.store.Delete(ctx, ids[i].String())
	})
}

func (u *blobUniverse) hasAll(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	out := make([]bool, len(ids))
	err := u.forEach(ctx, len(ids), func(ctx context.Context, i int) error {
		id := ids[i]
		if id == nil {
			return nil
		}
		has, err := u.store.Has(ctx, id.String())
		if err != nil {
			return err
		}
		out[i] = has
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

//deadcode:keep
func (u *blobUniverse) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyClaims(ctx, u, src, ids, opts...)
}

//deadcode:keep
func (u *blobUniverse) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyContents(ctx, u, src, refs, opts...)
}

// Bookmarks returns 𝒰_hist over the same BlobStore, which is what puts an
// archive's claims, its content and its locator in one directory or bucket
// (RankeDB paper §Sequencer). An append writes a fresh id_seq(i, s), so the list
// asks the store for no overwrite.
//
//deadcode:keep
func (u *blobUniverse) Bookmarks() ranke.BookmarkStore { return u.marks }

// Capabilities surfaces the backing store's declared capabilities.
//
//deadcode:keep
func (u *blobUniverse) Capabilities() ranke.Capabilities {
	c := u.store.Capabilities()
	c.RawClaims = true       // a blob store keeps each claim's verbatim canonical bytes
	c.ExternalContent = true // and holds externalized content of any size (no cap)
	c.Bookmarks = true       // and 𝒰_hist beside them under R-BMPREFIX
	c.Tier = u.tier          // the write role the deployment configured (default authoritative)
	return c
}

// Sync fills from src (a no-op when src is nil).
func (u *blobUniverse) Sync(ctx context.Context, src ranke.Universe, id ranke.Id) <-chan ranke.SyncResult {
	return ranke.DefaultSync(ctx, u, src, id)
}

// GetClaimHeights answers from the id→height cache, decoding and caching
// whatever ids are new to it.
func (u *blobUniverse) GetClaimHeights(ctx context.Context, ids []ranke.Id) ([]uint64, error) {
	return u.heights.GetClaimHeights(ctx, u, ids)
}

// ClaimsInBranches walks, a byte store keeping no branch index of its own.
//
//deadcode:keep
func (u *blobUniverse) ClaimsInBranches(ctx context.Context, branches map[string]ranke.Id, ids []ranke.Id) ([]bool, error) {
	return ranke.DefaultClaimsInBranches(ctx, u, branches, ids)
}

// Query answers an RQL read via the reference executor over this byte store;
// uses/connections work via closure inversion (no native reverse index).
func (u *blobUniverse) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	return ranke.DefaultQuery(ctx, u, q, scope)
}

// GetClaimTags reports ErrUnsupported: an opaque byte store holds only the
// signed bytes, no mutable side-data (Capabilities.Tags is false).
//
//deadcode:keep
func (u *blobUniverse) GetClaimTags(_ context.Context, _ []ranke.Id) ([]map[string]string, error) {
	return nil, ranke.ErrUnsupported
}

// SetClaimsTags is unsupported (see GetClaimTags).
//
//deadcode:keep
func (u *blobUniverse) SetClaimsTags(_ context.Context, _ []string, _ map[string]map[string]string) error {
	return ranke.ErrUnsupported
}

// Tag is a no-op: reads against a byte store consult no tags.
func (u *blobUniverse) Tag(_ context.Context, _ ranke.Id) error { return nil }

// GetClaimsRaw returns each claim's stored bytes: the CBOR its id was signed over.
//
//deadcode:keep
func (u *blobUniverse) GetClaimsRaw(ctx context.Context, ids []ranke.Id) ([][]byte, error) {
	out := make([][]byte, len(ids))
	err := u.forEach(ctx, len(ids), func(ctx context.Context, i int) error {
		id := ids[i]
		if id == nil {
			return errNilID
		}
		data, err := u.store.Get(ctx, id.String())
		if err != nil {
			return err
		}
		out[i] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
