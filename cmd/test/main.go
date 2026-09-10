// package: cmd/test / cli
// type:    cmd
// job:     `test` — a cobra CLI for running the project's customizable test tooling; today one
// subcommand, `performance`, drives the backend matrix
// limits:  a thin flag→Config→RunMatrix adapter; the matrix logic lives in tests/performance
// (shared with `go test`)
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rankegraph/ranke-go/tests/performance"
)

var errBadSize = errors.New("--size: not a claim count")

// parseCount reads a target claim count, accepting a k (thousand) or m (million)
// suffix and a decimal mantissa: "5000", "1k", "1.5m". Case-insensitive.
func parseCount(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	mult := 1.0
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k':
			mult, s = 1e3, s[:n-1]
		case 'm':
			mult, s = 1e6, s[:n-1]
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 1 {
		return 0, fmt.Errorf("%w: %q", errBadSize, s)
	}
	return int(f * mult), nil
}

func main() {
	root := &cobra.Command{
		Use:   "test",
		Short: "Ranke-Graph test tooling",
		Long:  "test runs the project's customizable test tooling.",
	}
	root.AddCommand(performanceCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func performanceCmd() *cobra.Command {
	var (
		sizeStr  string
		seed     int64
		access   int
		backends []string
		native   bool
		step     string
		report   bool
	)
	cmd := &cobra.Command{
		Use:   "performance",
		Short: "Run the storage-backend performance matrix",
		Long: "Generate a deterministic size-N archive into each storage backend and\n" +
			"time the chapters (write / verify / random access), reporting per-step\n" +
			"latency distributions.\n\n" +
			"Backends — durable byte-stores standalone; neo4j is a graph cache, only\n" +
			"stacked over a durable tier:\n" +
			"  mem, fs, sqlite, s3(MinIO), redis, neo4j/mem, neo4j/redis/s3\n\n" +
			"A backend that can't run here (no podman, no RANKE_NEO4J_*/RANKE_REDIS_*/\n" +
			"RANKE_S3_ENDPOINT) is skipped, not failed.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			size, err := parseCount(sizeStr)
			if err != nil {
				return err
			}
			cfg := performance.Config{
				Size:     size,
				Seed:     seed,
				Access:   access,
				Backends: backends,
				Progress: true, // interactive CLI: show the in-place progress line
				Native:   native,
				Step:     step,
				Report:   report,
			}
			return performance.RunMatrix(cfg, cmd.OutOrStdout(), nil)
		},
	}
	f := cmd.Flags()
	f.StringVar(&sizeStr, "size", "1k", "target claim count; accepts k/m suffixes (e.g. 1k, 1.5m)")
	f.Int64Var(&seed, "seed", 1, "generator seed (fixes every id)")
	f.IntVar(&access, "access", 50, "chapter-3 random accesses")
	f.StringSliceVar(&backends, "backends", nil, "backends to run, comma-separated (mem,fs,sqlite,s3,redis,neo4j/mem,neo4j/redis/s3); default all")
	f.BoolVar(&native, "native", false, "use the host-native neo4j/redis/s3 on localhost (flushed at start, left in place after) instead of spawning podman pods")
	f.StringVar(&step, "step", "", "run only this step (e.g. 2-verify, 3.1, 4.5; 4 = all queries); setup and tag always run")
	f.BoolVar(&report, "report", false, "print each query's full execution report in the queries section")
	return cmd
}
