// package: adapter/storage / bookmarkstore
// type:    logic
// job:     the 𝒰_hist a blob-backed Universe hands out — over the same BlobStore seam, its entries
// under a key prefix that keeps the two keyspaces apart (`R-BMPREFIX`)
// limits:  no storage of its own; the record's shape and its rules are the library's
// (-> blob.go for the Universe half)
package storage

import (
	"context"

	"github.com/rankegraph/ranke-go"
)

// bookmarkPrefix separates 𝒰_hist's keys from 𝒰's in one physical store
// (`R-BMPREFIX`). It has to: external content whose bytes are S([i, s]) is stored at
// H(c) = id_seq(i, s), a bookmark's slot, so unprefixed the two would collide there.
// Slash-free, so a flat key namespace needs no directory to hold it.
const bookmarkPrefix = "hist-"

// newBlobBookmarks adapts a BlobStore into a ranke.BookmarkStore, so one directory or
// bucket holds an archive's claims, its content and its bookmark list. Unexported, so
// the Universe owning the store is the only way to a 𝒰_hist over it.
func newBlobBookmarks(store BlobStore) ranke.BookmarkStore {
	return &blobBookmarks{store: store}
}

type blobBookmarks struct{ store BlobStore }

func (b *blobBookmarks) Get(ctx context.Context, key ranke.Id) ([]byte, error) {
	if key == nil {
		return nil, errNilID
	}
	return b.store.Get(ctx, bookmarkPrefix+key.String())
}

func (b *blobBookmarks) Put(ctx context.Context, key ranke.Id, record []byte) error {
	if key == nil {
		return errNilID
	}
	return b.store.Put(ctx, bookmarkPrefix+key.String(), record)
}
