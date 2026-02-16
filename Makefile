# Makefile for beads project

.PHONY: all build test test-full-cgo bench bench-quick clean install help check-up-to-date fmt fmt-check

# Default target
all: build

BUILD_DIR := .
INSTALL_DIR := $(HOME)/.local/bin

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

# ICU4C is keg-only in Homebrew (not symlinked into the prefix).
# Dolt's go-icu-regex dependency needs these paths to compile and link.
# This handles both macOS (brew --prefix icu4c) and Linux/Linuxbrew.
# On Windows, ICU is not needed (pure-Go regex via gms_pure_go + regex_windows.go).
ifneq ($(OS),Windows_NT)
ICU_PREFIX := $(shell brew --prefix icu4c 2>/dev/null)
ifneq ($(ICU_PREFIX),)
export CGO_CFLAGS   += -I$(ICU_PREFIX)/include
export CGO_CPPFLAGS += -I$(ICU_PREFIX)/include
export CGO_LDFLAGS  += -L$(ICU_PREFIX)/lib
# Linuxbrew gcc doesn't install a 'c++' symlink; point CGO at g++
ifeq ($(shell uname),Linux)
export CXX ?= g++
endif
endif
endif

# Build the bd binary
build:
	@echo "Building bd..."
ifeq ($(OS),Windows_NT)
	go build -tags gms_pure_go -ldflags="-X main.Build=$$(git rev-parse --short HEAD)" -o $(BUILD_DIR)/bd.exe ./cmd/bd
else
	go build -ldflags="-X main.Build=$$(git rev-parse --short HEAD)" -o $(BUILD_DIR)/bd ./cmd/bd
ifeq ($(shell uname),Darwin)
	@codesign -s - -f $(BUILD_DIR)/bd 2>/dev/null || true
	@echo "Signed bd for macOS"
endif
endif

# Run all tests (skips known broken tests listed in .test-skip)
test:
	@echo "Running tests..."
	@TEST_COVER=1 ./scripts/test.sh

# Run full CGO-enabled test suite (no skip list).
# On macOS, auto-configures ICU include/link flags.
test-full-cgo:
	@echo "Running full CGO-enabled tests..."
	@./scripts/test-cgo.sh ./...

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
	@git fetch origin main --quiet 2>/dev/null || true
	@LOCAL=$$(git rev-parse HEAD 2>/dev/null); \
	REMOTE=$$(git rev-parse origin/main 2>/dev/null); \
	if [ -n "$$REMOTE" ] && [ "$$LOCAL" != "$$REMOTE" ]; then \
		echo "ERROR: Local branch is not up to date with origin/main"; \
		echo "  Local:  $$(git rev-parse --short HEAD)"; \
		echo "  Remote: $$(git rev-parse --short origin/main)"; \
		echo "Run 'git pull' first, or use SKIP_UPDATE_CHECK=1 to override"; \
		exit 1; \
	fi
endif

# Install bd to ~/.local/bin (builds, signs on macOS, and copies)
# Also creates 'beads' symlink as an alias for bd
install: check-up-to-date build
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
	@echo "  make test-full-cgo - Run full CGO-enabled test suite"
	@echo "  make bench        - Run performance benchmarks (generates CPU profiles)"
	@echo "  make bench-quick  - Run quick benchmarks (shorter benchtime)"
	@echo "  make install      - Install bd to ~/.local/bin (with codesign on macOS, includes 'beads' alias)"
	@echo "  make fmt          - Format all Go files with gofmt"
	@echo "  make fmt-check    - Check Go formatting (for CI)"
	@echo "  make clean        - Remove build artifacts and profile files"
	@echo "  make help         - Show this help message"
