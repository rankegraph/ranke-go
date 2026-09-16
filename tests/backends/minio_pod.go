// package: tests/backends / integration
// type:    tool
// job:     obtain an S3 object store for the matrix — a running store via RANKE_S3_ENDPOINT
// (services/s3.sh, a CI service container), else an ephemeral MinIO podman pod — with a fresh,
// empty bucket of its own
// limits:  requires RANKE_S3_ENDPOINT or podman; returns ErrUnavailable otherwise. Never tears down
// a store it did not start, only the bucket it created.
package backends

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	// quay.io is where MinIO is published: docker.io/minio/minio answers 404, and an
	// anonymous pull of it is refused as access denied.
	minioImage = "quay.io/minio/minio:latest"
	minioUser  = "minioadmin"
	minioPass  = "minioadmin"
	minioReady = 30 * time.Second
	// s3Bucket is the base each open's bucket name is built from; s3NativeEndpoint is
	// where --native expects a store to serve. Both match services/s3.sh.
	s3Bucket         = "ranke-perf"
	s3Region         = "us-east-1"
	s3NativeEndpoint = "http://127.0.0.1:9000"
)

// s3Seq numbers the buckets one process creates; with the pid it makes each open's
// bucket name unique.
var s3Seq atomic.Int64

// s3Conn yields an S3 object store to test against. RANKE_S3_ENDPOINT points at an
// already-running store (services/s3.sh, or a CI service container) and is never torn
// down; otherwise an ephemeral MinIO pod is spawned (needs podman). Either way the
// caller gets a bucket of its own. A named endpoint that does not serve is an error,
// not ErrUnavailable: asking for a store and not getting one is a failure.
func s3Conn() (*awss3.Client, string, func(), error) {
	endpoint := os.Getenv("RANKE_S3_ENDPOINT")
	if endpoint == "" && forceNativeServices {
		endpoint = s3NativeEndpoint
	}
	if endpoint == "" {
		return minioPod()
	}
	client := s3Client(endpoint)
	bucket, cleanup, err := freshBucket(client)
	if err != nil {
		return nil, "", nil, fmt.Errorf("s3 at %s: %w", endpoint, err)
	}
	return client, bucket, cleanup, nil
}

// s3Client addresses endpoint path-style with static credentials — what MinIO and the
// other S3-compatible stores the suite runs against serve.
func s3Client(endpoint string) *awss3.Client {
	cfg := aws.Config{
		Region: envOr("RANKE_S3_REGION", s3Region),
		Credentials: credentials.NewStaticCredentialsProvider(
			envOr("RANKE_S3_KEY", minioUser), envOr("RANKE_S3_SECRET", minioPass), ""),
	}
	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

// freshBucket creates an empty bucket, waits for it to serve, and returns it with a
// cleanup that removes it and its objects. One bucket per open is what keeps two
// concurrent runs against one shared store off each other's objects — the reason no
// exclusive lock is needed here.
func freshBucket(client *awss3.Client) (string, func(), error) {
	bucket := fmt.Sprintf("%s-%d-%d", envOr("RANKE_S3_BUCKET", s3Bucket), os.Getpid(), s3Seq.Add(1))
	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		return "", nil, fmt.Errorf("create bucket %s: %w", bucket, err)
	}
	cleanup := func() { dropBucket(client, bucket) }
	// Serve-readiness gate: HeadBucket until it answers, so warm-up never bleeds
	// into the measured window.
	if err := waitServing(client, bucket); err != nil {
		cleanup()
		return "", nil, err
	}
	return bucket, cleanup, nil
}

// dropBucket empties the bucket and removes it. Listing after each batch converges
// because every listed key is deleted; a failure leaves the bucket, which costs disk
// on a shared store and nothing else.
func dropBucket(client *awss3.Client, bucket string) {
	ctx := context.Background()
	for {
		page, err := client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		if err != nil || len(page.Contents) == 0 {
			break
		}
		ids := make([]s3types.ObjectIdentifier, 0, len(page.Contents))
		for _, obj := range page.Contents {
			ids = append(ids, s3types.ObjectIdentifier{Key: obj.Key})
		}
		if _, err := client.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		}); err != nil {
			return
		}
	}
	_, _ = client.DeleteBucket(ctx, &awss3.DeleteBucketInput{Bucket: aws.String(bucket)})
}

// minioPod starts a MinIO container in a podman pod, waits for it to serve,
// creates one empty bucket, and returns an S3 client plus a cleanup func that
// removes the pod. Returns ErrUnavailable when podman is not on PATH and no
// endpoint was named, so the matrix reports the row as unavailable rather than
// falling back to an in-process fake.
func minioPod() (*awss3.Client, string, func(), error) {
	if _, err := exec.LookPath("podman"); err != nil {
		return nil, "", nil, fmt.Errorf("%w: podman not on PATH (s3 needs a real object-store pod, or set RANKE_S3_ENDPOINT)", ErrUnavailable)
	}

	port, err := freePort()
	if err != nil {
		return nil, "", nil, err
	}
	name := fmt.Sprintf("ranke-perf-minio-%d", port)

	runCtx, cancel := context.WithTimeout(context.Background(), minioReady)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "podman", "run", "-d", "--rm",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:9000", port),
		// The same credentials s3Client will present, so an overridden key still opens.
		"-e", "MINIO_ROOT_USER="+envOr("RANKE_S3_KEY", minioUser),
		"-e", "MINIO_ROOT_PASSWORD="+envOr("RANKE_S3_SECRET", minioPass),
		minioImage, "server", "/data",
	).CombinedOutput()
	if err != nil {
		return nil, "", nil, fmt.Errorf("podman run minio: %v: %s", err, strings.TrimSpace(string(out)))
	}
	cleanup := func() { removePod(name) }

	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitReady(endpoint); err != nil {
		cleanup()
		return nil, "", nil, err
	}

	client := s3Client(endpoint)
	bucket, _, err := freshBucket(client)
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	// The pod goes with the bucket, so dropping the bucket separately is wasted work.
	return client, bucket, cleanup, nil
}

// freePort grabs an ephemeral port by binding :0 and releasing it.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitReady polls MinIO's readiness endpoint until 200 or the deadline.
func waitReady(endpoint string) error {
	deadline := time.Now().Add(minioReady)
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint + "/minio/health/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("minio not ready at %s within %s", endpoint, minioReady)
}

// waitServing blocks until HeadBucket succeeds — a real S3 round-trip
// confirming the pod serves requests, not merely that it is up.
func waitServing(client *awss3.Client, bucket string) error {
	deadline := time.Now().Add(minioReady)
	for {
		_, err := client.HeadBucket(context.Background(), &awss3.HeadBucketInput{
			Bucket: aws.String(bucket),
		})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("minio bucket %s not serving within %s: %w", bucket, minioReady, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
