# Compaction Feature Issues

This file contains all issues for the database compaction feature, ready to import with:
```bash
bd create -f compaction-issues.md
```

---

## Epic: Add intelligent database compaction with Claude Haiku

### Type
epic

### Priority
2

### Description

Implement multi-tier database compaction using Claude Haiku to semantically compress old, closed issues. This keeps the database lightweight and agent-friendly while preserving essential context.

Goals:
- 70-95% space reduction for eligible issues
- Full restore capability via snapshots
- Opt-in with dry-run safety
- ~$1 per 1,000 issues compacted

### Acceptance Criteria
- Schema migration with snapshots table
- Haiku integration for summarization
- Two-tier compaction (30d, 90d)
- CLI with dry-run, restore, stats
- Full test coverage
- Documentation complete

### Labels
compaction, epic, haiku, v1.1

---

## Add compaction schema and migrations

### Type
task

### Priority
1

### Description

Add database schema support for issue compaction tracking and snapshot storage.

### Design

Add three columns to `issues` table:
- `compaction_level INTEGER DEFAULT 0` - 0=original, 1=tier1, 2=tier2
- `compacted_at DATETIME` - when last compacted
- `original_size INTEGER` - bytes before first compaction

Create `issue_snapshots` table:
```sql
CREATE TABLE issue_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    snapshot_time DATETIME NOT NULL,
    compaction_level INTEGER NOT NULL,
    original_size INTEGER NOT NULL,
    compressed_size INTEGER NOT NULL,
    original_content TEXT NOT NULL,  -- JSON blob
    archived_events TEXT,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);
```

Add indexes:
- `idx_snapshots_issue` on `issue_id`
- `idx_snapshots_level` on `compaction_level`

Add migration functions in `internal/storage/sqlite/sqlite.go`:
- `migrateCompactionColumns(db *sql.DB) error`
- `migrateSnapshotsTable(db *sql.DB) error`

### Acceptance Criteria
- Existing databases migrate automatically
- New databases include columns by default
- Migration is idempotent (safe to run multiple times)
- No data loss during migration
- Tests verify migration on fresh and existing DBs

### Labels
compaction, schema, migration, database

---

## Add compaction configuration keys

### Type
task

### Priority
1

### Description

Add configuration keys for compaction behavior with sensible defaults.

### Design

Add to `internal/storage/sqlite/schema.go` initial config:
```sql
INSERT OR IGNORE INTO config (key, value) VALUES
    ('compact_tier1_days', '30'),
    ('compact_tier1_dep_levels', '2'),
    ('compact_tier2_days', '90'),
    ('compact_tier2_dep_levels', '5'),
    ('compact_tier2_commits', '100'),
    ('compact_model', 'claude-3-5-haiku-20241022'),
    ('compact_batch_size', '50'),
    ('compact_parallel_workers', '5'),
    ('auto_compact_enabled', 'false');
```

Add helper functions for loading config into typed struct.

### Acceptance Criteria
- Config keys created on init
- Existing DBs get defaults on migration
- `bd config get/set` works with all keys
- Type validation (days=int, enabled=bool)
- Documentation in README.md

### Labels
compaction, config, configuration

---

## Implement candidate identification queries

### Type
task

### Priority
1

### Description

Write SQL queries to identify issues eligible for Tier 1 and Tier 2 compaction based on closure time and dependency status.

### Design

Create `internal/storage/sqlite/compact.go` with:

```go
type CompactionCandidate struct {
    IssueID       string
    ClosedAt      time.Time
    OriginalSize  int
    EstimatedSize int
    DependentCount int
}

func (s *SQLiteStorage) GetTier1Candidates(ctx context.Context) ([]*CompactionCandidate, error)
func (s *SQLiteStorage) GetTier2Candidates(ctx context.Context) ([]*CompactionCandidate, error)
func (s *SQLiteStorage) CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error)
```

Use recursive CTE for dependency depth checking (similar to ready_issues view).

### Acceptance Criteria
- Tier 1 query filters by days and dependency depth
- Tier 2 query includes commit/issue count checks
- Dependency checking handles circular deps gracefully
- Performance: <100ms for 10,000 issue database
- Tests cover edge cases (no deps, circular deps, mixed status)

### Labels
compaction, sql, query, dependencies

---

## Create Haiku client and prompt templates

### Type
task

### Priority
1

### Description

Implement Claude Haiku API client with template-based prompts for Tier 1 and Tier 2 summarization.

### Design

Create `internal/compact/haiku.go`:

```go
type HaikuClient struct {
    client *anthropic.Client
    model  string
}

func NewHaikuClient(apiKey string) (*HaikuClient, error)
func (h *HaikuClient) SummarizeTier1(ctx context.Context, issue *types.Issue) (string, error)
func (h *HaikuClient) SummarizeTier2(ctx context.Context, issue *types.Issue) (string, error)
```

Use text/template for prompt rendering.

Tier 1 output format:
```
**Summary:** [2-3 sentences]
**Key Decisions:** [bullet points]
**Resolution:** [outcome]
```

Tier 2 output format:
```
Single paragraph ≤150 words covering what was built, why it mattered, lasting impact.
```

### Acceptance Criteria
- API key from env var or config (env takes precedence)
- Prompts render correctly with templates
- Rate limiting handled gracefully (exponential backoff)
- Network errors retry up to 3 times
- Mock tests for API calls

### Labels
compaction, haiku, api, llm

---

## Implement snapshot creation and restoration

### Type
task

### Priority
1

### Description

Implement snapshot creation before compaction and restoration capability to undo compaction.

### Design

Add to `internal/storage/sqlite/compact.go`:

```go
func (s *SQLiteStorage) CreateSnapshot(ctx context.Context, issue *types.Issue, level int) error
func (s *SQLiteStorage) RestoreFromSnapshot(ctx context.Context, issueID string, level int) error
func (s *SQLiteStorage) GetSnapshots(ctx context.Context, issueID string) ([]*Snapshot, error)
```

Snapshot JSON structure:
```json
{
  "description": "...",
  "design": "...",
  "notes": "...",
  "acceptance_criteria": "...",
  "title": "..."
}
```

### Acceptance Criteria
- Snapshot created atomically with compaction
- Restore returns exact original content
- Multiple snapshots per issue supported (Tier 1 → Tier 2)
- JSON encoding handles UTF-8 and special characters
- Size calculation is accurate (UTF-8 bytes)

### Labels
compaction, snapshot, restore, safety

---

## Implement Tier 1 compaction logic

### Type
task

### Priority
1

### Description

Implement the core Tier 1 compaction process: snapshot → summarize → update.

### Design

Add to `internal/compact/compactor.go`:

```go
type Compactor struct {
    store  storage.Storage
    haiku  *HaikuClient
    config *CompactConfig
}

func New(store storage.Storage, apiKey string, config *CompactConfig) (*Compactor, error)
func (c *Compactor) CompactTier1(ctx context.Context, issueID string) error
func (c *Compactor) CompactTier1Batch(ctx context.Context, issueIDs []string) error
```

Process:
1. Verify eligibility
2. Calculate original size
3. Create snapshot
4. Call Haiku for summary
5. Update issue (description=summary, clear design/notes/criteria)
6. Set compaction_level=1, compacted_at=now, original_size
7. Record EventCompacted
8. Mark dirty for export

### Acceptance Criteria
- Single issue compaction works end-to-end
- Batch processing with parallel workers (5 concurrent)
- Errors don't corrupt database (transaction rollback)
- EventCompacted includes size savings
- Dry-run mode (identify + size estimate only, no API calls)

### Labels
compaction, tier1, core-logic

---

## Implement Tier 2 compaction logic

### Type
task

### Priority
2

### Description

Implement Tier 2 ultra-compression: more aggressive summarization and optional event pruning.

### Design

Add to `internal/compact/compactor.go`:

```go
func (c *Compactor) CompactTier2(ctx context.Context, issueID string) error
func (c *Compactor) CompactTier2Batch(ctx context.Context, issueIDs []string) error
```

Process:
1. Verify issue is at compaction_level = 1
2. Check Tier 2 eligibility (days, deps, commits/issues)
3. Create Tier 2 snapshot
4. Call Haiku with ultra-compression prompt
5. Update issue (description = single paragraph, clear all other fields)
6. Set compaction_level = 2
7. Optionally prune events (keep created/closed, archive rest to snapshot)

### Acceptance Criteria
- Requires existing Tier 1 compaction
- Git commit counting works (with fallback to issue counter)
- Events optionally pruned (config: compact_events_enabled)
- Archived events stored in snapshot JSON
- Size reduction 90-95%

### Labels
compaction, tier2, advanced

---

## Add `bd compact` CLI command

### Type
task

### Priority
1

### Description

Implement the `bd compact` command with dry-run, batch processing, and progress reporting.

### Design

Create `cmd/bd/compact.go`:

```go
var compactCmd = &cobra.Command{
    Use:   "compact",
    Short: "Compact old closed issues to save space",
}

Flags:
  --dry-run              Preview without compacting
  --tier int            Compaction tier (1 or 2, default: 1)
  --all                 Process all candidates
  --id string           Compact specific issue
  --force               Force compact (bypass checks, requires --id)
  --batch-size int      Issues per batch
  --workers int         Parallel workers
  --json                JSON output
```

### Acceptance Criteria
- `--dry-run` shows accurate preview with size estimates
- `--all` processes all candidates
- `--id` compacts single issue
- `--force` bypasses eligibility checks (only with --id)
- Progress bar for batches (e.g., [████████] 47/47)
- JSON output with `--json`
- Exit codes: 0=success, 1=error
- Shows summary: count, size saved, cost, time

### Labels
compaction, cli, command

---

## Add `bd compact --restore` functionality

### Type
task

### Priority
2

### Description

Implement restore command to undo compaction from snapshots.

### Design

Add to `cmd/bd/compact.go`:

```go
var compactRestore string

compactCmd.Flags().StringVar(&compactRestore, "restore", "", "Restore issue from snapshot")
```

Process:
1. Load snapshot for issue
2. Parse JSON content
3. Update issue with original content
4. Set compaction_level = 0, compacted_at = NULL, original_size = NULL
5. Record event (EventRestored or EventUpdated)
6. Mark dirty for export

### Acceptance Criteria
- Restores exact original content
- Handles multiple snapshots (use latest by default)
- `--level` flag to choose specific snapshot
- Updates compaction_level correctly
- Exports restored content to JSONL
- Shows before/after in output

### Labels
compaction, restore, cli

---

## Add `bd compact --stats` command

### Type
task

### Priority
2

### Description

Add statistics command showing compaction status and potential savings.

### Design

```go
var compactStats bool

compactCmd.Flags().BoolVar(&compactStats, "stats", false, "Show compaction statistics")
```

Output:
- Total issues, by compaction level (0, 1, 2)
- Current DB size vs estimated uncompacted size
- Space savings (KB/MB and %)
- Candidates for each tier with size estimates
- Estimated API cost (Haiku pricing)

### Acceptance Criteria
- Accurate counts by compaction_level
- Size calculations include all text fields (UTF-8 bytes)
- Shows candidates with eligibility reasons
- Cost estimation based on current Haiku pricing
- JSON output supported
- Clear, readable table format

### Labels
compaction, stats, reporting

---

## Add EventCompacted to event system

### Type
task

### Priority
2

### Description

Add new event type for tracking compaction in audit trail.

### Design

1. Add to `internal/types/types.go`:
```go
const EventCompacted EventType = "compacted"
```

2. Record event during compaction:
```go
eventData := map[string]interface{}{
    "tier": tier,
    "original_size": originalSize,
    "compressed_size": compressedSize,
    "reduction_pct": (1 - float64(compressedSize)/float64(originalSize)) * 100,
}
```

3. Update event display in `bd show`.

### Acceptance Criteria
- Event includes tier, original_size, compressed_size, reduction_pct
- Shows in event history (`bd events <id>`)
- Exports to JSONL correctly
- `bd show` displays compaction status and marker

### Labels
compaction, events, audit

---

## Add compaction indicator to `bd show`

### Type
task

### Priority
2

### Description

Update `bd show` command to display compaction status prominently.

### Design

Add to issue display:
```
bd-42: Fix authentication bug [CLOSED] 🗜️

Status: closed (compacted L1)
...

---
💾 Restore: bd compact --restore bd-42
📊 Original: 2,341 bytes | Compressed: 468 bytes (80% reduction)
🗜️ Compacted: 2025-10-15 (Tier 1)
```

Emoji indicators:
- Tier 1: 🗜️
- Tier 2: 📦

### Acceptance Criteria
- Compaction status visible in title line
- Footer shows size savings when compacted
- Restore command shown for compacted issues
- Works with `--json` output (includes compaction fields)
- Emoji optional (controlled by config or terminal detection)

### Labels
compaction, ui, display

---

## Write compaction tests

### Type
task

### Priority
1

### Description

Comprehensive test suite for compaction functionality.

### Design

Test coverage:

1. **Candidate Identification:**
   - Eligibility by time
   - Dependency depth checking
   - Mixed status dependents
   - Edge cases (no deps, circular deps)

2. **Snapshots:**
   - Create and restore
   - Multiple snapshots per issue
   - Content integrity (UTF-8, special chars)

3. **Tier 1 Compaction:**
   - Single issue compaction
   - Batch processing
   - Error handling (API failures)

4. **Tier 2 Compaction:**
   - Requires Tier 1
   - Events pruning
   - Commit counting fallback

5. **CLI:**
   - All flag combinations
   - Dry-run accuracy
   - JSON output parsing

6. **Integration:**
   - End-to-end flow
   - JSONL export/import
   - Restore verification

### Acceptance Criteria
- Test coverage >80%
- All edge cases covered
- Mock Haiku API in tests (no real API calls)
- Integration tests pass
- `go test ./...` passes
- Benchmarks for performance-critical paths

### Labels
compaction, testing, quality

---

## Add compaction documentation

### Type
task

### Priority
2

### Description

Document compaction feature in README and create detailed COMPACTION.md guide.

### Design

**Update README.md:**
- Add to Features section
- CLI examples (dry-run, compact, restore, stats)
- Configuration guide
- Cost analysis

**Create COMPACTION.md:**
- How compaction works (architecture overview)
- When to use each tier
- Detailed cost analysis with examples
- Safety mechanisms (snapshots, restore, dry-run)
- Troubleshooting guide
- FAQ

**Create examples/compaction/:**
- `workflow.sh` - Example monthly compaction workflow
- `cron-compact.sh` - Cron job setup
- `auto-compact.sh` - Auto-compaction script

### Acceptance Criteria
- README.md updated with compaction section
- COMPACTION.md comprehensive and clear
- Examples work as documented (tested)
- Screenshots or ASCII examples included
- API key setup documented (env var vs config)
- Covers common questions and issues

### Labels
compaction, docs, documentation, examples

---

## Optional: Implement auto-compaction

### Type
task

### Priority
3

### Description

Implement automatic compaction triggered by certain operations when enabled via config.

### Design

Trigger points (when `auto_compact_enabled = true`):
1. `bd stats` - check and compact if candidates exist
2. `bd export` - before exporting
3. Configurable: on any read operation after N candidates accumulate

Add:
```go
func (s *SQLiteStorage) AutoCompact(ctx context.Context) error {
    enabled, _ := s.GetConfig(ctx, "auto_compact_enabled")
    if enabled != "true" {
        return nil
    }

    // Run Tier 1 compaction on all candidates
    // Limit to batch_size to avoid long operations
    // Log activity for transparency
}
```

### Acceptance Criteria
- Respects auto_compact_enabled config (default: false)
- Limits batch size to avoid blocking operations
- Logs compaction activity (visible with --verbose)
- Can be disabled per-command with `--no-auto-compact` flag
- Only compacts Tier 1 (Tier 2 remains manual)
- Doesn't run more than once per hour (rate limiting)

### Labels
compaction, automation, optional, v1.2

---

## Optional: Add git commit counting

### Type
task

### Priority
3

### Description

Implement git commit counting for "project time" measurement as alternative to calendar time for Tier 2 eligibility.

### Design

```go
func getCommitsSince(closedAt time.Time) (int, error) {
    cmd := exec.Command("git", "rev-list", "--count",
        fmt.Sprintf("--since=%s", closedAt.Format(time.RFC3339)), "HEAD")
    output, err := cmd.Output()
    if err != nil {
        return 0, err  // Not in git repo or git not available
    }
    return strconv.Atoi(strings.TrimSpace(string(output)))
}
```

Fallback strategies:
1. Git commit count (preferred)
2. Issue counter delta (store counter at close time, compare later)
3. Pure time-based (90 days)

### Acceptance Criteria
- Counts commits since closed_at timestamp
- Handles git not available gracefully (falls back)
- Fallback to issue counter delta works
- Configurable via compact_tier2_commits config key
- Tested with real git repo
- Works in non-git environments

### Labels
compaction, git, optional, tier2
