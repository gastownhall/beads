# Testing Guide

## Overview

The beads project has a comprehensive test suite with **~41,000 lines of code** across **205 files** in `cmd/bd` alone.

## Test Performance

- **Total test time:** ~3 minutes (excluding broken tests)
- **Package count:** 20+ packages with tests
- **Compilation overhead:** ~180 seconds (most of the total time)
- **Individual test time:** Only ~3.8 seconds combined for all 313 tests in cmd/bd

## Running Tests

### Quick Start

```bash
# Run all tests (auto-skips known broken tests)
make test

# Or directly:
./scripts/test.sh

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
   - Consistent with CI/CD

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

The test script is designed to work seamlessly with CI/CD:

```yaml
# Example GitHub Actions
- name: Run tests
  run: make test
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
