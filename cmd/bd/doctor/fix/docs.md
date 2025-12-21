# Noridoc: cmd/bd/doctor/fix

Path: @/cmd/bd/doctor/fix

### Overview

The `cmd/bd/doctor/fix` directory contains automated remediation functions for issues detected by the `bd doctor` command. Each module handles a specific category of issues (deletions manifest, database config, sync branch, etc.) and provides functions to automatically fix problems found in beads workspaces.

### How it fits into the larger codebase

- **Integration with Doctor Detection**: The `@/cmd/bd/doctor.go` command runs checks to identify workspace problems, then calls functions from this package when `--fix` flag is used. The `CheckDatabaseJSONLSync()` function in `@/cmd/bd/doctor/database.go` (lines 299-486) detects sync issues and provides direction-specific guidance about which fix to run. When DB count differs from JSONL count, it now recommends `bd doctor --fix` to run `DBJSONLSync()` with the appropriate direction.

- **Dependency on Core Libraries**: The fix functions use core libraries like `@/internal/deletions` (for reading/writing deletion manifests), `@/internal/types` (for issue data structures), `@/internal/configfile` (for database path resolution), and git operations via `exec.Command`.

- **Data Persistence Points**: Each fix module directly modifies persistent workspace state: deletions manifest, database files, JSONL files, and git branch configuration. Changes are written to disk and persisted in the git repository. The sync fix is unique in that it delegates persistence to `bd export` or `bd sync --import-only` commands.

- **Deletion Tracking Architecture**: The deletions manifest (`@/internal/deletions/deletions.go`) is an append-only log tracking issue deletions. The fix in `deletions.go` is critical to maintaining the integrity of this log by preventing tombstones from being incorrectly re-added to it after `bd migrate-tombstones` runs.

- **Tombstone System**: The fix works in concert with the tombstone system (`@/internal/types/types.go` - `Status == StatusTombstone`). Tombstones represent soft-deleted issues that contain deletion metadata. The fix prevents tombstones from being confused with actively deleted issues during deletion hydration.

- **Database Configuration Management**: The sync fix uses `@/internal/configfile.Load()` to support both canonical and custom database paths, enabling workspaces with non-standard database locations (via `metadata.json`) to be synced correctly.

**Database-JSONL Sync** (`sync.go`):

The `DBJSONLSync()` function fixes synchronization issues between the SQLite database and JSONL export files by detecting data direction and running the appropriate sync command:

1. **Bidirectional Detection** (lines 23-97):
   - Counts issues in both database (via SQL query) and JSONL file (via line-by-line JSON parsing)
   - Determines sync direction based on issue counts:
     - If `dbCount > jsonlCount`: DB has newer data → runs `bd export` to sync JSONL
     - If `jsonlCount > dbCount`: JSONL has newer data → runs `bd sync --import-only` to import
     - If counts equal but timestamps differ: Uses file modification times to decide direction
   - This replaces the previous unidirectional approach that could leave users stuck when DB was the source of truth

2. **Database Path Resolution** (lines 32-37):
   - Uses `configfile.Load()` to check for custom database paths in `metadata.json`
   - Falls back to canonical database name (`beads.CanonicalDatabaseName`)
   - Supports both current and legacy database configurations

3. **JSONL File Discovery** (lines 39-48):
   - Checks for both canonical (`issues.jsonl`) and legacy (`beads.jsonl`) JSONL file names
   - Supports workspaces that migrated from one naming convention to another
   - Returns early if either database or JSONL is missing (nothing to sync)

4. **Helper Functions**:
   - `countDatabaseIssues()` (lines 124-139): Opens SQLite database and queries `COUNT(*) FROM issues`
   - `countJSONLIssues()` (lines 141-174): Iterates through JSONL file line-by-line, parsing JSON and counting valid issues with IDs. Skips malformed JSON lines gracefully.

5. **Command Execution** (lines 106-120):
   - Gets bd binary path safely via `getBdBinary()` to prevent fork bombs in tests
   - Executes `bd export` or `bd sync --import-only` with workspace directory as working directory
   - Streams stdout/stderr to user for visibility

**Problem Solved (bd-68e4)**:

Previously, when the database contained more issues than the JSONL export, the doctor would recommend `bd sync --import-only`, which imports JSONL into DB. Since JSONL hadn't changed and the database had newer data, this command was a no-op (0 created, 0 updated), leaving users unable to sync their JSONL file with the database. The bidirectional detection now recognizes this case and runs `bd export` instead.

### Core Implementation

**Deletions Manifest Hydration** (`deletions.go`):

1. **HydrateDeletionsManifest()** (lines 16-96):
   - Entry point called by `bd doctor --fix` when "Deletions Manifest" issue is detected
   - Compares current JSONL IDs (read from `issues.jsonl`) against historical IDs from git history
   - Finds IDs that existed in history but are missing from current JSONL (legitimate deletions)
   - Adds these missing IDs to the deletions manifest with author "bd-doctor-hydrate"
   - Skips IDs already present in the existing deletions manifest to avoid duplicates

2. **getCurrentJSONLIDs()** (lines 98-135):
   - Reads current `issues.jsonl` file line-by-line as JSON
   - Parses each line to extract ID and Status fields
   - **CRITICAL FIX (bd-in7q)**: Skips issues with `Status == "tombstone"` (lines 127-131)
   - Returns a set of "currently active" issue IDs
   - Gracefully handles missing files (returns empty set) and malformed JSON lines (skips them)
   - This is where the bd-in7q fix is implemented - tombstones are not considered "currently active" and won't be flagged as deleted

3. **getHistoricalJSONLIDs()** (lines 137-148):
   - Delegates to `getHistoricalIDsViaDiff()` to extract all IDs ever present in JSONL from git history
   - Uses git log to find all commits that modified the JSONL file

4. **getHistoricalIDsViaDiff()** (lines 178-232):
   - Walks git history commit-by-commit (memory efficient)
   - For each commit touching the JSONL file, parses JSON to extract IDs
   - Uses `looksLikeIssueID()` validation to avoid false positives from JSON containing ID-like strings
   - Returns complete set of all IDs ever present in the repo history

5. **looksLikeIssueID()** (lines 150-176):
   - Validates that a string matches the issue ID format: `prefix-suffix`
   - Prefix must be alphanumeric with underscores, suffix must be base36 hash or number with optional dots for child issues
   - Used to filter out false positives when parsing JSON

**Test Coverage** (`fix_test.go`):

The test file includes comprehensive coverage for the sync functionality:

- **TestCountJSONLIssues**: Tests the `countJSONLIssues()` helper with:
  - Empty JSONL files (returns 0)
  - Valid issues in JSONL (correct count)
  - Mixed valid and invalid JSON lines (counts only valid issues)
  - Nonexistent files (returns error)

- **TestDBJSONLSync_Validation**: Tests validation logic:
  - Returns without error when no database exists (nothing to sync)
  - Returns without error when no JSONL exists (nothing to sync)

- **TestDBJSONLSync_MissingDatabase**: Validates graceful handling when only JSONL exists

**Test Coverage** (`deletions_test.go`):

The test file covers edge cases and validates the bd-in7q fix:

- **TestGetCurrentJSONLIDs_SkipsTombstones**: Core fix validation - verifies tombstones are excluded from current IDs
- **TestGetCurrentJSONLIDs_HandlesEmptyFile**: Graceful handling of empty JSONL files
- **TestGetCurrentJSONLIDs_HandlesMissingFile**: Graceful handling when JSONL doesn't exist
- **TestGetCurrentJSONLIDs_SkipsInvalidJSON**: Malformed JSON lines are skipped without failing

### Things to Know

**The bd-in7q Bug and Fix**:

The bug occurred because `bd migrate-tombstones` converts deletion records from the legacy `deletions.jsonl` file into inline tombstone entries in `issues.jsonl`. Without the fix, the sequence would be:

1. User runs `bd migrate-tombstones` → creates tombstones in JSONL with `status: "tombstone"`
2. User runs `bd sync` → triggers `bd doctor hydrate`
3. `getCurrentJSONLIDs()` was reading ALL issues including tombstones
4. Comparison logic sees tombstones are no longer in git history commit 0 (before migration)
5. They're flagged as "deleted" and re-added to deletions manifest with author "bd-doctor-hydrate"
6. Next sync applies these deletion records, marking issues as deleted in the database
7. Result: thousands of false deletion records corrupt the manifest and database state

The fix simply filters out `Status == "tombstone"` issues in `getCurrentJSONLIDs()` (line 129). This ensures tombstones (which represent already-recorded deletions) never participate in deletion detection. They're semantically invisible to the deletion tracking system.

**Why Tombstones Exist**:

`@/internal/types/types.go` defines `StatusTombstone` as part of the system (bd-vw8). Tombstones are soft-deleted issues that retain all metadata (ID, DeletedBy, DeletedAt, DeleteReason) for audit trails and conflict resolution. They differ from entries in the deletions manifest, which are just an ID + deletion metadata without the original issue content.

**Append-Only Nature of Deletions Manifest**:

The deletions manifest (`@/internal/deletions/deletions.go`) is append-only. When a duplicate deletion is added, the last write wins (line 81 in deletions.go). This design assumes deletions are only recorded once, which the fix preserves by skipping tombstones.

**Missing File Handling**:

The `getCurrentJSONLIDs()` function returns an empty set when the JSONL file doesn't exist (lines 104-105). This is intentional - it allows hydration to work on repos that have never had issues.json yet. Only `getHistoricalIDsViaDiff()` will find historical IDs from git.

**ID Format Validation**:

The `looksLikeIssueID()` function validates format strictly (lines 150-176). This prevents parsing errors from embedded JSON with accidental ID-like strings. Example: if issue description contains `"id":"some-text"`, it won't be treated as an issue ID.

**Integration with Migrate Tombstones**:

The `@/cmd/bd/migrate_tombstones.go` command creates tombstones using `convertDeletionRecordToTombstone()` (lines 268-284). These tombstones have `Status == types.StatusTombstone`. The fix works because migrate-tombstones sets this status correctly (verified by `TestMigrateTombstones_TombstonesAreValid()` in migrate_tombstones_test.go).

**State Machine for Deleted Issues**:

There are now two ways an issue can be marked as deleted:
1. **Database state**: Issue has `status = "tombstone"` in the database (from `@/internal/storage/sqlite`)
2. **Manifest state**: Issue ID appears in `deletions.jsonl` (from `@/internal/deletions`)

The deletion hydration logic treats deletions manifest as the source of truth for what SHOULD be deleted, then applies those deletions to the database. The fix ensures the manifest only contains legitimate deletions, not tombstones that were migrated from the manifest.

Created and maintained by Nori.
