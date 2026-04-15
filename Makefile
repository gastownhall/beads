# Makefile for beads project

# On Windows, GNU Make defaults to cmd.exe which doesn't support POSIX
# shell syntax used throughout this Makefile. Use Git for Windows' bash.
ifeq ($(OS),Windows_NT)
GIT_BASH := $(shell where git 2>nul)
ifneq ($(GIT_BASH),)
SHELL := $(subst cmd,bin,$(subst git.exe,bash.exe,$(GIT_BASH)))
endif
endif

.PHONY: all build test test-icu-path test-full-cgo test-regression test-upgrade test-cross-version test-migration bench bench-quick clean install install-force help check-up-to-date fmt fmt-check

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

# Run differential regression tests (baseline v0.49.6 vs current worktree).
# Downloads baseline binary on first run; cached in ~/Library/Caches/beads-regression/.
# Override baseline: BD_REGRESSION_BASELINE_BIN=/path/to/bd make test-regression
test-regression:
	@echo "Running regression tests (baseline vs candidate)..."
	go test -tags=regression -timeout=10m -v ./tests/regression/...

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


# Run performance benchmarks against Dolt storage backend
# Requires CGO and Dolt; generates CPU profile files
# View flamegraph: go tool pprof -http=:8080 <profile-file>
bench:
	@echo "Running performance benchmarks (Dolt backend)..."
	@echo ""
	go test -bench=. -benchtime=1s -benchmem -run=^$$ ./internal/storage/dolt/ -timeout=30m
	@echo ""
	@echo "Benchmark complete."

# Run quick benchmarks (shorter benchtime for faster feedback)
bench-quick:
	@echo "Running quick performance benchmarks..."
	go test -bench=. -benchtime=100ms -benchmem -run=^$$ ./internal/storage/dolt/ -timeout=15m

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
	@rm -f $(INSTALL_DIR)/bd.exe
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
check-docs: build
	@./scripts/check-doc-flags.sh ./bd

# Clean build artifacts and benchmark profiles
clean:
	@echo "Cleaning..."
	rm -f bd
	rm -f bd.exe
	rm -f internal/storage/dolt/bench-cpu-*.prof
	rm -f beads-perf-*.prof

# Show help
help:
	@echo "Beads Makefile targets:"
	@echo "  make build        - Build the bd binary"
	@echo "  make test         - Run all tests"
	@echo "  make test-icu-path - Run opt-in ICU regex path tests (maintainer-only)"
	@echo "  make test-full-cgo - Deprecated alias for make test-icu-path"
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
	@echo "  make help         - Show this help message"
