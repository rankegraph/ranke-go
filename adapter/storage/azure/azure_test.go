package azure_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage/adaptertest"
	"github.com/rankegraph/ranke-go/adapter/storage/azure"
)

// endpointEnv names the blob service the live tests here run against: Azurite from
// services/azurite.sh, a CI service container, or a storage account. Unset, only the
// tests needing no service run; the matrix's "azure" row is the other place the
// adapter meets a live service (-> tests/backends).
const endpointEnv = "RANKE_AZURE_ENDPOINT"

// TestNewValidates covers what New answers before it dials anything: a missing client
// or an unnamed container is the caller's mistake, and saying so costs no round trip.
func TestNewValidates(t *testing.T) {
	if _, err := azure.New(nil, "ranke"); err == nil {
		t.Fatal("New with a nil client: expected an error")
	}
	client, err := azblob.NewClientWithNoCredential("http://127.0.0.1:10000/devstoreaccount1", nil)
	if err != nil {
		t.Fatalf("azblob.NewClientWithNoCredential: %v", err)
	}
	if _, err := azure.New(client, ""); err == nil {
		t.Fatal("New with an empty container: expected an error")
	}
}

// TestConformance runs the shared black-box Universe suite against the azure adapter,
// each universe in a blob container of its own on the service named by endpointEnv.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) ranke.Universe {
		client, name := liveContainer(t)
		u, err := azure.New(client, name)
		if err != nil {
			t.Fatalf("azure.New: %v", err)
		}
		return u
	})
}

// TestReadOnly verifies the ReadOnly option: no write probe is attempted, and the
// Universe reports no overwrite/delete (durability stays intrinsic).
func TestReadOnly(t *testing.T) {
	client, name := liveContainer(t)
	u, err := azure.New(client, name, azure.ReadOnly())
	if err != nil {
		t.Fatalf("azure.New: %v", err)
	}
	if c := u.Capabilities(); c.Overwrite || c.Delete || !c.Persistent {
		t.Fatalf("read-only azure caps = %+v; want no overwrite/delete, persistent", c)
	}
}

// containerSeq numbers the containers one process creates, so two universes alive at
// once never share one.
var containerSeq atomic.Int64

// liveContainer returns a client for the service named by endpointEnv and a fresh,
// empty container on it, removed at test end. The emulator's account and key are the
// defaults, so Azurite needs the endpoint alone.
func liveContainer(t *testing.T) (*azblob.Client, string) {
	t.Helper()
	endpoint := os.Getenv(endpointEnv)
	if endpoint == "" {
		t.Skipf("no blob service (services/azurite.sh native up, then set %s)", endpointEnv)
	}
	cred, err := azblob.NewSharedKeyCredential(
		envOr("RANKE_AZURE_ACCOUNT", "devstoreaccount1"),
		envOr("RANKE_AZURE_KEY", "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="))
	if err != nil {
		t.Fatalf("azblob.NewSharedKeyCredential: %v", err)
	}
	client, err := azblob.NewClientWithSharedKeyCredential(endpoint, cred, nil)
	if err != nil {
		t.Fatalf("azblob.NewClientWithSharedKeyCredential: %v", err)
	}
	name := fmt.Sprintf("ranke-adapter-%d-%d", os.Getpid(), containerSeq.Add(1))
	if _, err := client.CreateContainer(context.Background(), name, nil); err != nil {
		t.Fatalf("CreateContainer %s: %v", name, err)
	}
	t.Cleanup(func() { _, _ = client.DeleteContainer(context.Background(), name, nil) })
	return client, name
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
