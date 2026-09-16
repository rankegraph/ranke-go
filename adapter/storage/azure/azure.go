// package: azure / persistence
// type:    adapter
// job:     stores claims and content blobs as block blobs in one Azure Blob Storage container
// limits:  no indexing or codec logic; a BlobStore behind storage.NewBlobUniverse (-> adapter)
//
// Package azure keys claims and blobs by their id strings in one container, the
// Azure counterpart of the s3 adapter. Open (storage.Streamer) streams large
// content off the download response. New takes a configured client, so a
// deployment brings its own credentials and a test points one at Azurite.
package azure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage"
)

var (
	errNilClient      = errors.New("adapter/azure.New: nil client")
	errEmptyContainer = errors.New("adapter/azure.New: empty container")
	errIO             = errors.New("adapter/azure: io")
)

// New returns an Azure-Blob-backed Universe over the given client and container.
// It assumes the container already exists and probes its capabilities (see
// probeCaps); pass ReadOnly for an immutable container so the probe never writes.
func New(client *azblob.Client, containerName string, opts ...Option) (ranke.Universe, error) {
	if client == nil {
		return nil, errNilClient
	}
	if containerName == "" {
		return nil, errEmptyContainer
	}
	cfg := config{concurrency: defaultConcurrency}
	for _, o := range opts {
		o(&cfg)
	}
	cc := client.ServiceClient().NewContainerClient(containerName)
	// NewBlobUniverse defaults a byte store to the authoritative tier, which Azure
	// Blob Storage is: a durable, verbatim source of truth.
	return storage.NewBlobUniverse(&store{
		container: cc,
		caps:      probeCaps(context.Background(), cc, cfg.readOnly),
	}, storage.WithConcurrency(cfg.concurrency)), nil
}

type store struct {
	container *container.Client
	caps      ranke.Capabilities
}

type config struct {
	readOnly    bool
	concurrency int
}

// defaultConcurrency is how many blobs bulk ops transfer in parallel: the service
// has no multi-blob read, so throughput comes from concurrent single-blob calls.
const defaultConcurrency = 16

// Option configures an azure store.
type Option func(*config)

// WithConcurrency sets how many blobs the bulk operations transfer in parallel,
// hiding the service's per-request latency; n<=1 forces sequential.
func WithConcurrency(n int) Option { return func(c *config) { c.concurrency = n } }

// ReadOnly declares the container read-only, append-only, or under an
// immutability policy, so the probe writes no sentinel: a write probe cannot tell
// those cases apart.
func ReadOnly() Option { return func(c *config) { c.readOnly = true } }

// sentinelKey is the harmless blob name the New-time capability probe writes.
const sentinelKey = "ranke-graph-sentinel"

// probeCaps learns the container's capabilities: a listing probes Enumerate, two
// sentinel uploads separate read-write from append-only, then a delete cleans up.
func probeCaps(ctx context.Context, cc *container.Client, readOnly bool) ranke.Capabilities {
	caps := ranke.Capabilities{Persistent: true}
	pager := cc.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{MaxResults: to.Ptr(int32(1))})
	if _, err := pager.NextPage(ctx); err == nil {
		caps.Enumerate = true
	}
	if readOnly {
		return caps
	}
	put := func(body string) error {
		_, err := cc.NewBlockBlobClient(sentinelKey).Upload(ctx,
			streaming.NopCloser(bytes.NewReader([]byte(body))), nil)
		return err
	}
	if put("ranke capability probe 1") == nil {
		caps.Overwrite = put("ranke capability probe 2") == nil
		_, err := cc.NewBlobClient(sentinelKey).Delete(ctx, nil)
		caps.Delete = err == nil
	}
	return caps
}

func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	body, err := s.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("%w: read body %s: %w", errIO, key, err)
	}
	return data, nil
}

// Open implements storage.Streamer: a retry reader over the download response, so
// content streams and a dropped connection resumes mid-blob rather than failing.
func (s *store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := s.container.NewBlobClient(key).DownloadStream(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil, ranke.ErrNotFound
		}
		return nil, fmt.Errorf("%w: download %s: %w", errIO, key, err)
	}
	return resp.NewRetryReader(ctx, nil), nil
}

// Put stores key when absent, via a conditional write (If-None-Match: "*"): keys
// are content-addressed, so a 412 means identical bytes are there — a dedup hit.
func (s *store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.container.NewBlockBlobClient(key).Upload(ctx,
		streaming.NopCloser(bytes.NewReader(data)),
		&blockblob.UploadOptions{AccessConditions: &blob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{
				IfNoneMatch: to.Ptr(azcore.ETagAny),
			},
		}})
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobAlreadyExists, bloberror.ConditionNotMet) {
			return nil // already present — content-addressed, identical bytes
		}
		return fmt.Errorf("%w: upload %s: %w", errIO, key, err)
	}
	return nil
}

// Delete removes the blob; a blob that was not there reports success, which is the
// idempotence the port asks for.
func (s *store) Delete(ctx context.Context, key string) error {
	_, err := s.container.NewBlobClient(key).Delete(ctx, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.BlobNotFound) {
		return fmt.Errorf("%w: delete %s: %w", errIO, key, err)
	}
	return nil
}

func (s *store) Has(ctx context.Context, key string) (bool, error) {
	_, err := s.container.NewBlobClient(key).GetProperties(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("%w: properties %s: %w", errIO, key, err)
	}
	return true, nil
}

func (s *store) Close() error { return nil }

// Capabilities returns the container's capabilities as probed at New (see probeCaps).
func (s *store) Capabilities() ranke.Capabilities {
	return s.caps
}
