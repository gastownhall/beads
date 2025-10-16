# Issue Database Compaction Design

**Status:** Design Phase
**Created:** 2025-10-15
**Target:** Beads v1.1

## Executive Summary

Add intelligent database compaction to beads that uses Claude Haiku to semantically compress old, closed issues. This keeps the database lightweight and agent-friendly while preserving essential context about past work. The design philosophy: **most work is throwaway, and forensic value decays exponentially with time**.

### Key Metrics
- **Space savings:** 70-95% reduction in text volume for old issues
- **Cost:** ~$1.10 per 1,000 issues compacted (Haiku pricing)
- **Safety:** Full snapshot system with restore capability
- **Performance:** Batch processing with parallel workers

---

## Motivation

### The Problem

Beads databases grow indefinitely:
- Issues accumulate detailed `description`, `design`, `notes`, `acceptance_criteria` fields
- Events table logs every change forever
- Old closed issues (especially those with all dependents closed) rarely need full detail
- Agent context windows work better with concise, relevant information

### Why This Matters

1. **Agent Efficiency:** Smaller databases → faster queries → clearer agent thinking
2. **Context Management:** Agents benefit from summaries of old work, not verbose details
3. **Git Performance:** Smaller JSONL exports → faster git operations
4. **Pragmatic Philosophy:** Beads is agent memory, not a historical archive
5. **Forensic Decay:** Need for detail decreases exponentially after closure

### What We Keep

- Issue ID and title (always)
- Semantic summary of what was done and why
- Key architectural decisions
- Closure outcome
- Full git history in JSONL commits (ultimate backup)
- Restore capability via snapshots

---

## Technical Design

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                      Compaction Pipeline                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  1. Candidate Identification                                     │
│     ↓                                                             │
│     • Query closed issues meeting time + dependency criteria     │
│     • Check dependency depth (recursive CTE)                     │
│     • Calculate size/savings estimates                           │
│                                                                   │
│  2. Snapshot Creation                                            │
│     ↓                                                             │
│     • Store original content in issue_snapshots table            │
│     • Calculate content hash for verification                    │
│     • Enable restore capability                                  │
│                                                                   │
│  3. Haiku Summarization                                          │
│     ↓                                                             │
│     • Batch process with worker pool (5 parallel)                │
│     • Different prompts for Tier 1 vs Tier 2                     │
│     • Handle API errors gracefully                               │
│                                                                   │
│  4. Issue Update                                                 │
│     ↓                                                             │
│     • Replace verbose fields with summary                        │
│     • Set compaction_level and compacted_at                      │
│     • Record event (EventCompacted)                              │
│     • Mark dirty for JSONL export                                │
│                                                                   │
│  5. Optional: Events Pruning                                     │
│     ↓                                                             │
│     • Keep only created/closed events for Tier 2                 │
│     • Archive detailed event history to snapshots                │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

### Compaction Tiers

#### **Tier 1: Standard Compaction**

**Eligibility:**
- `status = 'closed'`
- `closed_at >= 30 days ago` (configurable: `compact_tier1_days`)
- All issues that depend on this one (via `blocks` or `parent-child`) are closed
- Dependency check depth: 2 levels (configurable: `compact_tier1_dep_levels`)

**Process:**
1. Snapshot original content
2. Send to Haiku with 300-word summarization prompt
3. Store summary in `description`
4. Clear `design`, `notes`, `acceptance_criteria`
5. Set `compaction_level = 1`
6. Keep all events

**Output Format (Haiku prompt):**
```
**Summary:** [2-3 sentences: problem, solution, outcome]
**Key Decisions:** [bullet points of non-obvious choices]
**Resolution:** [how it was closed]
```

**Expected Reduction:** 70-85% of original text size

#### **Tier 2: Aggressive Compaction**

**Eligibility:**
- Already at `compaction_level = 1`
- `closed_at >= 90 days ago` (configurable: `compact_tier2_days`)
- All dependencies (all 4 types) up to 5 levels deep are closed
- One of:
  - ≥100 git commits since `closed_at` (configurable: `compact_tier2_commits`)
  - ≥500 new issues created since closure
  - Manual override with `--force`

**Process:**
1. Snapshot Tier 1 content
2. Send to Haiku with 150-word ultra-compression prompt
3. Store single paragraph in `description`
4. Clear all other text fields
5. Set `compaction_level = 2`
6. Prune events: keep only `created` and `closed`, move rest to snapshot

**Output Format (Haiku prompt):**
```
Single paragraph (≤150 words):
- What was built/fixed
- Why it mattered
- Lasting architectural impact (if any)
```

**Expected Reduction:** 90-95% of original text size

---

### Schema Changes

#### New Columns on `issues` Table

```sql
ALTER TABLE issues ADD COLUMN compaction_level INTEGER DEFAULT 0;
ALTER TABLE issues ADD COLUMN compacted_at DATETIME;
ALTER TABLE issues ADD COLUMN original_size INTEGER; -- bytes before compaction
```

#### New Table: `issue_snapshots`

```sql
CREATE TABLE IF NOT EXISTS issue_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    snapshot_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    compaction_level INTEGER NOT NULL, -- 1 or 2
    original_size INTEGER NOT NULL,
    compressed_size INTEGER NOT NULL,
    -- JSON blob with original content
    original_content TEXT NOT NULL,
    -- Optional: compressed events for Tier 2
    archived_events TEXT,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_snapshots_issue ON issue_snapshots(issue_id);
CREATE INDEX IF NOT EXISTS idx_snapshots_level ON issue_snapshots(compaction_level);
```

**Snapshot JSON Structure:**
```json
{
  "description": "original description text...",
  "design": "original design text...",
  "notes": "original notes text...",
  "acceptance_criteria": "original criteria text...",
  "title": "original title",
  "hash": "sha256:abc123..."
}
```

#### New Config Keys

```sql
INSERT INTO config (key, value) VALUES
    -- Tier 1 settings
    ('compact_tier1_days', '30'),
    ('compact_tier1_dep_levels', '2'),

    -- Tier 2 settings
    ('compact_tier2_days', '90'),
    ('compact_tier2_dep_levels', '5'),
    ('compact_tier2_commits', '100'),
    ('compact_tier2_new_issues', '500'),

    -- API settings
    ('anthropic_api_key', ''),  -- Falls back to ANTHROPIC_API_KEY env var
    ('compact_model', 'claude-3-5-haiku-20241022'),

    -- Performance settings
    ('compact_batch_size', '50'),
    ('compact_parallel_workers', '5'),

    -- Safety settings
    ('auto_compact_enabled', 'false'),  -- Opt-in
    ('compact_events_enabled', 'false'), -- Events pruning (Tier 2)

    -- Display settings
    ('compact_show_savings', 'true');  -- Show size reduction in output
```

#### New Event Type

```go
const (
    // ... existing event types ...
    EventCompacted EventType = "compacted"
)
```

---

### Haiku Integration

#### Prompt Templates

**Tier 1 Prompt:**
```
Summarize this closed software issue. Preserve key decisions, implementation approach, and outcome. Max 300 words.

Title: {{.Title}}
Type: {{.IssueType}}
Priority: {{.Priority}}

Description:
{{.Description}}

Design Notes:
{{.Design}}

Implementation Notes:
{{.Notes}}

Acceptance Criteria:
{{.AcceptanceCriteria}}

Output format:
**Summary:** [2-3 sentences: what problem, what solution, what outcome]
**Key Decisions:** [bullet points of non-obvious choices]
**Resolution:** [how it was closed]
```

**Tier 2 Prompt:**
```
Ultra-compress this old closed issue to ≤150 words. Focus on lasting architectural impact.

Title: {{.Title}}
Original Summary (already compressed):
{{.Description}}

Output a single paragraph covering:
- What was built/fixed
- Why it mattered
- Lasting impact (if any)

If there's no lasting impact, just state what was done and that it's resolved.
```

#### API Client Structure

```go
package compact

import (
    "github.com/anthropics/anthropic-sdk-go"
)

type HaikuClient struct {
    client *anthropic.Client
    model  string
}

func NewHaikuClient(apiKey string) *HaikuClient {
    return &HaikuClient{
        client: anthropic.NewClient(apiKey),
        model:  anthropic.ModelClaude_3_5_Haiku_20241022,
    }
}

func (h *HaikuClient) Summarize(ctx context.Context, prompt string, maxTokens int) (string, error)
```

#### Error Handling

- **Rate limits:** Exponential backoff with jitter
- **API errors:** Log and skip issue (don't fail entire batch)
- **Network failures:** Retry up to 3 times
- **Invalid responses:** Fall back to truncation with warning
- **Context length:** Truncate input if needed (rare, but possible)

---

### Dependency Checking

Reuse existing recursive CTE logic from `ready_issues` view, adapted for compaction:

```sql
-- Check if issue and N levels of dependents are all closed
WITH RECURSIVE dependent_tree AS (
    -- Base case: the candidate issue
    SELECT id, status, 0 as depth
    FROM issues
    WHERE id = ?

    UNION ALL

    -- Recursive case: issues that depend on this one
    SELECT i.id, i.status, dt.depth + 1
    FROM dependent_tree dt
    JOIN dependencies d ON d.depends_on_id = dt.id
    JOIN issues i ON i.id = d.issue_id
    WHERE d.type IN ('blocks', 'parent-child')  -- Only blocking deps matter
      AND dt.depth < ?  -- Max depth parameter
)
SELECT CASE
    WHEN COUNT(*) = SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END)
    THEN 1 ELSE 0 END as all_closed
FROM dependent_tree;
```

**Performance:** This query is O(N) where N is the number of dependents. With proper indexes, should be <10ms per issue.

---

### Git Integration (Optional)

For "project time" measurement via commit counting:

```go
func getCommitsSince(closedAt time.Time) (int, error) {
    cmd := exec.Command("git", "rev-list", "--count",
        fmt.Sprintf("--since=%s", closedAt.Format(time.RFC3339)),
        "HEAD")
    output, err := cmd.Output()
    if err != nil {
        return 0, err
    }
    return strconv.Atoi(strings.TrimSpace(string(output)))
}
```

**Fallback:** If git unavailable or not in a repo, use issue counter delta:
```sql
SELECT last_id FROM issue_counters WHERE prefix = ?
-- Store at close time, compare at compaction time
```

---

### CLI Commands

#### `bd compact` - Main Command

```bash
bd compact [flags]

Flags:
  --dry-run              Show what would be compacted without doing it
  --tier int            Compaction tier (1 or 2, default: 1)
  --all                 Process all eligible issues (default: preview only)
  --id string           Compact specific issue by ID
  --force               Bypass eligibility checks (with --id)
  --batch-size int      Issues per batch (default: from config)
  --workers int         Parallel workers (default: from config)
  --json                JSON output for agents

Examples:
  bd compact --dry-run                 # Preview Tier 1 candidates
  bd compact --dry-run --tier 2        # Preview Tier 2 candidates
  bd compact --all                     # Compact all Tier 1 candidates
  bd compact --tier 2 --all            # Compact all Tier 2 candidates
  bd compact --id bd-42                # Compact specific issue
  bd compact --id bd-42 --force        # Force compact even if recent
```

#### `bd compact --restore` - Restore Compacted Issues

```bash
bd compact --restore <issue-id> [flags]

Flags:
  --level int           Restore to specific snapshot level (default: latest)
  --json                JSON output

Examples:
  bd compact --restore bd-42           # Restore bd-42 from latest snapshot
  bd compact --restore bd-42 --level 1 # Restore from Tier 1 snapshot
```

#### `bd compact --stats` - Compaction Statistics

```bash
bd compact --stats [flags]

Flags:
  --json                JSON output

Example output:
  === Compaction Statistics ===

  Total Issues: 1,247
  Compacted (Tier 1): 342 (27.4%)
  Compacted (Tier 2): 89 (7.1%)

  Database Size: 2.3 MB
  Estimated Uncompacted Size: 8.7 MB
  Space Savings: 6.4 MB (73.6%)

  Candidates:
    Tier 1: 47 issues (est. 320 KB → 64 KB)
    Tier 2: 15 issues (est. 180 KB → 18 KB)

  Estimated Compaction Cost: $0.04 (Haiku)
```

---

### Output Examples

#### Dry Run Output

```
$ bd compact --dry-run

=== Tier 1 Compaction Preview ===
Eligibility: Closed ≥30 days, 2 levels of dependents closed

bd-42: Fix authentication bug (P1, bug)
  Closed: 45 days ago
  Size: 2,341 bytes → ~468 bytes (80% reduction)
  Dependents: bd-43, bd-44 (both closed)

bd-57: Add login form (P2, feature)
  Closed: 38 days ago
  Size: 1,823 bytes → ~365 bytes (80% reduction)
  Dependents: (none)

bd-58: Refactor auth middleware (P2, task)
  Closed: 35 days ago
  Size: 3,102 bytes → ~620 bytes (80% reduction)
  Dependents: bd-59 (closed)

... (44 more issues)

Total: 47 issues
Estimated reduction: 87.3 KB → 17.5 KB (80%)
Estimated cost: $0.03 (Haiku API)

Run with --all to compact these issues.
```

#### Compaction Progress

```
$ bd compact --all

Compacting 47 issues (Tier 1)...

Creating snapshots... [████████████████████] 47/47
Calling Haiku API...   [████████████████████] 47/47 (12s, $0.027)
Updating issues...     [████████████████████] 47/47

✓ Successfully compacted 47 issues
  Size reduction: 87.3 KB → 18.2 KB (79.1%)
  API cost: $0.027
  Time: 14.3s

Compacted issues will be exported to .beads/issues.jsonl
```

#### Show Compacted Issue

```
$ bd show bd-42

bd-42: Fix authentication bug [CLOSED] 🗜️

Status: closed (compacted L1)
Priority: 1 (High)
Type: bug
Closed: 2025-08-31 (45 days ago)
Compacted: 2025-10-15 (saved 1,873 bytes)

**Summary:** Fixed race condition in JWT token refresh logic causing intermittent
401 errors under high load. Implemented mutex-based locking around token refresh
operations. All users can now stay authenticated reliably during concurrent requests.

**Key Decisions:**
- Used sync.RWMutex instead of channels for simpler reasoning about lock state
- Added exponential backoff to token refresh to prevent thundering herd
- Preserved existing token format for backward compatibility with mobile clients

**Resolution:** Deployed to production on Aug 31, monitored for 2 weeks with zero
401 errors. Closed after confirming fix with load testing.

---
💾 Restore: bd compact --restore bd-42
📊 Original size: 2,341 bytes | Compressed: 468 bytes (80% reduction)
```

---

### Safety Mechanisms

1. **Snapshot-First:** Always create snapshot before modifying issue
2. **Restore Capability:** Full restore from snapshots with `--restore`
3. **Opt-In Auto-Compaction:** Disabled by default (`auto_compact_enabled = false`)
4. **Dry-Run Required:** Preview before committing with `--dry-run`
5. **Git Backup:** JSONL exports preserve full history in git commits
6. **Audit Trail:** `EventCompacted` records what was done and when
7. **Size Verification:** Track original_size and compressed_size for validation
8. **Idempotent:** Re-running compaction on already-compacted issues is safe (no-op)
9. **Graceful Degradation:** API failures don't corrupt data, just skip issues
10. **Reversible:** Restore is always available, even after git push

---

### Testing Strategy

#### Unit Tests

1. **Candidate Identification:**
   - Issues meeting time criteria
   - Dependency depth checking
   - Mixed status dependents (some closed, some open)
   - Edge case: circular dependencies

2. **Haiku Client:**
   - Mock API responses
   - Rate limit handling
   - Error recovery
   - Prompt rendering

3. **Snapshot Management:**
   - Create snapshot
   - Restore from snapshot
   - Multiple snapshots per issue (Tier 1 → Tier 2)
   - Snapshot integrity (hash verification)

4. **Size Calculation:**
   - Accurate byte counting
   - UTF-8 handling
   - Empty fields

#### Integration Tests

1. **End-to-End Compaction:**
   - Create test issues
   - Age them (mock timestamps)
   - Run compaction
   - Verify summaries
   - Restore and verify

2. **Batch Processing:**
   - Large batches (100+ issues)
   - Parallel worker coordination
   - Error handling mid-batch

3. **JSONL Export:**
   - Compacted issues export correctly
   - Import preserves compaction_level
   - Round-trip fidelity

4. **CLI Commands:**
   - All flag combinations
   - JSON output parsing
   - Error messages

#### Manual Testing Checklist

- [ ] Dry-run shows accurate candidates
- [ ] Compaction reduces size as expected
- [ ] Haiku summaries are high quality
- [ ] Restore returns exact original content
- [ ] Stats command shows correct numbers
- [ ] Auto-compaction respects config
- [ ] Git workflow (commit → pull → auto-compact)
- [ ] Multi-machine workflow with compaction
- [ ] API key handling (env var vs config)
- [ ] Rate limit handling under load

---

### Performance Considerations

#### Scalability

**Small databases (<1,000 issues):**
- Full scan acceptable
- Compact all eligible in one run
- <1 minute total time

**Medium databases (1,000-10,000 issues):**
- Batch processing required
- Progress reporting essential
- 5-10 minutes total time

**Large databases (>10,000 issues):**
- Incremental compaction (process N per run)
- Consider scheduled background job
- 30-60 minutes total time

#### Optimization Strategies

1. **Index Usage:**
   - `idx_issues_status` - filter closed issues
   - `idx_dependencies_depends_on` - dependency traversal
   - `idx_snapshots_issue` - restore lookups

2. **Batch Sizing:**
   - Default 50 issues per batch
   - Configurable via `compact_batch_size`
   - Trade-off: larger batches = fewer commits, more RAM

3. **Parallel Workers:**
   - Default 5 parallel Haiku calls
   - Configurable via `compact_parallel_workers`
   - Respects Haiku rate limits

4. **Query Optimization:**
   - Use prepared statements for snapshots
   - Reuse dependency check query
   - Avoid N+1 queries in batch operations

---

### Cost Analysis

#### Haiku Pricing (as of 2025-10-15)

- Input: $0.25 per million tokens (~$0.0003 per 1K tokens)
- Output: $1.25 per million tokens (~$0.0013 per 1K tokens)

#### Per-Issue Estimates

**Tier 1:**
- Input: ~1,000 tokens (full issue content)
- Output: ~400 tokens (summary)
- Cost: ~$0.0008 per issue

**Tier 2:**
- Input: ~500 tokens (Tier 1 summary)
- Output: ~200 tokens (ultra-compressed)
- Cost: ~$0.0003 per issue

#### Batch Costs

| Issues | Tier 1 Cost | Tier 2 Cost | Total |
|--------|-------------|-------------|-------|
| 100    | $0.08       | $0.03       | $0.11 |
| 500    | $0.40       | $0.15       | $0.55 |
| 1,000  | $0.80       | $0.30       | $1.10 |
| 5,000  | $4.00       | $1.50       | $5.50 |

**Monthly budget (typical project):**
- ~50-100 new issues closed per month
- ~30-60 days later, eligible for Tier 1
- Monthly cost: $0.04 - $0.08 (negligible)

---

### Configuration Examples

#### Conservative Setup (manual only)

```bash
bd init
bd config set compact_tier1_days 60
bd config set compact_tier2_days 180
bd config set auto_compact_enabled false

# Run manually when needed
bd compact --dry-run
bd compact --all  # after review
```

#### Aggressive Setup (auto-compact)

```bash
bd config set compact_tier1_days 14
bd config set compact_tier2_days 45
bd config set auto_compact_enabled true
bd config set compact_batch_size 100

# Auto-compacts on bd stats, bd export
bd stats  # triggers compaction if candidates exist
```

#### Development Setup (fast feedback)

```bash
bd config set compact_tier1_days 1
bd config set compact_tier2_days 3
bd config set compact_tier1_dep_levels 1
bd config set compact_tier2_dep_levels 2

# Test compaction on recently closed issues
```

---

### Future Enhancements

#### Phase 2 (Post-MVP)

1. **Local Model Support:**
   - Use Ollama for zero-cost summarization
   - Fallback chain: Haiku → Ollama → truncation

2. **Custom Prompts:**
   - User-defined summarization prompts
   - Per-project templates
   - Domain-specific summaries (e.g., "focus on API changes")

3. **Selective Preservation:**
   - Mark issues as "do not compact"
   - Preserve certain labels (e.g., `architecture`, `security`)
   - Field-level preservation (e.g., keep design notes, compress others)

4. **Analytics:**
   - Compaction effectiveness over time
   - Cost tracking per run
   - Quality feedback (user ratings of summaries)

5. **Smart Scheduling:**
   - Auto-detect optimal compaction times
   - Avoid compaction during active development
   - Weekend/off-hours processing

6. **Multi-Tier Expansion:**
   - Tier 3: Archive to separate file
   - Tier 4: Delete (with backup)
   - Configurable tier chain

#### Phase 3 (Advanced)

1. **Distributed Compaction:**
   - Coordinate across multiple machines
   - Avoid duplicate work in team settings
   - Lock mechanism for compaction jobs

2. **Incremental Summarization:**
   - Re-summarize if issue reopened
   - Preserve history of summaries
   - Version tracking for prompts

3. **Search Integration:**
   - Full-text search includes summaries
   - Boost compacted issues in search results
   - Semantic search using embeddings

---

## Implementation Issues

The following issues should be created in beads to track this work. They're designed to be implemented in dependency order.

---

### Epic: Issue Database Compaction

**Issue ID:** (auto-generated)
**Title:** Epic: Add intelligent database compaction with Claude Haiku
**Type:** epic
**Priority:** 2
**Description:**

Implement multi-tier database compaction using Claude Haiku to semantically compress old, closed issues. This keeps the database lightweight and agent-friendly while preserving essential context.

**Goals:**
- 70-95% space reduction for eligible issues
- Full restore capability via snapshots
- Opt-in with dry-run safety
- ~$1 per 1,000 issues compacted

**Acceptance Criteria:**
- [ ] Schema migration with snapshots table
- [ ] Haiku integration for summarization
- [ ] Two-tier compaction (30d, 90d)
- [ ] CLI with dry-run, restore, stats
- [ ] Full test coverage
- [ ] Documentation complete

**Dependencies:** None (this is the epic)

---

### Issue 1: Add compaction schema and migrations

**Type:** task
**Priority:** 1
**Dependencies:** Blocks all other compaction work

**Description:**

Add database schema support for issue compaction tracking and snapshot storage.

**Design:**

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

**Acceptance Criteria:**
- [ ] Existing databases migrate automatically
- [ ] New databases include columns by default
- [ ] Migration is idempotent (safe to run multiple times)
- [ ] No data loss during migration
- [ ] Tests verify migration on fresh and existing DBs

**Estimated Time:** 4 hours

---

### Issue 2: Add compaction configuration keys

**Type:** task
**Priority:** 1
**Dependencies:** Blocks compaction logic

**Description:**

Add configuration keys for compaction behavior with sensible defaults.

**Implementation:**

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

Add helper functions in `internal/storage/` or `cmd/bd/`:
```go
func getCompactConfig(ctx context.Context, store Storage) (*CompactConfig, error)
```

**Acceptance Criteria:**
- [ ] Config keys created on init
- [ ] Existing DBs get defaults on migration
- [ ] `bd config get/set` works with all keys
- [ ] Type validation (days=int, enabled=bool)
- [ ] Documentation in README.md

**Estimated Time:** 2 hours

---

### Issue 3: Implement candidate identification queries

**Type:** task
**Priority:** 1
**Dependencies:** Needs schema and config

**Description:**

Write SQL queries to identify issues eligible for Tier 1 and Tier 2 compaction.

**Design:**

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

Implement recursive dependency checking using CTE similar to `ready_issues` view.

**Acceptance Criteria:**
- [ ] Tier 1 query filters by days and dependency depth
- [ ] Tier 2 query includes commit/issue count checks
- [ ] Dependency checking handles circular deps gracefully
- [ ] Performance: <100ms for 10,000 issue database
- [ ] Tests cover edge cases (no deps, circular deps, mixed status)

**Estimated Time:** 6 hours

---

### Issue 4: Create Haiku client and prompt templates

**Type:** task
**Priority:** 1
**Dependencies:** None (can work in parallel)

**Description:**

Implement Claude Haiku API client with template-based prompts for Tier 1 and Tier 2 summarization.

**Implementation:**

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

Add error handling:
- Rate limit retry with exponential backoff
- Network errors: 3 retries
- Invalid responses: return error, don't corrupt data

**Acceptance Criteria:**
- [ ] API key from env var or config (env takes precedence)
- [ ] Prompts render correctly with template
- [ ] Rate limiting handled gracefully
- [ ] Mock tests for API calls
- [ ] Real integration test (optional, requires API key)

**Estimated Time:** 6 hours

---

### Issue 5: Implement snapshot creation and restoration

**Type:** task
**Priority:** 1
**Dependencies:** Needs schema changes

**Description:**

Implement snapshot creation before compaction and restoration capability.

**Implementation:**

Add to `internal/storage/sqlite/compact.go`:

```go
type Snapshot struct {
    ID              int64
    IssueID         string
    SnapshotTime    time.Time
    CompactionLevel int
    OriginalSize    int
    CompressedSize  int
    OriginalContent string  // JSON
    ArchivedEvents  string  // JSON, nullable
}

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

**Acceptance Criteria:**
- [ ] Snapshot created atomically with compaction
- [ ] Restore returns exact original content
- [ ] Multiple snapshots per issue supported (Tier 1 → Tier 2)
- [ ] JSON encoding handles special characters
- [ ] Size calculation is accurate (UTF-8 bytes)

**Estimated Time:** 5 hours

---

### Issue 6: Implement Tier 1 compaction logic

**Type:** task
**Priority:** 1
**Dependencies:** Needs Haiku client, snapshots, candidate queries

**Description:**

Implement the core Tier 1 compaction process: snapshot → summarize → update.

**Implementation:**

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
5. Update issue (description = summary, clear design/notes/criteria)
6. Set compaction_level = 1, compacted_at = now
7. Record EventCompacted
8. Mark dirty for export

**Acceptance Criteria:**
- [ ] Single issue compaction works end-to-end
- [ ] Batch processing with parallel workers
- [ ] Errors don't corrupt database (transaction rollback)
- [ ] EventCompacted includes size savings
- [ ] Dry-run mode (identify + size estimate only)

**Estimated Time:** 8 hours

---

### Issue 7: Implement Tier 2 compaction logic

**Type:** task
**Priority:** 2
**Dependencies:** Needs Tier 1 working

**Description:**

Implement Tier 2 ultra-compression: more aggressive summarization and optional event pruning.

**Implementation:**

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
5. Update issue (description = single paragraph, clear all else)
6. Set compaction_level = 2
7. Optionally prune events (keep created/closed, archive rest)

**Acceptance Criteria:**
- [ ] Requires existing Tier 1 compaction
- [ ] Git commit counting works (with fallback)
- [ ] Events optionally pruned (config: compact_events_enabled)
- [ ] Archived events stored in snapshot
- [ ] Size reduction 90-95%

**Estimated Time:** 6 hours

---

### Issue 8: Add `bd compact` CLI command

**Type:** task
**Priority:** 1
**Dependencies:** Needs Tier 1 compaction logic

**Description:**

Implement the `bd compact` command with dry-run, batch processing, and progress reporting.

**Implementation:**

Create `cmd/bd/compact.go`:

```go
var compactCmd = &cobra.Command{
    Use:   "compact",
    Short: "Compact old closed issues to save space",
    Long:  `...`,
}

var (
    compactDryRun    bool
    compactTier      int
    compactAll       bool
    compactID        string
    compactForce     bool
    compactBatchSize int
    compactWorkers   int
)

func init() {
    compactCmd.Flags().BoolVar(&compactDryRun, "dry-run", false, "Preview without compacting")
    compactCmd.Flags().IntVar(&compactTier, "tier", 1, "Compaction tier (1 or 2)")
    compactCmd.Flags().BoolVar(&compactAll, "all", false, "Process all candidates")
    compactCmd.Flags().StringVar(&compactID, "id", "", "Compact specific issue")
    compactCmd.Flags().BoolVar(&compactForce, "force", false, "Force compact (bypass checks)")
    // ... more flags
}
```

**Output:**
- Dry-run: Table of candidates with size estimates
- Actual run: Progress bar with batch updates
- Summary: Count, size saved, cost, time

**Acceptance Criteria:**
- [ ] `--dry-run` shows accurate preview
- [ ] `--all` processes all candidates
- [ ] `--id` compacts single issue
- [ ] `--force` bypasses eligibility checks (with --id)
- [ ] Progress bar for batches
- [ ] JSON output with `--json`
- [ ] Exit code: 0=success, 1=error

**Estimated Time:** 6 hours

---

### Issue 9: Add `bd compact --restore` functionality

**Type:** task
**Priority:** 2
**Dependencies:** Needs snapshots and CLI

**Description:**

Implement restore command to undo compaction from snapshots.

**Implementation:**

Add to `cmd/bd/compact.go`:

```go
var compactRestore string

compactCmd.Flags().StringVar(&compactRestore, "restore", "", "Restore issue from snapshot")
```

Process:
1. Load snapshot for issue
2. Parse JSON content
3. Update issue with original content
4. Set compaction_level = 0, compacted_at = NULL
5. Record EventRestored
6. Mark dirty

**Acceptance Criteria:**
- [ ] Restores exact original content
- [ ] Handles multiple snapshots (prompt user or use latest)
- [ ] `--level` flag to choose snapshot
- [ ] Updates compaction_level correctly
- [ ] Exports restored content to JSONL

**Estimated Time:** 4 hours

---

### Issue 10: Add `bd compact --stats` command

**Type:** task
**Priority:** 2
**Dependencies:** Needs compaction working

**Description:**

Add statistics command showing compaction status and potential savings.

**Implementation:**

```go
var compactStats bool

compactCmd.Flags().BoolVar(&compactStats, "stats", false, "Show compaction statistics")
```

Output:
- Total issues, by compaction level
- Current DB size vs estimated uncompacted size
- Space savings (MB and %)
- Candidates for each tier with estimates
- Estimated API cost

**Acceptance Criteria:**
- [ ] Accurate counts by compaction_level
- [ ] Size calculations include all text fields
- [ ] Shows candidates with eligibility reasons
- [ ] Cost estimation based on Haiku pricing
- [ ] JSON output supported

**Estimated Time:** 4 hours

---

### Issue 11: Add EventCompacted to event system

**Type:** task
**Priority:** 2
**Dependencies:** Needs schema changes

**Description:**

Add new event type for tracking compaction in audit trail.

**Implementation:**

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

3. Show in `bd show` output:
```
Events:
  2025-10-15: compacted (tier 1, saved 1.8KB, 80%)
  2025-08-31: closed by alice
  2025-08-20: created by alice
```

**Acceptance Criteria:**
- [ ] Event includes tier and size info
- [ ] Shows in event history
- [ ] Exports to JSONL
- [ ] `bd show` displays compaction marker

**Estimated Time:** 3 hours

---

### Issue 12: Add compaction indicator to `bd show`

**Type:** task
**Priority:** 2
**Dependencies:** Needs compaction working

**Description:**

Update `bd show` command to display compaction status prominently.

**Implementation:**

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

Show different emoji for tiers:
- Tier 1: 🗜️
- Tier 2: 📦

**Acceptance Criteria:**
- [ ] Compaction status visible in title
- [ ] Footer shows size savings
- [ ] Restore command shown
- [ ] Works with `--json` output

**Estimated Time:** 2 hours

---

### Issue 13: Write compaction tests

**Type:** task
**Priority:** 1
**Dependencies:** Needs all compaction logic

**Description:**

Comprehensive test suite for compaction functionality.

**Test Coverage:**

1. **Candidate Identification:**
   - Eligibility by time
   - Dependency depth checking
   - Mixed status dependents
   - Edge cases (no deps, circular)

2. **Snapshots:**
   - Create and restore
   - Multiple snapshots per issue
   - Content integrity

3. **Tier 1 Compaction:**
   - Single issue
   - Batch processing
   - Error handling

4. **Tier 2 Compaction:**
   - Requires Tier 1
   - Events pruning
   - Commit counting fallback

5. **CLI:**
   - All flag combinations
   - Dry-run accuracy
   - JSON output

6. **Integration:**
   - End-to-end flow
   - JSONL export/import
   - Restore verification

**Acceptance Criteria:**
- [ ] Test coverage >80%
- [ ] All edge cases covered
- [ ] Mock Haiku API in tests
- [ ] Integration tests pass
- [ ] `go test ./...` passes

**Estimated Time:** 8 hours

---

### Issue 14: Add compaction documentation

**Type:** task
**Priority:** 2
**Dependencies:** All features complete

**Description:**

Document compaction feature in README and create COMPACTION.md guide.

**Content:**

Update README.md:
- Add to Features section
- CLI examples
- Configuration guide

Create COMPACTION.md:
- How compaction works
- When to use each tier
- Cost analysis
- Safety mechanisms
- Troubleshooting

Create examples/compaction/:
- Example workflow
- Cron job setup
- Auto-compaction script

**Acceptance Criteria:**
- [ ] README.md updated
- [ ] COMPACTION.md comprehensive
- [ ] Examples work as documented
- [ ] Screenshots/examples included
- [ ] API key setup documented

**Estimated Time:** 4 hours

---

### Issue 15: Optional: Implement auto-compaction

**Type:** task
**Priority:** 3 (nice-to-have)
**Dependencies:** Needs all compaction working

**Description:**

Implement automatic compaction triggered by certain operations when enabled via config.

**Implementation:**

Trigger points (when `auto_compact_enabled = true`):
1. `bd stats` - check and compact if candidates exist
2. `bd export` - before exporting
3. Background timer (optional, via daemon)

Add:
```go
func (s *SQLiteStorage) AutoCompact(ctx context.Context) error {
    enabled, _ := s.GetConfig(ctx, "auto_compact_enabled")
    if enabled != "true" {
        return nil
    }

    // Run Tier 1 compaction on all candidates
    // Limit to batch_size to avoid long operations
}
```

**Acceptance Criteria:**
- [ ] Respects auto_compact_enabled config
- [ ] Limits batch size to avoid blocking
- [ ] Logs compaction activity
- [ ] Can be disabled per-command with `--no-auto-compact`

**Estimated Time:** 4 hours

---

### Issue 16: Optional: Add git commit counting

**Type:** task
**Priority:** 3 (nice-to-have)
**Dependencies:** Needs Tier 2 logic

**Description:**

Implement git commit counting for "project time" measurement as alternative to calendar time.

**Implementation:**

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

Fallback to issue counter delta if git unavailable.

**Acceptance Criteria:**
- [ ] Counts commits since closed_at
- [ ] Handles git not available gracefully
- [ ] Fallback to issue counter works
- [ ] Configurable via compact_tier2_commits
- [ ] Tested with real git repo

**Estimated Time:** 3 hours

---

## Success Metrics

### Technical Metrics

- [ ] 70-85% size reduction for Tier 1
- [ ] 90-95% size reduction for Tier 2
- [ ] <100ms candidate identification query
- [ ] <2s per issue compaction (Haiku latency)
- [ ] Zero data loss (all restore tests pass)

### Quality Metrics

- [ ] Haiku summaries preserve key information
- [ ] Developers can understand compacted issues
- [ ] Restore returns exact original content
- [ ] No corruption in multi-machine workflows

### Operational Metrics

- [ ] Cost: <$1.50 per 1,000 issues
- [ ] Dry-run accuracy: 95%+ estimate correctness
- [ ] Error rate: <1% API failures (with retry)
- [ ] User adoption: Docs clear, examples work

---

## Rollout Plan

### Phase 1: Alpha (Internal Testing)

1. Merge compaction feature to main
2. Test on beads' own database
3. Verify JSONL export/import
4. Validate Haiku summaries
5. Fix any critical bugs

### Phase 2: Beta (Opt-In)

1. Announce in README (opt-in, experimental)
2. Gather feedback from early adopters
3. Iterate on prompt templates
4. Add telemetry (optional, with consent)

### Phase 3: Stable (Default Disabled)

1. Mark feature as stable
2. Keep auto_compact_enabled = false by default
3. Encourage manual `bd compact --dry-run` first
4. Document in quickstart guide

### Phase 4: Mature (Consider Auto-Enable)

1. After 6+ months of stability
2. Consider auto-compaction for new users
3. Provide migration guide for disabling

---

## Risks and Mitigations

| Risk | Impact | Likelihood | Mitigation |
|------|--------|------------|------------|
| Haiku summaries lose critical info | High | Medium | Manual review in dry-run, restore capability, improve prompts |
| API rate limits during batch | Medium | Medium | Exponential backoff, respect rate limits, batch sizing |
| JSONL merge conflicts increase | Medium | Low | Compaction is deterministic per issue, git handles well |
| Users accidentally compress important issues | High | Low | Dry-run required, restore available, snapshots permanent |
| Cost higher than expected | Low | Low | Dry-run shows estimates, configurable batch sizes |
| Schema migration fails | High | Very Low | Idempotent migrations, tested on existing DBs |

---

## Open Questions

1. **Should compaction be reversible forever, or expire snapshots?**
   - Recommendation: Keep snapshots indefinitely (disk is cheap)

2. **Should we compress snapshots themselves (gzip)?**
   - Recommendation: Not in MVP, add if storage becomes issue

3. **Should tier selection be automatic or manual?**
   - Recommendation: Manual in MVP, auto-tier in future

4. **How to handle issues compacted on one machine but not another?**
   - Answer: JSONL export includes compaction_level, imports preserve it

5. **Should we support custom models (Sonnet, Opus)?**
   - Recommendation: Haiku only in MVP, add later if needed

---

## Appendix: Example Workflow

### Typical Monthly Compaction

```bash
# 1. Check what's eligible
$ bd compact --dry-run
=== Tier 1 Candidates ===
42 issues eligible (closed >30 days, deps closed)
Est. reduction: 127 KB → 25 KB (80%)
Est. cost: $0.03

# 2. Review candidates manually
$ bd list --status closed --json | jq 'map(select(.compaction_level == 0))'

# 3. Compact Tier 1
$ bd compact --all
✓ Compacted 42 issues in 18s ($0.03)

# 4. Check Tier 2 candidates (optional)
$ bd compact --dry-run --tier 2
=== Tier 2 Candidates ===
8 issues eligible (closed >90 days, 100+ commits since)
Est. reduction: 45 KB → 4 KB (91%)
Est. cost: $0.01

# 5. Compact Tier 2
$ bd compact --all --tier 2
✓ Compacted 8 issues in 6s ($0.01)

# 6. Export and commit
$ bd export -o .beads/issues.jsonl
$ git add .beads/issues.jsonl
$ git commit -m "Compact 50 old issues (saved 143 KB)"
$ git push

# 7. View stats
$ bd compact --stats
Total Space Saved: 143 KB (82% reduction)
Database Size: 2.1 MB (down from 2.3 MB)
```

---

## Conclusion

This design provides a comprehensive, safe, and cost-effective way to keep beads databases lightweight while preserving essential context. The two-tier approach balances aggressiveness with safety, and the snapshot system ensures full reversibility.

The use of Claude Haiku for semantic compression is key - it preserves meaning rather than just truncating text. At ~$1 per 1,000 issues, the cost is negligible for the value provided.

Implementation is straightforward with clear phases and well-defined issues. The MVP (Tier 1 only) can be delivered in ~40 hours of work, with Tier 2 and enhancements following incrementally.

This aligns perfectly with beads' philosophy: **pragmatic, agent-focused, and evolutionarily designed**.
