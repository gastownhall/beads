# Makefile for beads project

# On Windows, GNU Make defaults to cmd.exe which doesn't support POSIX
# shell syntax used throughout this Makefile. Use Git for Windows' bash.
ifeq ($(OS),Windows_NT)
GIT_BASH := $(shell where git 2>/dev/null)
ifneq ($(GIT_BASH),)
SHELL := $(subst cmd,bin,$(subst git.exe,bash.exe,$(GIT_BASH)))
endif
endif

.PHONY: all build doctor-build test test-icu-path test-full-cgo test-regression test-upgrade test-cross-version test-migration corpus-regen bench bench-quick clean clean-test-tmp install install-force help check-up-to-date fmt fmt-check check-testing-short
.PHONY: ci-pr-core ci-pr-policy ci-pr-lint ci-package-mcp ci-package-npm ci-website

# Default target
all: build

BUILD_DIR := .
GIT_BUILD := $(shell git rev-parse --short HEAD)
ifeq ($(OS),Windows_NT)
INSTALL_DIR := $(USERPROFILE)/.local/bin
else
INSTALL_DIR := $(HOME)/.local/bin
endif

# Dolt backend requires CGO for embedded database support.
# Without CGO, builds will fail with "dolt backend requires CGO".
#
# Windows notes:
#   - ICU is NOT required. go-icu-regex has a pure-Go fallback (regex_windows.go)
#     and gms_pure_go tag tells go-mysql-server to use pure-Go regex too.
#   - CGO_ENABLED=1 needs a C compiler (MinGW/MSYS2) but does NOT need ICU.
export CGO_ENABLED := 1

# When go.mod requires a newer Go version than the locally installed one,
# GOTOOLCHAIN=auto downloads the right compiler but coverage instrumentation
# may still use the local toolchain's compile tool, causing version mismatch.
# Force the go.mod version to ensure all tools match.
GO_VERSION := $(shell sed -n 's/^go //p' go.mod)
ifneq ($(GO_VERSION),)
export GOTOOLCHAIN := go$(GO_VERSION)
endif

# gms_pure_go tells go-mysql-server to use Go's stdlib regex instead of
# ICU-backed go-icu-regex.  This eliminates the ICU shared-library runtime
# dependency, making release binaries portable across Linux distros.
# ICU flags are only needed for scripts/test-icu-path.sh (which exercises the
# opt-in ICU regex path).
BUILD_TAGS := gms_pure_go
REGRESSION_TIMEOUT ?= 20m

# Build the bd binary
build:
	@echo "Building bd..."
ifeq ($(OS),Windows_NT)
	go build -tags "$(BUILD_TAGS)" -ldflags="-X main.Build=$(GIT_BUILD)" -o $(BUILD_DIR)/bd.exe ./cmd/bd
else
	go build -tags "$(BUILD_TAGS)" -ldflags="-X main.Build=$(GIT_BUILD)" -o $(BUILD_DIR)/bd ./cmd/bd
ifeq ($(shell uname),Darwin)
	@codesign -s - -f $(BUILD_DIR)/bd 2>/dev/null || true
	@echo "Signed bd for macOS"
endif
endif

# Diagnose the local build environment for the gms_pure_go/CGO build trap
# (mybd-t7mk.1). A bare `CGO_ENABLED=1 go build ./cmd/bd` without
# -tags=gms_pure_go fails with a C-linker error from go-icu-regex
# (unicode/uregex.h: No such file or directory) because go-mysql-server
# links ICU by default under cgo; CGO_ENABLED=0 avoids that but can't open
# embedded Dolt at runtime. See docs/ICU-POLICY.md.
doctor-build:
	@echo "Build environment diagnostic (doctor-build):"
	@GOFLAGS_VAL="$$(go env GOFLAGS)"; \
	CGO_VAL="$$(go env CGO_ENABLED)"; \
	CC_VAL="$$(go env CC)"; \
	echo "  GOFLAGS:     $${GOFLAGS_VAL:-<empty>}"; \
	echo "  CGO_ENABLED: $$CGO_VAL"; \
	echo "  CC:          $$CC_VAL"; \
	if command -v "$$CC_VAL" >/dev/null 2>&1; then \
		echo "  CC on PATH:  yes ($$(command -v "$$CC_VAL"))"; \
	else \
		echo "  CC on PATH:  NO - $$CC_VAL not found"; \
	fi; \
	echo ""; \
	EFFECTIVE_TAGS="$$(printf '%s\n' "$$GOFLAGS_VAL" | tr ' ' '\n' | sed -n 's/^-tags=//p' | tail -n 1)"; \
	case ",$$EFFECTIVE_TAGS," in \
		*,gms_pure_go,*) \
			echo "PASS: GOFLAGS carries -tags=gms_pure_go; bare 'go build'/'go test' are safe." ;; \
		*) \
			echo "WARN: GOFLAGS is missing -tags=gms_pure_go."; \
			echo "      A bare 'CGO_ENABLED=1 go build ./cmd/bd' will fail with a C-linker error"; \
			echo "      (unicode/uregex.h: No such file or directory) from go-icu-regex, because"; \
			echo "      go-mysql-server links ICU by default under cgo."; \
			echo ""; \
			echo "      Remedy - persist the tag so bare go commands pick it up:"; \
			echo "        go env -w GOFLAGS=-tags=gms_pure_go"; \
			echo "      Or build explicitly this once (CGO_ENABLED=1 is required for"; \
			echo "      embedded Dolt at runtime; CGO_ENABLED=0 cannot open it):"; \
			echo "        CGO_ENABLED=1 go build -tags gms_pure_go ./cmd/bd" ;; \
	esac

# Run all tests (skips known broken tests listed in .test-skip)
test:
	@echo "Running tests..."
	@TEST_COVER=1 ./scripts/test.sh

# Run the opt-in ICU regex path test suite (no skip list).
# This is a local developer workflow for intentionally exercising the leftover
# ICU path; it is not part of normal validation.
test-icu-path:
	@echo "Running opt-in ICU regex path tests..."
	@./scripts/test-icu-path.sh ./...

# Deprecated compatibility alias. Keep forwarding so old local notes still work,
# but make the opt-in ICU nature explicit.
test-full-cgo:
	@echo "WARNING: make test-full-cgo is deprecated; use make test-icu-path for the explicit ICU-only path." >&2
	@$(MAKE) test-icu-path

ci-pr-core:
	@./scripts/ci/pr-core.sh

ci-pr-policy:
	@./scripts/ci/pr-policy.sh

ci-pr-lint:
	@./scripts/ci/pr-lint.sh

ci-package-mcp:
	@./scripts/ci/package-mcp.sh

ci-package-npm:
	@./scripts/ci/package-npm.sh

ci-website:
	@./scripts/ci/website.sh

# Run differential regression tests (baseline v0.49.6 vs current worktree).
# Downloads baseline binary on first run; cached in ~/Library/Caches/beads-regression/.
# Override baseline: BD_REGRESSION_BASELINE_BIN=/path/to/bd make test-regression
test-regression:
	@echo "Running regression tests (baseline vs candidate)..."
	go test -tags=regression,$(BUILD_TAGS) -timeout=$(REGRESSION_TIMEOUT) -v ./tests/regression/...

# Run upgrade smoke tests (release stability gate).
# Tests that upgrading from previous release preserves data, role, and mode.
# Override version: ./scripts/upgrade-smoke-test.sh v0.62.0
test-upgrade: build
	@echo "Running upgrade smoke tests..."
	@CANDIDATE_BIN=./bd ./scripts/upgrade-smoke-test.sh


# Run cross-version smoke tests (last 30 tags → candidate).
# Creates epic, issues, and dependencies with old versions, upgrades, verifies.
# Specific versions: ./scripts/cross-version-smoke-test.sh v0.55.0 v0.56.1
# All from v0.30.0: ./scripts/cross-version-smoke-test.sh --from v0.30.0
test-cross-version: build
	@echo "Running cross-version smoke tests..."
	@CANDIDATE_BIN=./bd ./scripts/cross-version-smoke-test.sh

# Run migration test harness (rich dataset, fidelity checks, recipe discovery).
# Tests direct and stepping-stone upgrade paths from all storage eras.
# Direct only: ./scripts/migration-test/run.sh --direct-only
# Single version: ./scripts/migration-test/run.sh v0.49.6
test-migration: build
	@echo "Running migration test harness..."
	@CANDIDATE_BIN=./bd ./scripts/migration-test/run.sh

# Regenerate the golden-JSON contract corpus (cmd/bd/protocol/testdata/corpus/).
# Run after any deliberate bd --json wire change; review the diff, then commit.
# A downstream consumer vendors this corpus to detect cross-version drift. Needs Docker (Dolt).
corpus-regen:
	@echo "Regenerating contract corpus..."
	go test -tags "$(BUILD_TAGS)" ./cmd/bd/protocol -run TestCorpusGolden -corpus.update -count=1


# Run performance benchmarks against Dolt storage backend
# Requires CGO and Dolt; generates CPU profile files
# View flamegraph: go tool pprof -http=:8080 <profile-file>
bench:
	@echo "Running performance benchmarks (Dolt backend)..."
	@echo ""
	go test -tags "$(BUILD_TAGS)" -bench=. -benchtime=1s -benchmem -run=^$$ ./internal/storage/dolt/ -timeout=30m
	@echo ""
	@echo "Benchmark complete."

# Run quick benchmarks (shorter benchtime for faster feedback)
bench-quick:
	@echo "Running quick performance benchmarks..."
	go test -tags "$(BUILD_TAGS)" -bench=. -benchtime=100ms -benchmem -run=^$$ ./internal/storage/dolt/ -timeout=15m

# Check that local branch is up to date with origin/main
check-up-to-date:
ifndef SKIP_UPDATE_CHECK
	@# Skip check on detached HEAD (tag checkouts, CI builds)
	@if ! git symbolic-ref HEAD >/dev/null 2>&1; then exit 0; fi
	@git fetch origin main --quiet 2>/dev/null || true
	@LOCAL=$$(git rev-parse HEAD 2>/dev/null); \
	REMOTE=$$(git rev-parse origin/main 2>/dev/null); \
	if [ -n "$$REMOTE" ] && [ "$$LOCAL" != "$$REMOTE" ]; then \
		echo "ERROR: Local branch is not up to date with origin/main"; \
		echo "  Local:  $$(git rev-parse --short HEAD)"; \
		echo "  Remote: $$(git rev-parse --short origin/main)"; \
		echo "Run 'git pull' first, or use 'make install-force' to override"; \
		exit 1; \
	fi
endif

# Install bd to ~/.local/bin (builds, signs on macOS, and copies)
# Also creates 'beads' symlink as an alias for bd
# Use install-force to skip the origin/main update check
install install-force: build
	@mkdir -p $(INSTALL_DIR)
ifeq ($(OS),Windows_NT)
	@rm -f $(INSTALL_DIR)/bd $(INSTALL_DIR)/bd.exe
	@cp $(BUILD_DIR)/bd.exe $(INSTALL_DIR)/bd.exe
	@echo "Installed bd.exe to $(INSTALL_DIR)/bd.exe"
else
	@rm -f $(INSTALL_DIR)/bd
	@cp $(BUILD_DIR)/bd $(INSTALL_DIR)/bd
	@echo "Installed bd to $(INSTALL_DIR)/bd"
	@rm -f $(INSTALL_DIR)/beads
	@ln -s bd $(INSTALL_DIR)/beads
	@echo "Created 'beads' alias -> bd"
endif
	@git config core.hooksPath .githooks 2>/dev/null && echo "Configured git hooks (.githooks/)" || true

install: check-up-to-date

# Format all Go files
fmt:
	@echo "Formatting Go files..."
	@gofmt -w .
	@echo "Done"

# Check that all Go files are properly formatted (for CI)
fmt-check:
	@echo "Checking Go formatting..."
	@UNFORMATTED=$$(gofmt -l .); \
	if [ -n "$$UNFORMATTED" ]; then \
		echo "The following files are not properly formatted:"; \
		echo "$$UNFORMATTED"; \
		echo ""; \
		echo "Run 'make fmt' to fix formatting"; \
		exit 1; \
	fi
	@echo "All Go files are properly formatted"

# Validate documentation references against actual CLI flags
check-docs:
	@echo "Building bd for docs checks..."
	@CGO_ENABLED=0 go build -tags "$(BUILD_TAGS)" -ldflags="-X main.Build=$(GIT_BUILD)" -o $(BUILD_DIR)/bd ./cmd/bd
	@./scripts/check-doc-flags.sh ./bd
	@./scripts/check-doc-freshness.sh

# Ensure -short is not used as an implicit CI tier boundary.
check-testing-short:
	@./scripts/check-testing-short.sh

# Clean build artifacts and benchmark profiles
clean:
	@echo "Cleaning..."
	rm -f bd
	rm -f bd.exe
	rm -f internal/storage/dolt/bench-cpu-*.prof
	rm -f beads-perf-*.prof

# Sweep orphaned cmd/bd test temp dirs (e.g. when a test run was SIGKILLed
# before its TestMain cleanup ran). Safe to run between test runs; will
# skip dirs in use by a live test process. See bd-3q2u.
clean-test-tmp:
	@echo "Sweeping orphaned cmd/bd test temp dirs from $${TMPDIR:-/tmp}..."
	@./scripts/clean-test-tmp.sh

# Show help
help:
	@echo "Beads Makefile targets:"
	@echo "  make build        - Build the bd binary"
	@echo "  make doctor-build - Diagnose build env (GOFLAGS/CGO/CC) for the ICU build trap"
	@echo "  make test         - Run all tests"
	@echo "  make test-icu-path - Run opt-in ICU regex path tests (maintainer-only)"
	@echo "  make test-full-cgo - Deprecated alias for make test-icu-path"
	@echo "  make ci-pr-core  - Run required PR core Go test wrapper"
	@echo "  make ci-pr-policy - Run required PR policy wrapper"
	@echo "  make ci-pr-lint  - Run required PR formatting and lint wrapper"
	@echo "  make ci-package-mcp - Run MCP Python package gate"
	@echo "  make ci-package-npm - Run npm package gate"
	@echo "  make ci-website - Run website typecheck/build gate"
	@echo "  make test-regression - Run differential regression tests (baseline vs candidate)"
	@echo "  make test-upgrade  - Run upgrade smoke tests (release stability gate)"
	@echo "  make test-cross-version - Run cross-version smoke tests (last 30 tags)"
	@echo "  make test-migration - Run migration test harness (fidelity checks, recipes)"
	@echo "  make bench        - Run performance benchmarks (generates CPU profiles)"
	@echo "  make bench-quick  - Run quick benchmarks (shorter benchtime)"
	@echo "  make install      - Install bd to ~/.local/bin (with codesign on macOS, includes 'beads' alias)"
	@echo "  make install-force - Install bd, skipping the origin/main update check"
	@echo "  make fmt          - Format all Go files with gofmt"
	@echo "  make fmt-check    - Check Go formatting (for CI)"
	@echo "  make check-docs   - Validate docs against CLI flags"
	@echo "  make clean        - Remove build artifacts and profile files"
	@echo "  make clean-test-tmp - Sweep orphaned cmd/bd test temp dirs from \$$TMPDIR"
	@echo "  make help         - Show this help message"
