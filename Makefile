# Makefile — ranke-go
#
# `make build` compiles the repo's binaries into bin/: the ranke CLI
# (cmd/ranke), the ranke-test harness (cmd/test), and the scenariodoc
# generator (cmd/scenariodoc).

.PHONY: all help build install uninstall check/full test test/full full-intended test/core test/core/coverage test/vectors test/integration test/matrix test/concurrency test/performance test-verbose coverage coverage-gaps vet fmt fmt-check tidy upgrade lint tools rule-citations rql-schema verify check clean scenarios verify-scenarios update-references scenarios-docs verify-docs conformance-bundle docs docs-current docs-clean check-clean-tree check-release-bump release major minor patch breaking feature fix

# ask = prompt before raising the go directive (`make upgrade`); keep = leave it; or a version.
GO_VERSION ?= ask

# "The library" for coverage purposes = the root package plus the mem
# storage adapter. mem is the fundamental, always-present, dependency-free
# Universe — root behaviour can only be exercised through some adapter,
# and mem is the one that ships everywhere. Other adapters are verified
# against the Universe contract via adapter/storage/adaptertest, not counted.
MODULE  := github.com/rankegraph/ranke-go
COVERPKG := $(MODULE),$(MODULE)/adapter/storage/mem
# Test packages that drive the number — explicitly enumerated, NOT a
# ./adapter/... glob, so a new adapter is opted into the core test
# deliberately rather than swept in by accident:
#   ./tests/...                    the library's feature suite
#   ./adapter/storage/mem/...      fundamental adapter (also counted in COVERPKG)
#   ./adapter/storage/fs/...       exercises root through a second real backend
#   ./adapter/storage/sqlite/...   pure-Go SQLite backend (modernc, no cgo)
#   ./adapter/storage/s3/...       S3 backend against an in-process gofakes3 server
#   ./adapter/storage/minimal/...  smallest possible adapter (map behind BlobStore)
#   ./adapter/storage/rest/...     HTTP blob backend against an in-process server
#   ./adapter/sequencer/...        BranchTableHead backends (mem/file/func)
# Every adapter here runs with NO special infrastructure. Adapter
# statements are NOT in COVERPKG — they're verified against their contract,
# not counted. conformance scenarios are deliberately excluded.
COVERDRIVERS := ./tests/... ./adapter/storage/mem/... ./adapter/storage/fs/... ./adapter/storage/sqlite/... ./adapter/storage/s3/... ./adapter/storage/minimal/... ./adapter/storage/rest/... ./adapter/storage/stack/... ./adapter/storage/partition/... ./adapter/sequencer/...

BINDIR ?= $(HOME)/.local/bin

# Every test target goes through GOTEST, so one override reaches all of them:
# `make test GOTEST="go test -count=1"` to defeat the cache, for instance.
# Packages run in parallel — internal/exclusive locks the shared services.
GOTEST ?= go test

# The rows the fast gate asks for: the backends that need no service, so it has
# nothing to skip. RANKE_ROWS carries the set to the Go layer
# (-> tests/backends.Requested); test/full leaves it unset, which means all of them.
FAST_ROWS ?= mem,fs,sqlite

# Foundational papers live in the ranke-graph repo. `make docs` pulls a
# fresh copy into docs/papers/ for local reference; the directory is
# gitignored and never committed — always fetched, never vendored.
RANKE_GRAPH_REPO ?= https://github.com/rankegraph/ranke-graph
RANKE_GRAPH_REF  ?= main
PAPERS_DIR       := docs/papers

# release-cycle.sh lives in ranke-graph and serves every consumer repo, so the git
# mechanics of a release (branch resolution, the merge-then-tag dance, the wait for
# CI) are written once, there. Cached under bin/ (gitignored), like brokkr below.
# scripts/release.sh here is this repo's own dispatcher: it delegates a real
# release to this, and keeps only the prerelease mode ("make release pre <bump>"),
# which release-cycle.sh's fixed sequence has no shape for.
RELEASE_CYCLER     := bin/release-cycle.sh
RELEASE_CYCLER_URL ?= https://raw.githubusercontent.com/rankegraph/ranke-graph/$(RANKE_GRAPH_REF)/scripts/release-cycle.sh

# brokkr, the linter `make lint` runs.
#
# A sindri agent pod already HAS one: the hub bind-mounts the brokkr it built into
# every pod and /usr/local/bin points there. That copy is the fleet's own — the exact
# binary every other agent runs, and newer than master — so preferring it keeps this
# gate on the same tool AND takes a network fetch off the gate's critical path. A 403
# on the installer failed two gate runs in a row here, on an identical commit.
#
# Anywhere else the installer still runs on every lint rather than caching, so the
# gate tracks the tool: a brokkr release can turn a run red on an untouched branch,
# which is the intent, as it is for the spec bundle. It compares versions and
# downloads only when they differ, so the common path is one check. bin/ is
# gitignored, so the binary is fetched infrastructure and never committed.
BROKKR_PROVIDED  := $(shell command -v brokkr 2>/dev/null)
BROKKR           := $(if $(BROKKR_PROVIDED),$(BROKKR_PROVIDED),bin/tools/brokkr)
BROKKR_INSTALLER := https://raw.githubusercontent.com/flocko-motion/sindri/master/scripts/install-brokkr.sh

SCENARIO_DIRS := $(wildcard conformance/scenarios/*)

# Default target: the static gates (build, gofmt, lint, rule citations, scenario
# bundles) plus the fast test gate. The full suite is a deliberate ask —
# `make test/full` — since it needs every service up.
all: verify test ## Default: static gates + the fast test suite

help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_/-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# Build all binaries into bin/: the ranke CLI, the ranke-test harness,
# and the scenariodoc generator.
build: ## Compile the CLI, ranke-test harness and scenariodoc into bin/
	@mkdir -p bin
	go build -o bin/ranke ./cmd/ranke
	go build -o bin/ranke-test ./cmd/test
	go build -o bin/scenariodoc ./cmd/scenariodoc

# Copy the built CLI to $(BINDIR)/ranke (default: ~/.local/bin).
# No sudo needed; just make sure $(BINDIR) is on your PATH.
install: build ## Copy the built CLI to $(BINDIR)/ranke (default ~/.local/bin)
	@mkdir -p $(BINDIR)
	install -m 0755 bin/ranke $(BINDIR)/ranke

uninstall: ## Remove the installed CLI from $(BINDIR)
	rm -f $(BINDIR)/ranke

# test/core — the datatype unit layer (root package): claims, codec,
# content, id, signing, graph, guarantees. No infrastructure, no fs, no
# adapters. Fast; the correctness of the datatype itself lives here.
test/core: ## Datatype unit layer only — fast, no infrastructure
	$(GOTEST) .

# test/core/coverage — the datatype layer with statement coverage. Prints a
# per-file breakdown (from the raw profile, statement-weighted) and the core
# total. Drill into one file's functions with:
#   go tool cover -func=coverage-core.out | grep node.go
# or open the annotated source with:
#   go tool cover -html=coverage-core.out
test/core/coverage: ## test/core with a per-file coverage breakdown
	@$(GOTEST) . -covermode=atomic -coverprofile=coverage-core.out
	@echo ""
	@echo "coverage by file:"
	@awk 'NR>1 { split($$1,a,":"); f=a[1]; sub(/.*\//,"",f); t[f]+=$$2; if ($$3>0) c[f]+=$$2 } \
		END { for (f in t) printf "  %5.1f%%  %s\n", 100*c[f]/t[f], f }' coverage-core.out | sort -k2
	@echo ""
	@printf "core "; go tool cover -func=coverage-core.out | tail -1

# test/integration — the blackbox suite in /tests: the Archive/Sequencer
# layer driven across adapters. The fs test uses a fixed directory
# (RANKE_FS_DIR, default /tmp/ranke-go-test) that TestMain wipes and
# recreates each run; the path is echoed so it's visible without `-v`.
RANKE_FS_DIR ?= /tmp/ranke-go-test
test/integration: ## Archive/Sequencer blackbox suite across adapters
	@RANKE_FS_DIR=$(RANKE_FS_DIR) $(GOTEST) ./tests/... && \
	echo "" && \
	echo "fs archive directory (preserved for inspection):" && \
	echo "  $(RANKE_FS_DIR)"

# test/matrix — the cross-backend conformance matrix: build the same
# deterministic archive into every backend the run asks for and assert each one
# answers the whole RQL corpus exactly as the mem reference does. RANKE_ROWS names
# the set (default: all of them), and the services they need come up with:
#   services/neo4j.sh native up    # the neo4j/mem row
#   services/redis.sh native up    # the redis row
#   services/s3.sh native up       # the s3 row, and the neo4j/redis/s3 stack
# Verbose so the per-row, per-query sub-tests are visible.
test/matrix: ## Cross-backend RQL conformance matrix (RANKE_ROWS to narrow; needs services up)
	$(GOTEST) ./tests/matrix/ -v -count=1

# test/concurrency/N — N writers contributing at once, over every (Sequencer,
# storage) pair the run asks for. The suite runs this at a modest count already;
# this target is for the massive one, which is where a race that hides at 64
# writers shows itself. E.g.
#   make test/concurrency/1000
# RANKE_ROWS narrows the storage half, and the services come up as for test/matrix.
# The serialised Sequencer is capped internally — its count is sequential work.
test/concurrency/%:
	@RANKE_CONCURRENCY=$* $(GOTEST) ./tests/ -run TestConcurrentContributionsLoseNothing \
		-v -count=1 -timeout 30m

# test/performance/N — the backend matrix: generate the same deterministic
# size-N archive into each storage backend and time build + verify, one row
# per backend. N is the generator size knob (SpecForSize); ~5*N claims. E.g.
#   make test/performance/2000   # a 10k+-claim archive per backend
# Verbose so the per-backend timing rows print; generous timeout for large N.
test/performance/%:
	@RANKE_PERF_SIZE=$* $(GOTEST) ./tests/performance/ -run TestPerformanceMatrix -v -count=1 -timeout 30m

# test/vectors — the spec's own conformance artifacts, fetched from the latest
# ranke-graph release. The gate that catches an encoder change: verification hashes
# stored bytes, so existing claims keep verifying while newly built ones drift, and
# nothing else here would notice. Point RANKE_TESTDATA_DIR at an extracted set to
# run it offline. Unreachable is a failure, here as in CI: not checking conformance
# is worse than a red run. The bundle is cached and revalidated, so a run that finds
# the release unchanged transfers nothing.
test/vectors: ## The published cross-implementation conformance vectors (RANKE_TESTDATA_DIR to work offline)
	go test ./tests/ -run TestPublished -v -count=1

# test — the fast gate, and one pass over every package: the datatype, the feature
# suite, every adapter, and cross-backend agreement over the rows that need no
# service. Each package runs once, so the cache works and an untouched tree re-runs
# in seconds. It asks for nothing it cannot have — no service rows, so there is
# nothing to skip and no green covering a backend that never ran.
#
# The service rows live in `make test/full`.
test: ## Fast gate: one pass over ./..., the rows needing no service (RANKE_ROWS to change)
	@RANKE_FS_DIR=$(RANKE_FS_DIR) RANKE_ROWS=$(FAST_ROWS) $(GOTEST) ./...

# test/full — every correctness row, and the target CI runs, so the gate and the
# local run are the same thing. Every backend row is REQUIRED: RANKE_ROWS is unset,
# so the matrix asks for all of them, and a row that cannot open fails the run
# rather than skipping. Also the scenario bundles with their docs, and the
# concurrency suite under -race — its writer count is sized for the race detector,
# so this is where the detector runs. The -race pass takes the service-free rows:
# a race lives in the Sequencer's head handling or an adapter's own concurrency,
# and those three carry both, for seconds instead of the minute all seven cost.
#
# The benchmark and the 10k-claim scale set are development tools: `make
# test/performance/N` and `bin/ranke-test` drive the first, RANKE_SCALE the second.
# A gate answers whether the code is correct, which neither of them asks.
#
# Needs the services up (services/{neo4j,redis,s3}.sh native up) and the spec
# fetched (make docs). The 30m timeout is per package: the matrix is minutes
# against live services, past go test's 10m default.
#
# WRITES to the tree: verify-scenarios regenerates conformance/scenarios/*/data/,
# which `make clean` owns and .gitignore covers.
# Guarded because CI runs it on every push, and it was being reached for during
# ordinary work where `make test` was the answer. CI passes the guard by being CI;
# a person passes it by saying so.
test/full: full-intended ## Full gate: every backend row, concurrency under -race, scenario docs (needs RANKE_FULL=1)
	@RANKE_FS_DIR=$(RANKE_FS_DIR) $(GOTEST) -timeout 30m ./...
	@RANKE_FS_DIR=$(RANKE_FS_DIR) RANKE_ROWS=$(FAST_ROWS) $(GOTEST) -race -timeout 30m \
		./tests/ -run TestConcurrentContributionsLoseNothing -count=1
	@$(MAKE) verify-scenarios verify-docs

# full-intended stops a slow run nobody meant to start. GITHUB_ACTIONS and CI are
# set by a runner; RANKE_FULL is a person saying they mean it.
full-intended:
	@if [ -z "$$GITHUB_ACTIONS$$CI$$RANKE_FULL" ]; then \
		echo ""; \
		echo "  make test/full takes MINUTES: every backend row, the concurrency suite"; \
		echo "  under -race, the scenario bundles and their docs."; \
		echo ""; \
		echo "  During regular work you want:   make test      (seconds)"; \
		echo "  CI runs test/full on every push, so the slow run happens anyway."; \
		echo ""; \
		echo "  Run it yourself ONLY when you touched what the fast gate leaves out —"; \
		echo "  a service-backed row, the concurrency suite, a scenario bundle."; \
		echo "  Then say so, on whichever target you meant:"; \
		echo ""; \
		echo "      RANKE_FULL=1 make $(firstword $(MAKECMDGOALS))"; \
		echo ""; \
		exit 1; \
	fi

test-verbose: ## Verbose test run over ./tests/...
	$(GOTEST) -v ./tests/...

# Merged library coverage from `go test ./...`. -coverpkg attributes
# coverage to the /tests package, which imports the library. (Packages
# without _test.go files, like conformance, are skipped by go test.)
coverage: ## Merged library coverage (writes coverage.out)
	@RANKE_FS_DIR=$(RANKE_FS_DIR) $(GOTEST) -coverpkg=$(COVERPKG) \
		-covermode=atomic -coverprofile=coverage.out $(COVERDRIVERS)
	@go tool cover -func=coverage.out | tail -1

# Map of where coverage is missing: every library function below 100%,
# worst first. Refreshes the profile via `coverage` first.
coverage-gaps: coverage ## Functions below 100% coverage, worst first
	@echo ""
	@echo "Functions below 100% coverage (worst first):"
	@go tool cover -func=coverage.out \
		| awk '$$3+0<100 && $$1 ~ /\.go:/ { print $$3"\t"$$0 }' \
		| sort -n \
		| sed 's#$(MODULE)/##'

# Narrative output: scenarios print what they are doing at every step.
# Useful for understanding what the integration suite covers.
test-debug: ## Narrative integration/provenance run, verbose
	$(GOTEST) -v -run "TestIntegration|TestProvenance" ./...

# Static checks.
vet: ## go vet ./...
	go vet ./...

fmt: ## gofmt -w . (writes)
	gofmt -w .

# Fail if any Go file is not gofmt-clean (lists the offenders). The check half
# of `fmt`; wired into `verify`. Skips .worktrees (sibling agent checkouts).
fmt-check: ## Fail if any Go file needs gofmt — the check half of fmt
	@out="$$(find . -path ./.worktrees -prune -o -name '*.go' -print | xargs gofmt -l)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt needed (run 'make fmt'):"; echo "$$out"; exit 1; \
	fi

tidy: ## go mod tidy
	go mod tidy

# $(RELEASE_CYCLER) is a file target with no prerequisite, so once cached under bin/
# it is never re-fetched on its own — a stale copy (missing a ranke-graph fix) would
# sit there forever otherwise. upgrade is the one command that already means "bring
# everything to latest", so refreshing it here is what makes that true.
upgrade: ## Upgrade all deps and brokkr to latest, tidy, then check; asks before raising the go directive (GO_VERSION=keep|1.26.5)
	@GO_VERSION=$(GO_VERSION) ./scripts/upgrade.sh
	@rm -f $(RELEASE_CYCLER)
	@$(MAKE) $(RELEASE_CYCLER)

# Cut a release: verify → rebase onto the default branch → merge via PR → tag the
# merged tip → push the tag → watch the release workflow, failing here if it fails.
# Usage: make release <major|minor|patch> (aliases: breaking|feature|fix).
#
# `make release pre <bump>` tags a candidate on the branch instead, merging nothing —
# a version the proxy resolves, so ranke-graph can regenerate the vectors before the
# real release is cut. See scripts/release.sh for why that order matters.
# check-clean-tree first, ahead of verify: a dirty tree is a free, instant check,
# and verify is not — failing on it should not cost a build first.
check-clean-tree:
	@[ -z "$$(git status --porcelain)" ] || { echo "working tree is dirty — commit or stash before releasing" >&2; exit 1; }

# Same reasoning as check-clean-tree: a missing or misspelled bump word is a free,
# instant check, and verify is not — release-cycle.sh (or scripts/release.sh, for a
# prerelease) has its own case statement that validates it too, but only after
# verify already ran.
check-release-bump:
	@[ -n "$(filter major minor patch breaking feature fix,$(MAKECMDGOALS))" ] || \
		{ echo "usage: make release [pre] <major|breaking | minor|feature | patch|fix>" >&2; exit 1; }

release: check-clean-tree check-release-bump verify $(RELEASE_CYCLER) ## Cut a release: make release <major|minor|patch> (aliases breaking|feature|fix; prefix pre for a candidate)
	@./scripts/release.sh $(filter pre major minor patch breaking feature fix,$(MAKECMDGOALS))

# Absorb the positional bump word in `make release <bump>` so it isn't treated
# as a missing target.
pre major minor patch breaking feature fix:
	@:

$(RELEASE_CYCLER): ## Cache release-cycle.sh from ranke-graph (bin/ is gitignored — infra, never vendored)
	@mkdir -p $(dir $(RELEASE_CYCLER))
	@curl -fsSL $(RELEASE_CYCLER_URL) -o $(RELEASE_CYCLER)
	@chmod +x $(RELEASE_CYCLER)

# Run every conformance scenario from a clean state.
scenarios: ## Run every conformance scenario from a clean state
	@conformance/run.sh

# Run each scenario fresh and diff the produced bundle against the committed
# reference: same claims under the same ids, same branch heads at the same heights.
# Wired into `verify`, so anything that moves an id fails here first — the ids are
# signatures, and the reference bundle is the only thing holding them to a value.
#
# B_h is one bookmark id from the archive's list (§Bookmarks) — a scenario's
# Sequencer derives its seed deterministically, so the id is fixed rather than
# timestamped and byte-diffs like every other file here.
#
# Update after an intentional change: `make update-references`. Regenerating is
# not the same as checking: the bundle is self-generated, so promote it only
# after reading what changed and confirming each scenario still verifies clean.
verify-scenarios: ## Diff each scenario's fresh run against its committed reference
	@for d in $(SCENARIO_DIRS); do \
		echo "--- verify $$d ---"; \
		(cd "$$d" && rm -rf data && go run . > /dev/null); \
		if diff -r "$$d/data_reference" "$$d/data" > /dev/null; then \
			echo "$$d: matches reference ✓"; \
		else \
			echo "$$d: DRIFT — differs from checked-in reference"; \
			exit 1; \
		fi; \
	done

# Replace each scenario's archive_reference/ + ids_reference.txt
# with its current generated outputs. Run after an intentional
# scenario change, review the diff, then commit.
update-references: ## Promote each scenario's fresh run to its committed reference (review before commit)
	@for d in $(SCENARIO_DIRS); do \
		echo "--- update $$d ---"; \
		(cd "$$d" && rm -rf data && go run . > /dev/null); \
		rm -rf "$$d/data_reference"; \
		cp -r "$$d/data" "$$d/data_reference"; \
	done
	@echo "References updated. Review with: git diff conformance/scenarios/"

clean: ## Remove bin/, dist/, and generated scenario data
	rm -rf bin/ dist/
	@for d in $(SCENARIO_DIRS); do \
		rm -rf "$$d/data"; \
	done

# Regenerate each scenario.md from comments in main.go + the
# template at conformance/helpers/scenario.md.tmpl.
scenarios-docs: ## Regenerate each scenario.md from its main.go comments
	@go run ./cmd/scenariodoc

# Verify scenario.md files are in sync with main.go comments.
# Regenerates and diffs against checked-in state — fails on drift.
verify-docs: scenarios-docs ## Fail if scenario.md is out of sync with main.go
	@git diff --exit-code conformance/scenarios/*/scenario.md \
		|| { echo "scenario.md out of sync — run 'make scenarios-docs' and commit"; exit 1; }

# Build a self-contained conformance bundle suitable for downstream
# variant implementations (Python, ...) to verify against. Output:
# dist/ranke-conformance-<VERSION>.tar.gz.
#
# VERSION defaults to the current git describe (tag if on one, else
# short SHA). Override with `make conformance-bundle VERSION=v0.2.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
conformance-bundle: verify-scenarios scenarios-docs ## Pack conformance/ into dist/ranke-conformance-<VERSION>.tar.gz
	@mkdir -p dist
	@BUNDLE=ranke-conformance-$(VERSION); \
	WORK=$$(mktemp -d); \
	mkdir -p "$$WORK/$$BUNDLE"; \
	(cd "$$WORK/$$BUNDLE" && mkdir conformance); \
	git ls-files conformance/ | tar -cf - -T - | tar -xf - -C "$$WORK/$$BUNDLE/"; \
	[ -f specification.txt ] && cp specification.txt "$$WORK/$$BUNDLE/" || true; \
	cp README.md "$$WORK/$$BUNDLE/" 2>/dev/null || true; \
	tar -C "$$WORK" -czf "dist/$$BUNDLE.tar.gz" "$$BUNDLE"; \
	rm -rf "$$WORK"; \
	echo "wrote dist/$$BUNDLE.tar.gz"

# Pull the latest ranke-graph papers into docs/papers/ for reference.
# Not committed — fetched fresh (see .gitignore).
docs: ## Pull the latest ranke-graph papers into docs/papers/ (not committed)
	@RANKE_GRAPH_REPO=$(RANKE_GRAPH_REPO) RANKE_GRAPH_REF=$(RANKE_GRAPH_REF) \
		PAPERS_DIR=$(PAPERS_DIR) ./scripts/fetch-papers.sh

# The freshness check the gates run: `git ls-remote` against the stamp
# scripts/fetch-papers.sh writes, cloning only when the ref has moved — 40 bytes on
# the common path, 1.8 MB when there is something new to read.
#
# `verify` depends on this because the papers directory is gitignored and never
# expires: a copy fetched last week reads exactly like one fetched a minute ago, so
# every gate over it was reporting green against whatever happened to be on disk.
# That is how ranke-ts came to gate on six-day-old vectors, and the release path is
# where it costs most — `release` runs `verify`, so a stale spec ships.
#
# It needs the network. RANKE_DOCS_OFFLINE=1 keeps the copy on disk instead, and
# RANKE_SPEC / RANKE_RQL_SCHEMA point the individual gates at a copy of your own.
docs-current: ## Re-pull docs/papers/ only if ranke-graph moved (what verify depends on)
	@RANKE_GRAPH_REPO=$(RANKE_GRAPH_REPO) RANKE_GRAPH_REF=$(RANKE_GRAPH_REF) \
		PAPERS_DIR=$(PAPERS_DIR) ./scripts/fetch-papers.sh --if-moved

# Remove the pulled paper references.
docs-clean: ## Remove the pulled paper references
	rm -rf $(PAPERS_DIR)

# brokkr static-analysis gate: canonical headers, exported-doc coverage,
# deadcode (with --test), and the line-count limit.
lint: tools ## Run the brokkr static-analysis gate
	$(BROKKR) lint

# Install or update brokkr at $(BROKKR), unless the environment already provides one
# (-> BROKKR_PROVIDED), in which case there is nothing to fetch and nothing that can
# fail. pipefail is what makes a failed download fail here: without it bash reads an
# empty script, exits 0, and the missing binary surfaces one target later as a puzzle.
tools: ## Install or update brokkr at bin/tools/brokkr (nothing to do where one is provided)
ifneq ($(BROKKR_PROVIDED),)
	@echo "brokkr provided by the environment: $(BROKKR_PROVIDED)"
else
	@bash -o pipefail -c 'curl -fsSL $(BROKKR_INSTALLER) | bash -s -- $(BROKKR)'
endif

# Rule-citation gate: every backticked `V-…`/`R-…` id a comment cites is one the
# spec declares, and every declared rule is either cited or listed in
# scripts/rule-citations.allow with a reason. It says nothing about whether a
# citation is TRUE — only that the ids exist and are accounted for.
#
# The spec comes from $(PAPERS_DIR), or from RANKE_SPEC when you are working
# against a copy of your own:  make rule-citations RANKE_SPEC=path/to/spec.typ
rule-citations: ## Every cited V-*/R-* rule id exists in the spec, and every declared one is accounted for
	@./scripts/rule-citations.sh

# Rule-coverage gate for the published reference vectors: every ADT (V-*) rule the
# spec declares either has a case that BREAKS it, or is listed in
# scripts/rule-vectors.allow with a reason. It generates the set and reads the
# manifest, so what is gated is the artifact downstream receives.
#
# Coverage is per RULE, not per clause — see the script's header for what that misses.
# Needs the spec, like rule-citations; RANKE_SPEC points it at a copy.
rule-vectors: ## Every declared V-* rule has a conformance case that breaks it, or is on the allowlist
	@./scripts/rule-vectors.sh

# rql-schema: the machine-readable projection of the query language against the Go
# constants that implement it. Every constraint the schema states is probed against
# DecodeQuery, and a keyword the gate cannot check FAILS rather than passing silently.
# Needs the schema from gitignored docs/papers/, which `verify` fetches through
# `docs-current` before this runs; on its own it fails on a bare checkout rather than
# passing blind. RANKE_RQL_SCHEMA points it elsewhere.
rql-schema: ## Check the Go query implementation against rql.schema.json
	@go run ./scripts/rqlgate

# The static gates: build the binaries, check formatting, run the lint gate, check
# rule citations, and reproduce the scenario bundles. Everything that reads the tree
# rather than running the suite — the tests are `test` (fast) and `test/full`
# (everything). Around 2s on top of the build.
#
# Needs the spec, and now fetches it: `docs-current` brings $(PAPERS_DIR) up to the
# remote ref before any gate reads it, so a bare clone no longer fails here and a
# stale copy no longer passes. A gate that cannot see the spec cannot check it, and
# one reading a copy of unknown age is blind in the same way — a skip would turn
# green exactly there.
#
# WRITES to the tree: verify-scenarios regenerates conformance/scenarios/*/data/,
# which `make clean` owns and .gitignore covers — no hand-written file is touched,
# but a bundle you were reading is rebuilt under you. It is here because the
# scenario references are checked nowhere a change is made: CI checks them
# (.github/workflows/ci.yml), which is a verdict arriving after the work has left
# the desk, and that is how the 0.18.0 signature framing landed against a bundle
# it had invalidated. `release` depends on this target, so an id-moving release
# now stops here rather than shipping a reference that reproduces nothing.
verify: docs-current build fmt-check lint rule-citations rule-vectors rql-schema verify-scenarios ## Static gates: docs, build, gofmt, lint, rule citations, scenario bundles

# One-shot "is everything green", and the name people reach for, so it costs what
# that name promises: the static gates, vet, and the fast suite. Seconds. The full
# suite against live services is `RANKE_FULL=1 make test/full`, which CI runs on
# every push.
check: verify vet test ## Everything green: static gates + vet + the fast suite

# check/full — check with the full suite instead of the fast one: the gate CI runs,
# and what to run by hand before a release. Guarded through test/full, so it needs
# GITHUB_ACTIONS, CI or RANKE_FULL.
check/full: verify vet test/full ## check, with the full suite instead of the fast one (needs RANKE_FULL=1)
