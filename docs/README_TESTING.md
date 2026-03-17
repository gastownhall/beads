# Testing Strategy

> **Testing Philosophy**: For guidance on what to test, anti-patterns to avoid, and target metrics, see [TESTING_PHILOSOPHY.md](TESTING_PHILOSOPHY.md).

This repo no longer fits a simple "unit vs integration" split. The practical
lanes are:

## Default Lanes

### Short Local / PR-Like Lane
- Best choice for frequent local iteration
- Closest match to the default PR CI path
- Uses `.test-skip` plus `-short`
- Skips the heavy runtime-matrix cases

```bash
make test-short
# or
./scripts/test.sh -short
```

### Broader Local Lane
- Uses `.test-skip`
- Does **not** pass `-short`
- Enables coverage in the `make` target
- Includes the heavier runtime-manager regression surface

```bash
make test
# or
./scripts/test.sh
```

### Full CGO Lane
- Use when changing Dolt-sensitive code or validating the full CGO path
- Not the same as PR CI's default short run
- On macOS, use the wrapper instead of raw `go test` so ICU flags are set

```bash
make test-full-cgo
# or
./scripts/test-cgo.sh ./...
```

### Embedded-Dolt Lane
- Separate tagged lane
- Used for the in-process Dolt engine
- Runs separately from the default PR short path

```bash
BEADS_TEST_EMBEDDED_DOLT=1 go test -tags embeddeddolt ./internal/storage/embeddeddolt/...
BEADS_TEST_EMBEDDED_DOLT=1 go test -tags embeddeddolt -run TestEmbedded ./cmd/bd/...
```

### Opt-In `integration` Build-Tag Lane
- Still exists for explicitly tagged scenarios
- Not the only "slow" lane anymore

```bash
go test -tags=integration ./...
```

## CI Strategy

**PR Checks**
- `go test -short -race ./...`
- Coverage on Linux
- Separate embedded-Dolt job

So the closest local reproduction of default PR CI is `make test-short`, not
`make test`.

## Runtime-Manager Regression Coverage

The runtime-manager rewrite added a black-box CLI matrix in
`cmd/bd/cli_runtime_matrix_integration_test.go`.

Key facts:
- CGO-only
- `testing.Short()`-gated
- builds a real `bd` binary inside the test
- launches real Dolt server processes

That means the matrix mostly affects non-short local runs, not the default PR
CI path.

## Dolt Test Prerequisites

There are now two common Dolt-backed test styles:
- Docker-backed tests that look for the cached Dolt image
- Host-CLI tests that launch `dolt sql-server` directly

`BEADS_TEST_SKIP=dolt` skips both styles. Host-CLI tests also skip cleanly if
`dolt` is not on `PATH`.

## Adding New Tests

### For the short path
- Keep tests compatible with `go test -short ./...` when possible
- This is the path contributors and CI hit most often

### For slower or heavier tests
- Use `testing.Short()` guards for cases that launch real processes, do heavy
  repo setup, or otherwise do not belong in the fast loop
- Use build tags when the lane is genuinely separate, such as `integration` or
  `embeddeddolt`

### For tagged integration tests
Add build tags at the top of the file:

```go
//go:build integration
// +build integration

package yourpackage_test
```

Mark slow operations with `testing.Short()` check:

```go
func TestSomethingSlow(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }
    // ... slow test code
}
```

## Local Development

During development, prefer the short lane:

```bash
make test-short
```

When you need broader coverage, step up deliberately:

```bash
make test
make test-full-cgo
```

## Performance Optimization

### In-Memory Filesystems for Git Tests

Git-heavy integration tests use `testutil.TempDirInMemory()` to reduce I/O overhead:

```go
import "github.com/steveyegge/beads/internal/testutil"

func TestWithGitOps(t *testing.T) {
    tmpDir := testutil.TempDirInMemory(t)
    // ... test code using tmpDir
}
```

**Platform behavior:**
- **Linux**: Uses `/dev/shm` (tmpfs ramdisk) if available - provides 20-30% speedup
- **macOS**: Uses standard `/tmp` (APFS is already fast)
- **Windows**: Uses standard temp directory

**For CI (GitHub Actions):**
Linux runners automatically have `/dev/shm` available, so no configuration needed.

## Performance Notes

- The short lane should stay comfortable for frequent local use.
- The broader local lane is allowed to be heavier because it includes the
  non-short runtime-manager coverage and coverage instrumentation.
- CGO compile/link time is often a larger cost than individual test execution.
