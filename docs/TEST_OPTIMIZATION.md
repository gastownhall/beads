# Test Suite Optimization - November 2025

## Problem
Test suite was timing out after 5+ minutes, making development workflow painful.

## Root Cause
Slow integration tests were running during normal `go test ./...`:
- **Daemon tests**: 7 files with git operations and time.Sleep calls
- **Multi-clone convergence tests**: 2 tests creating multiple git repos
- **Concurrent import test**: 30-second timeout for deadlock detection

## Solution
Tagged slow integration tests with `//go:build integration` so they're excluded from normal runs:

### Files moved to integration-only:
1. `cmd/bd/daemon_test.go` (862 lines, 15 tests)
2. `cmd/bd/daemon_sync_branch_test.go` (1235 lines, 11 tests)
3. `cmd/bd/daemon_autoimport_test.go` (408 lines, 2 tests)
4. `cmd/bd/daemon_watcher_test.go` (7 tests)
5. `cmd/bd/daemon_watcher_platform_test.go`
6. `cmd/bd/daemon_lock_test.go`
7. `cmd/bd/git_sync_test.go`
8. `beads_hash_multiclone_test.go` (already tagged)
9. `internal/importer/importer_integration_test.go` (concurrent test)

### Fix for build error:
- Added `const windowsOS = "windows"` to `test_helpers_test.go` (was in daemon_test.go)

## Results

### Before:
```
$ go test ./...
> 300 seconds (timeout)
```

### After:
```
$ go test ./...
real    0m1.668s  ✅
user    0m2.075s
sys     0m1.586s
```

**99.4% faster!** From 5+ minutes to under 2 seconds.

## Running Integration Tests

### Normal development (fast):
```bash
go test ./...
```

### Full test suite including integration (slow):
```bash
go test -tags=integration ./...
```

### CI/CD:
```yaml
# Fast feedback on PRs
- run: go test ./...

# Full suite on merge to main
- run: go test -tags=integration ./...
```

## Benefits
1. ✅ Fast feedback loop for developers (<2s vs 5+ min)
2. ✅ Agents won't timeout on test runs
3. ✅ Integration tests still available when needed
4. ✅ CI can run both fast and comprehensive tests
5. ✅ No tests deleted - just separated by speed

## What Tests Remain in Fast Suite?
- All unit tests (~300+ tests)
- Quick integration tests (<100ms each)
- In-memory database tests
- Logic/validation tests
- Fast import/export tests

## Notes
- Integration tests still have `testing.Short()` checks for double safety
- The `integration` build tag is opt-in (must explicitly request with `-tags=integration`)
- All slow git/daemon operations are now integration-only
