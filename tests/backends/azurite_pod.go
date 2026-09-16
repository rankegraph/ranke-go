// package: tests/backends / integration
// type:    tool
// job:     obtain an Azure Blob store for the matrix — one named by RANKE_AZURE_ENDPOINT
// (services/azurite.sh, a CI service container), else an ephemeral Azurite podman pod
// limits:  needs RANKE_AZURE_ENDPOINT or podman, else ErrUnavailable; tears down only the blob
// container it created (-> minio_pod for the S3 half)
package backends

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

const (
	azuriteImage = "mcr.microsoft.com/azure-storage/azurite:latest"
	// The emulator's account and key, which the Azure docs publish and every Azurite
	// instance serves; nothing here is a secret.
	azuriteAccount = "devstoreaccount1"
	azuriteKey     = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
	azuriteReady   = 30 * time.Second
	// Both match services/azurite.sh: the base name each open builds on, and where
	// --native expects a service.
	azureContainer      = "ranke-perf"
	azureNativeEndpoint = "http://127.0.0.1:10000/devstoreaccount1"
)

// azureSeq numbers the containers one process creates; with the pid it makes each name unique.
var azureSeq atomic.Int64

// azureConn yields an Azure Blob store, each caller getting a blob container of its own.
// A named endpoint that does not serve is an error, not ErrUnavailable: asking for a
// store and not getting one is a failure.
func azureConn() (*azblob.Client, string, func(), error) {
	endpoint := envOr("RANKE_AZURE_ENDPOINT", "")
	if endpoint == "" && forceNativeServices {
		endpoint = azureNativeEndpoint
	}
	if endpoint == "" {
		return azuritePod()
	}
	client, err := azureClient(endpoint)
	if err != nil {
		return nil, "", nil, err
	}
	name, cleanup, err := freshContainer(client)
	if err != nil {
		return nil, "", nil, fmt.Errorf("azure blob at %s: %w", endpoint, err)
	}
	return client, name, cleanup, nil
}

// azureClient addresses the service URL with a shared key, which the emulator and a
// storage account both accept.
func azureClient(endpoint string) (*azblob.Client, error) {
	cred, err := azblob.NewSharedKeyCredential(
		envOr("RANKE_AZURE_ACCOUNT", azuriteAccount), envOr("RANKE_AZURE_KEY", azuriteKey))
	if err != nil {
		return nil, fmt.Errorf("azure credential: %w", err)
	}
	return azblob.NewClientWithSharedKeyCredential(endpoint, cred, nil)
}

// freshContainer creates an empty blob container, retrying until the deadline so a
// booting service reads as slow rather than broken. One container per open is what keeps
// concurrent runs against a shared service off each other's blobs.
func freshContainer(client *azblob.Client) (string, func(), error) {
	name := fmt.Sprintf("%s-%d-%d", envOr("RANKE_AZURE_CONTAINER", azureContainer), os.Getpid(), azureSeq.Add(1))
	deadline := time.Now().Add(azuriteReady)
	for {
		_, err := client.CreateContainer(context.Background(), name, nil)
		if err == nil {
			return name, func() { dropContainer(client, name) }, nil
		}
		if time.Now().After(deadline) {
			return "", nil, fmt.Errorf("create container %s within %s: %w", name, azuriteReady, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// dropContainer removes the container, and with it every blob inside.
func dropContainer(client *azblob.Client, name string) {
	_, _ = client.DeleteContainer(context.Background(), name, nil)
}

// azuritePod runs Azurite's blob service in a podman pod, ErrUnavailable without podman —
// the matrix reports the row unavailable rather than falling back to an in-process fake.
func azuritePod() (*azblob.Client, string, func(), error) {
	if _, err := exec.LookPath("podman"); err != nil {
		return nil, "", nil, fmt.Errorf("%w: podman not on PATH (azure needs a real blob-service pod, or set RANKE_AZURE_ENDPOINT)", ErrUnavailable)
	}

	port, err := freePort()
	if err != nil {
		return nil, "", nil, err
	}
	pod := fmt.Sprintf("ranke-perf-azurite-%d", port)

	runCtx, cancel := context.WithTimeout(context.Background(), azuriteReady)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "podman", "run", "-d", "--rm",
		"--name", pod,
		"-p", fmt.Sprintf("127.0.0.1:%d:10000", port),
		azuriteImage, "azurite-blob", "--blobHost", "0.0.0.0", "--blobPort", "10000",
	).CombinedOutput()
	if err != nil {
		return nil, "", nil, fmt.Errorf("podman run azurite: %v: %s", err, strings.TrimSpace(string(out)))
	}
	cleanup := func() { removePod(pod) }

	client, err := azureClient(fmt.Sprintf("http://127.0.0.1:%d/%s", port, envOr("RANKE_AZURE_ACCOUNT", azuriteAccount)))
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	// freshContainer retries until the service answers, so it is the readiness gate too.
	name, _, err := freshContainer(client)
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	// The pod goes with the container, so dropping the container separately is wasted work.
	return client, name, cleanup, nil
}
