# Testing Guide

## Overview

The test surface now has a few distinct lanes. The important thing is to pick the
right one for the change you are making instead of assuming every path is equally
cheap.

The short version:
- `make test-short` is the closest local match to PR CI. It uses `.test-skip`
  and passes `-short`.
- `make test` is the broader local run. It still uses `.test-skip`, but it does
  **not** pass `-short`. The `make` target also enables coverage.
- `make test-full-cgo` is the full CGO-enabled lane for Dolt-sensitive changes.

Compilation and link time still dominate many runs. The newer runtime-manager
coverage also means the non-short local path is meaningfully heavier than the
PR path.

## Running Tests

### Quick Start

```bash
# Fast local path (closest to PR CI)
make test-short

# Or directly:
./scripts/test.sh -short

# Broader local path (non-short; `make test` also enables coverage)
make test

# Or directly:
./scripts/test.sh

# Run full CGO-enabled suite (no skip list)
make test-full-cgo

# Run embedded-Dolt tagged lanes explicitly
BEADS_TEST_EMBEDDED_DOLT=1 go test -tags embeddeddolt ./internal/storage/embeddeddolt/...
BEADS_TEST_EMBEDDED_DOLT=1 go test -tags embeddeddolt -run TestEmbedded ./cmd/bd/...

# Run specific package
./scripts/test.sh ./cmd/bd/...

# Run specific test pattern
./scripts/test.sh -run TestCreate ./cmd/bd/...

# Verbose output
./scripts/test.sh -v
```

### Environment Variables

```bash
# Set custom timeout (default: 3m)
TEST_TIMEOUT=5m ./scripts/test.sh

# Force short mode via env instead of CLI flag
TEST_SHORT=1 ./scripts/test.sh

# Enable verbose output
TEST_VERBOSE=1 ./scripts/test.sh

# Run specific pattern
TEST_RUN=TestCreate ./scripts/test.sh
```

### Dolt-Backed Test Dependencies

The repo uses two different Dolt-backed test styles:
- Docker-backed Dolt tests that look for the exact cached test image.
- Host-CLI Dolt tests that launch `dolt sql-server` directly from your local
  `PATH`.

The Docker-backed tests auto-detect the environment and skip gracefully.

#### Readiness states

```csv
State,Condition,Behavior
doltSkipped,BEADS_TEST_SKIP contains "dolt",Silent skip (no warning)
doltNoDocker,Docker daemon not reachable,WARN + skip
doltNoImage,No Dolt image at all,WARN + skip with pull instruction
doltWrongVersion,Image repo cached but wrong tag,WARN + skip with pull instruction
doltReady,Exact image cached and Docker running,Run tests
```

States are checked once per test binary and cached. Order of evaluation:
`BEADS_TEST_SKIP` → Docker availability → exact image → any image version.

#### Skipping Dolt tests explicitly

Set `BEADS_TEST_SKIP` to opt out without Docker overhead (~1s `docker info`):

```bash
# Skip Dolt tests silently
BEADS_TEST_SKIP=dolt ./scripts/test.sh

# Skip multiple services (comma-separated)
BEADS_TEST_SKIP=dolt,slow ./scripts/test.sh
```

That same `BEADS_TEST_SKIP=dolt` contract now also covers the newer
runtime-manager host-`dolt` tests. Some older host-CLI tests still only guard
on local `dolt` availability. If a host-CLI test needs the local `dolt` binary
and it is not on `PATH`, it usually skips instead of failing hard.

#### Enabling Dolt tests

```bash
# Pull the exact Dolt image to enable integration tests
docker pull dolthub/dolt-sql-server:1.43.0

# Or make the local dolt CLI available for host-backed runtime tests
which dolt

# Point tests at an existing Dolt server (skips container startup)
BEADS_DOLT_PORT=3308 ./scripts/test.sh
```

`BEADS_DOLT_PORT` — when set, tests reuse the server at that port instead of
starting a container. Port 3307 is hardcoded as production and always rejected.

### Advanced Usage

```bash
# Skip additional tests beyond .test-skip
./scripts/test.sh -skip SomeSlowTest

# Run the wrapper in short mode
./scripts/test.sh -short

# Run with custom timeout
./scripts/test.sh -timeout 5m

# Combine flags
./scripts/test.sh -short -v -run TestCreate ./internal/beads/...
```

## Known Broken Tests

Tests in `.test-skip` are automatically skipped. Current broken tests:

1. **TestFallbackToDirectModeEnablesFlush** (GH #355)
   - Location: `cmd/bd/direct_mode_test.go:14`
   - Issue: Database deadlock, hangs for 5 minutes
   - Impact: Makes test suite extremely slow

## For Claude Code / AI Agents

When running tests during development:

### Best Practices

1. **Use the test script:** Always use `./scripts/test.sh` instead of `go test` directly
   - Automatically skips known broken tests
   - Uses appropriate timeouts
   - Exposes both the short local loop and the broader non-short path
   - For full CGO validation, use `./scripts/test-cgo.sh` (or `make test-full-cgo`)
   - For PR-like local coverage, prefer `./scripts/test.sh -short` (or `make test-short`)

2. **Target specific tests when possible:**
   ```bash
   # Fast local check:
   ./scripts/test.sh -short

   # Broader local check:
   ./scripts/test.sh

   # Run just what you changed:
   ./scripts/test.sh -short -run TestSpecificFeature ./cmd/bd/...
   ```

3. **Compilation is the bottleneck:**
   - CGO compile/link time still dominates many runs
   - `make test` is heavier than PR CI because it is non-short and enables coverage
   - The runtime-matrix tests are `testing.Short()`-gated, so they mostly show up in non-short local runs
   - Use `-short` and `-run` to keep the loop tight when you can

4. **Check for new failures:**
   ```bash
   # If you see a new failure, check if it's known:
   cat .test-skip
   ```

### Adding Tests to Skip List

If you discover a broken test:

1. File a GitHub issue documenting the problem
2. Add to `.test-skip`:
   ```bash
   # Issue #NNN: Brief description
   TestNameToSkip
   ```
3. Tests in `.test-skip` support regex patterns

## Runtime Matrix and Embedded-Dolt Lanes

The runtime-manager rewrite added a black-box CLI matrix in
`cmd/bd/cli_runtime_matrix_integration_test.go`.

Important properties:
- CGO-only
- `testing.Short()`-gated
- launches real `bd` binaries and real Dolt server processes

That means:
- PR CI mostly skips the heavy runtime-matrix cases because it runs `-short`
- local `make test` includes this lane when the package is exercised in non-short mode
- local `make test-short` skips it

There is also a separate embedded-Dolt lane in CI:
- `go test -tags embeddeddolt -v -race -count=1 ./internal/storage/embeddeddolt/`
- `go test -tags embeddeddolt -v -race -count=1 -run TestEmbedded ./cmd/bd/`

### Package Structure

```
cmd/bd/           - Main CLI tests (82 test files, most of the suite)
internal/beads/   - Core beads library tests
internal/storage/ - Storage backend tests (SQLite, memory)
internal/rpc/     - RPC protocol tests
internal/*/       - Various internal package tests
```

## Continuous Integration

PR CI does **not** run `make test`. It runs `go test -short -race ./...` (with
coverage on Linux) plus a separate embedded-Dolt tagged job.

So if you want the closest local reproduction of the default PR lane, use:

```bash
make test-short
```

## Debugging Test Failures

### Get detailed output
```bash
./scripts/test.sh -v ./path/to/package/...
```

### Run a single test
```bash
./scripts/test.sh -run '^TestExactName$' ./cmd/bd/...
```

### Check which tests are being skipped
```bash
./scripts/test.sh -short 2>&1 | head -5
```

Output shows:
```
Running: go test -timeout 3m -skip TestFoo|TestBar ./...
Skipping: TestFoo|TestBar
```

## Contributing

When adding new tests:

1. Keep tests fast (<0.1s if possible)
2. Use `t.Parallel()` for independent tests
3. Clean up resources in `t.Cleanup()` or `defer`
4. Avoid sleeps unless testing concurrency

When tests break:

1. Fix them if possible
2. If unfixable right now, file an issue and add to `.test-skip`
3. Document the issue in `.test-skip` with issue number
