# Multi-Repo Hydration Layer

This document describes the implementation of Task 3 from the multi-repo support feature (bd-307): the hydration layer that loads issues from multiple JSONL files into a unified SQLite database.

## Overview

The hydration layer enables beads to aggregate issues from multiple repositories into a single database for unified querying and analysis. It uses file modification time (mtime) caching to optimize performance by only reimporting files that have changed.

## Architecture

### 1. Database Schema

**Table: `repo_mtimes`**
```sql
CREATE TABLE repo_mtimes (
    repo_path TEXT PRIMARY KEY,      -- Absolute path to repository root
    jsonl_path TEXT NOT NULL,        -- Absolute path to .beads/issues.jsonl
    mtime_ns INTEGER NOT NULL,       -- Modification time in nanoseconds
    last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

This table tracks the last known modification time of each repository's JSONL file to enable intelligent skip logic during hydration.

### 2. Configuration

Multi-repo mode is configured via `internal/config/config.go`:

```yaml
# .beads/config.yaml
repos:
  primary: /path/to/primary/repo  # Canonical source (optional)
  additional:                      # Additional repos to hydrate from
    - ~/projects/repo1
    - ~/projects/repo2
```

- **Primary repo** (`.`): Issues from this repo are marked with `source_repo = "."`
- **Additional repos**: Issues marked with their relative path as `source_repo`

### 3. Implementation Files

**New Files:**
- `internal/storage/sqlite/multirepo.go` - Core hydration logic
- `internal/storage/sqlite/multirepo_test.go` - Test coverage
- `docs/MULTI_REPO_HYDRATION.md` - This document

**Modified Files:**
- `internal/storage/sqlite/schema.go` - Added `repo_mtimes` table
- `internal/storage/sqlite/migrations/013_repo_mtimes_table.go` - Migration for `repo_mtimes` table
- `internal/storage/sqlite/sqlite.go` - Integrated hydration into storage initialization
- `internal/storage/sqlite/ready.go` - Added `source_repo` to all SELECT queries
- `internal/storage/sqlite/labels.go` - Added `source_repo` to SELECT query
- `internal/storage/sqlite/migrations_test.go` - Added migration tests

## Key Functions

### `HydrateFromMultiRepo(ctx context.Context) (map[string]int, error)`

Main entry point for multi-repo hydration. Called automatically during `sqlite.New()`.

**Behavior:**
- Returns `nil, nil` if not in multi-repo mode (single-repo operation)
- Processes primary repo first (if configured)
- Then processes each additional repo
- Returns a map of `source_repo -> issue count` for imported issues

### `hydrateFromRepo(ctx, repoPath, sourceRepo string) (int, error)`

Handles hydration for a single repository.

**Steps:**
1. Resolves absolute path to repo and JSONL file
2. Checks file existence (skips if missing)
3. Compares current mtime with cached mtime
4. Skips import if mtime unchanged (optimization)
5. Imports issues if file changed or no cache exists
6. Updates mtime cache after successful import

### `importJSONLFile(ctx, jsonlPath, sourceRepo string) (int, error)`

Parses a JSONL file and imports all issues into the database.

**Features:**
- Handles large files (10MB max line size)
- Skips empty lines and comments (`#`)
- Sets `source_repo` field on all imported issues
- Computes `content_hash` if missing
- Uses transactions for atomicity
- Imports dependencies, labels, and comments

### `upsertIssueInTx(ctx, tx, issue *types.Issue) error`

Inserts or updates an issue within a transaction.

**Smart Update Logic:**
- Checks if issue exists by ID
- If new: inserts issue
- If exists: compares `content_hash` and only updates if changed
- Imports associated dependencies, labels, and comments
- Uses `INSERT OR IGNORE` for dependencies/labels to avoid duplicates

### `expandTilde(path string) (string, error)`

Utility function to expand `~` and `~/` paths to absolute home directory paths.

## Mtime Caching

The hydration layer uses file modification time (mtime) as a cache key to avoid unnecessary reimports.

**Cache Logic:**
1. First hydration: No cache exists → import file
2. Subsequent hydrations: Compare mtimes
   - If `mtime_current == mtime_cached` → skip import (fast path)
   - If `mtime_current != mtime_cached` → reimport (file changed)
3. After successful import: Update cache with new mtime

**Benefits:**
- **Performance**: Avoids parsing/importing unchanged JSONL files
- **Correctness**: Detects external changes via filesystem metadata
- **Simplicity**: No need for content hashing or git integration

**Limitations:**
- Relies on filesystem mtime accuracy
- Won't detect changes if mtime is manually reset
- Cross-platform mtime precision varies (nanosecond on Unix, ~100ns on Windows)

## Source Repo Tracking

Each issue has a `source_repo` field that identifies which repository it came from:

- **Primary repo**: `source_repo = "."`
- **Additional repos**: `source_repo = <relative_path>` (e.g., `~/projects/repo1`)

This enables:
- Filtering issues by source repository
- Understanding issue provenance in multi-repo setups
- Future features like repo-specific permissions or workflows

**Database Schema:**
```sql
ALTER TABLE issues ADD COLUMN source_repo TEXT DEFAULT '.';
CREATE INDEX idx_issues_source_repo ON issues(source_repo);
```

## Testing

Comprehensive test coverage in `internal/storage/sqlite/multirepo_test.go`:

### Test Cases

1. **`TestExpandTilde`**
   - Verifies tilde expansion for various path formats

2. **`TestHydrateFromMultiRepo/single-repo_mode_returns_nil`**
   - Confirms nil return when not in multi-repo mode

3. **`TestHydrateFromMultiRepo/hydrates_from_primary_repo`**
   - Validates primary repo import
   - Checks `source_repo = "."` is set correctly

4. **`TestHydrateFromMultiRepo/uses_mtime_caching_to_skip_unchanged_files`**
   - First hydration: imports 1 issue
   - Second hydration: imports 0 issues (cached)
   - Proves mtime cache optimization works

5. **`TestHydrateFromMultiRepo/imports_additional_repos`**
   - Creates primary + additional repo
   - Verifies both are imported
   - Checks source_repo fields are distinct

6. **`TestImportJSONLFile/imports_issues_with_dependencies_and_labels`**
   - Tests JSONL parsing with complex data
   - Validates dependencies and labels are imported
   - Confirms relational data integrity

7. **`TestMigrateRepoMtimesTable`**
   - Verifies migration creates table correctly
   - Confirms migration is idempotent

### Running Tests

```bash
# Run all multirepo tests
go test -v ./internal/storage/sqlite -run TestHydrateFromMultiRepo

# Run specific test
go test -v ./internal/storage/sqlite -run TestExpandTilde

# Run all sqlite tests
go test ./internal/storage/sqlite
```

## Integration

### Automatic Hydration

Hydration happens automatically during storage initialization:

```go
// internal/storage/sqlite/sqlite.go
func New(path string) (*SQLiteStorage, error) {
    // ... schema initialization ...
    
    storage := &SQLiteStorage{db: db, dbPath: absPath}
    
    // Skip for in-memory databases (used in tests)
    if path != ":memory:" {
        _, err := storage.HydrateFromMultiRepo(ctx)
        if err != nil {
            return nil, fmt.Errorf("failed to hydrate from multi-repo: %w", err)
        }
    }
    
    return storage, nil
}
```

### Configuration Example

**`.beads/config.yaml`:**
```yaml
repos:
  primary: /Users/alice/work/main-project
  additional:
    - ~/work/library-a
    - ~/work/library-b
    - /opt/shared/common-issues
```

**Resulting database:**
- Issues from `/Users/alice/work/main-project` → `source_repo = "."`
- Issues from `~/work/library-a` → `source_repo = "~/work/library-a"`
- Issues from `~/work/library-b` → `source_repo = "~/work/library-b"`
- Issues from `/opt/shared/common-issues` → `source_repo = "/opt/shared/common-issues"`

## Migration

The `repo_mtimes` table is created via standard migration system:

```go
// internal/storage/sqlite/migrations/013_repo_mtimes_table.go
func MigrateRepoMtimesTable(db *sql.DB) error {
    // Check if table exists
    var tableName string
    err := db.QueryRow(`
        SELECT name FROM sqlite_master
        WHERE type='table' AND name='repo_mtimes'
    `).Scan(&tableName)
    
    if err == sql.ErrNoRows {
        // Create table + index
        _, err := db.Exec(`
            CREATE TABLE repo_mtimes (...);
            CREATE INDEX idx_repo_mtimes_checked ON repo_mtimes(last_checked);
        `)
        return err
    }
    
    return nil // Already exists
}
```

**Migration is idempotent**: Safe to run multiple times, won't error on existing table.

## Future Enhancements

1. **Incremental Sync**: Instead of full reimport, use git hashes or checksums to sync only changed issues
2. **Conflict Resolution**: Handle cases where same issue ID exists in multiple repos with different content
3. **Selective Hydration**: Allow users to specify which repos to hydrate (CLI flag or config)
4. **Background Refresh**: Periodically check for JSONL changes without blocking CLI operations
5. **Repository Metadata**: Track repo URL, branch, last commit hash for better provenance

## Performance Considerations

**Mtime Cache Hit (fast path):**
- 1 SQL query per repo (check cached mtime)
- No file I/O if mtime matches
- **Typical latency**: <1ms per repo

**Mtime Cache Miss (import path):**
- 1 SQL query (check cache)
- 1 file read (parse JSONL)
- N SQL inserts/updates (where N = issue count)
- 1 SQL update (cache mtime)
- **Typical latency**: 10-100ms for 100 issues

**Optimization Tips:**
- Place frequently-changing repos in primary position
- Use `.beads/config.yaml` instead of env vars (faster viper access)
- Limit `additional` repos to ~10 for reasonable startup time

## Troubleshooting

**Hydration not working?**
1. Check config: `bd config list` should show `repos.primary` or `repos.additional`
2. Verify JSONL exists: `ls -la /path/to/repo/.beads/issues.jsonl`
3. Check logs: Set `BD_DEBUG=1` to see hydration debug output

**Issues not updating?**
- Mtime cache might be stale
- Force refresh by deleting cache: `DELETE FROM repo_mtimes WHERE repo_path = '/path/to/repo'`
- Or touch the JSONL file: `touch /path/to/repo/.beads/issues.jsonl`

**Performance issues?**
- Check repo count: `SELECT COUNT(*) FROM repo_mtimes`
- Measure hydration time with `BD_DEBUG=1`
- Consider reducing `additional` repos if startup is slow

## See Also

- [CONFIG.md](CONFIG.md) - Configuration system documentation
- [EXTENDING.md](EXTENDING.md) - Database schema extension guide
- [bd-307](https://github.com/steveyegge/beads/issues/307) - Original multi-repo feature request
