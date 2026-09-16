package backends

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/stretchr/testify/require"
)

// TestAzure validates the azure test infrastructure end to end against the service
// named by RANKE_AZURE_ENDPOINT (services/azurite.sh, or a CI service container): the
// blob container handed out is fresh and empty, two opens never share one, and cleanup
// removes it with its blobs — a container left per open is what a shared service would
// accumulate. The pod path needs podman and is not covered here.
func TestAzure(t *testing.T) {
	if os.Getenv("RANKE_AZURE_ENDPOINT") == "" {
		t.Skip("no azure endpoint (services/azurite.sh native up, then set RANKE_AZURE_ENDPOINT)")
	}
	ctx := context.Background()
	client, name, cleanup, err := azureConn()
	require.NoError(t, err)
	require.NotEmpty(t, name)

	cc := client.ServiceClient().NewContainerClient(name)
	page, err := cc.NewListBlobsFlatPager(nil).NextPage(ctx)
	require.NoError(t, err)
	require.Empty(t, page.Segment.BlobItems, "a fresh container carries no blobs")

	// A second open against the same service gets its own container: one run wiping
	// another's blobs is what that prevents.
	_, other, otherCleanup, err := azureConn()
	require.NoError(t, err)
	require.NotEqual(t, name, other)
	otherCleanup()

	// An occupied container is the case that matters: the cleanup removes it with
	// whatever a run left inside.
	_, err = cc.NewBlockBlobClient("claim").Upload(ctx,
		streaming.NopCloser(bytes.NewReader([]byte("x"))), nil)
	require.NoError(t, err)

	cleanup()
	_, err = cc.GetProperties(ctx, nil)
	require.Error(t, err, "cleanup must remove the container it created")
}
