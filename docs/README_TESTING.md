# Testing Strategy

This project uses a two-tier testing approach to balance speed and thoroughness.

> **Testing Philosophy**: For guidance on what to test, anti-patterns to avoid, and target metrics, see [TESTING_PHILOSOPHY.md](TESTING_PHILOSOPHY.md).

## Test Categories

### Fast Tests (Unit Tests)
- Run on every commit and PR
- Complete in ~2 seconds
- No build tags required
- Located throughout the codebase

```bash
go test -short ./...
```

### Integration Tests  
- Marked with `//go:build integration` tag
- Include slow git operations and multi-clone scenarios
- Run nightly and before releases
- Located in:
  - `beads_hash_multiclone_test.go` - Multi-clone convergence tests (~13s)
  - `beads_integration_test.go` - End-to-end scenarios
  - `beads_multidb_test.go` - Multi-database tests

```bash
go test -tags=integration ./...
```

## CI Strategy

**PR Checks** (fast, runs on every PR):
```bash
go test -short -race ./...
```

**Nightly** (comprehensive, runs overnight):
```bash
go test -tags=integration -race ./...
```

## Adding New Tests

### For Fast Tests
No special setup required. Just write the test normally.

### For Integration Tests
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

During development, run fast tests frequently:
```bash
go test -short ./...
```

Before committing, run full suite:
```bash
go test -tags=integration ./...
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

## Performance Targets

- **Fast tests**: < 3 seconds total
- **Integration tests**: < 15 seconds total
- **Full suite**: < 18 seconds total
