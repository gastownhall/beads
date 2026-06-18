# Testing Guide

## Overview

The beads project uses Go tests plus repository wrapper scripts. Prefer the
wrapper scripts for local validation because they apply the repository's normal
local build flags, skip policy, and timeout policy. The current GitHub Actions
PR contract still runs direct `go test` commands; the CI cleanup plan will move
that contract behind dedicated `scripts/ci/*` wrappers.

## Test Performance

- Go compilation dominates full-suite runtime.
- Target package/test runs are usually the fastest way to validate focused changes.
- Docker-backed Dolt integration tests auto-detect prerequisites and skip when unavailable.

## Running Tests

### Quick Start

```bash
# Run all tests (auto-skips known broken tests)
make test

# Or directly:
./scripts/test.sh

# Run opt-in ICU regex path tests (maintainer-only, not normal validation)
make test-icu-path

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

# Enable verbose output
TEST_VERBOSE=1 ./scripts/test.sh

# Run specific pattern
TEST_RUN=TestCreate ./scripts/test.sh
```

### Docker (Dolt Integration Tests)

Dolt integration tests require Docker with the exact Dolt image cached locally.
Tests auto-detect the environment and skip gracefully — no manual configuration
needed.

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

### Short Mode and Test Boundaries

`testing.Short()` is reserved for true runtime, stress, and large-fixture skips.
It must not be used as an implicit integration, e2e, API, Docker, or external
dependency boundary.

Use these mechanisms instead:

- `//go:build integration` or `//go:build e2e` for named suites.
- Environment readiness checks such as `BEADS_TEST_SKIP=dolt`,
  `BEADS_TEST_EMBEDDED_DOLT=1`, or required API-key checks.
- Named wrappers such as `make ci-pr-core`, the main integration shards, and the
  package gate wrappers.

Run `make check-testing-short` to verify that new `testing.Short()` usage stays
within the approved runtime/stress/large-fixture allowlist. The PR policy wrapper
runs the same check.

#### Enabling Dolt tests

```bash
# Pull the exact Dolt image to enable integration tests
docker pull dolthub/dolt-sql-server:2.1.0

# Point tests at an existing Dolt server (skips container startup)
BEADS_DOLT_PORT=3308 ./scripts/test.sh
```

`BEADS_DOLT_PORT` — when set, tests reuse the server at that port instead of
starting a container. Port 3307 is hardcoded as production and always rejected.

### Advanced Usage

```bash
# Skip additional tests beyond .test-skip
./scripts/test.sh -skip SomeSlowTest

# Run with custom timeout
./scripts/test.sh -timeout 5m

# Combine flags
./scripts/test.sh -v -run TestCreate ./internal/beads/...
```

## Known Broken Tests

Tests in `.test-skip` are automatically skipped by `scripts/test.sh`.

At the time of this review, `.test-skip` contains only comments and no active
test-name patterns. Treat any new skip as a temporary exception: file the
upstream issue first, record it in `.test-skip`, and remove the skip when the
test is fixed.

## For Claude Code / AI Agents

When running tests during development:

### Best Practices

1. **Use the test script:** Always use `./scripts/test.sh` instead of `go test` directly
   - Automatically skips known broken tests
   - Uses appropriate timeouts
   - Matches local default validation; use future `scripts/ci/*` wrappers when
     reproducing exact CI jobs
   - Only if intentionally exercising the ICU regex path, use `./scripts/test-icu-path.sh` (or deprecated `make test-full-cgo`)

2. **Target specific tests when possible:**
   ```bash
   # Instead of running everything:
   ./scripts/test.sh

   # Run just what you changed:
   ./scripts/test.sh -run TestSpecificFeature ./cmd/bd/...
   ```

3. **Compilation is the bottleneck:**
   - The 180-second compilation time dominates
   - Individual tests are fast
   - Use `-run` to avoid recompiling unnecessarily

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

## Test Organization

### Slowest Tests (>0.05s)

The top slow tests in cmd/bd:
- `TestDoctorWithBeadsDir` (1.68s) - Only significantly slow test
- `TestFlushManagerDebouncing` (0.21s)
- `TestDebouncer_*` tests (0.06-0.12s each) - Intentional sleeps for concurrency testing
- `TestMultiWorkspaceDeletionSync` (0.12s)

Most tests are <0.01s and very fast.

### Package Structure

```
cmd/bd/           - Main CLI tests (82 test files, most of the suite)
internal/beads/   - Core beads library tests
internal/storage/ - Storage backend tests (SQLite, memory)
internal/rpc/     - RPC protocol tests
internal/*/       - Various internal package tests
```

## Continuous Integration

The current CI workflow does not yet call the `scripts/ci/*` wrappers for every
job. Until workflow migration is complete, use the command documented in the
failing workflow when reproducing an existing status check exactly.

Use `scripts/test.sh` for local default validation and targeted development
runs.

Use the CI wrappers for the accepted target PR contracts:

```bash
make ci-pr-core
make ci-pr-policy
make ci-pr-lint
```

Use the package gate wrappers when touching package or docs surfaces:

```bash
make ci-package-mcp
make ci-package-npm
make ci-website
```

### Coverage Signal Policy

PR confidence is based on behavior checks, not raw coverage percentage.

- Treat Codecov percentages as informational trend data.
- Prefer focused tests for risky paths (storage, sync/git, migrations, state
  transitions, and corruption/integrity handling) over broad line-coverage
  churn.
- Add or extend at least one targeted regression test when fixing risky logic.
- Do not block a change solely on overall coverage movement when behavioral
  checks are strong.

For the current CI/test-surface inventory and cleanup roadmap, see
[CI_TEST_SURFACE_AUDIT.md](CI_TEST_SURFACE_AUDIT.md). That audit documents
where local commands and GitHub Actions currently diverge before the CI cleanup
work starts changing workflow behavior.

For accepted CI tier decisions and the implementation order, see
[CI_CLEANUP_PLAN.md](CI_CLEANUP_PLAN.md).

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
./scripts/test.sh 2>&1 | head -5
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
