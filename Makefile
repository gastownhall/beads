# Makefile for beads project

.PHONY: all build test bench bench-quick clean install help

# Default target
all: build

BINARY := bd
BUILD_DIR := .
INSTALL_DIR := $(HOME)/.local/bin

# Dolt backend requires CGO for embedded database support.
# Without CGO, builds will fail with "dolt backend requires CGO".
export CGO_ENABLED := 1

# ICU4C is keg-only on macOS (Homebrew doesn't symlink it into /opt/homebrew).
# Dolt's go-icu-regex dependency needs these paths to compile and link.
ifeq ($(shell uname),Darwin)
ICU_PREFIX := $(shell brew --prefix icu4c 2>/dev/null)
ifneq ($(ICU_PREFIX),)
export CGO_CFLAGS   += -I$(ICU_PREFIX)/include
export CGO_CPPFLAGS += -I$(ICU_PREFIX)/include
export CGO_LDFLAGS  += -L$(ICU_PREFIX)/lib
endif
endif

# Build the bd binary
build:
	@echo "Building bd..."
	go build -ldflags="-X main.Build=$$(git rev-parse --short HEAD)" -o $(BUILD_DIR)/$(BINARY) ./cmd/bd
ifeq ($(shell uname),Darwin)
	@codesign -s - -f $(BUILD_DIR)/$(BINARY) 2>/dev/null || true
	@echo "Signed $(BINARY) for macOS"
endif

# Run all tests (skips known broken tests listed in .test-skip)
test:
	@echo "Running tests..."
	@TEST_COVER=1 ./scripts/test.sh

# Run performance benchmarks (10K and 20K issue databases with automatic CPU profiling)
# Generates CPU profile: internal/storage/sqlite/bench-cpu-<timestamp>.prof
# View flamegraph: go tool pprof -http=:8080 <profile-file>
bench:
	@echo "Running performance benchmarks..."
	@echo "This will generate 10K and 20K issue databases and profile all operations."
	@echo "CPU profiles will be saved to internal/storage/sqlite/"
	@echo ""
	go test -bench=. -benchtime=1s -tags=bench -run=^$$ ./internal/storage/sqlite/ -timeout=30m
	@echo ""
	@echo "Benchmark complete. Profile files saved in internal/storage/sqlite/"
	@echo "View flamegraph: cd internal/storage/sqlite && go tool pprof -http=:8080 bench-cpu-*.prof"

# Run quick benchmarks (shorter benchtime for faster feedback)
bench-quick:
	@echo "Running quick performance benchmarks..."
	go test -bench=. -benchtime=100ms -tags=bench -run=^$$ ./internal/storage/sqlite/ -timeout=15m

# Install bd to ~/.local/bin (builds, signs on macOS, and copies)
# Also creates 'beads' symlink as an alias for bd
install: build
	@mkdir -p $(INSTALL_DIR)
	@rm -f $(INSTALL_DIR)/$(BINARY)
	@cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY)"
	@rm -f $(INSTALL_DIR)/beads
	@ln -s $(BINARY) $(INSTALL_DIR)/beads
	@echo "Created 'beads' alias -> $(BINARY)"

# Clean build artifacts and benchmark profiles
clean:
	@echo "Cleaning..."
	rm -f bd
	rm -f internal/storage/sqlite/bench-cpu-*.prof
	rm -f beads-perf-*.prof

# Show help
help:
	@echo "Beads Makefile targets:"
	@echo "  make build        - Build the bd binary"
	@echo "  make test         - Run all tests"
	@echo "  make bench        - Run performance benchmarks (generates CPU profiles)"
	@echo "  make bench-quick  - Run quick benchmarks (shorter benchtime)"
	@echo "  make install      - Install bd to ~/.local/bin (with codesign on macOS, includes 'beads' alias)"
	@echo "  make clean        - Remove build artifacts and profile files"
	@echo "  make help         - Show this help message"
