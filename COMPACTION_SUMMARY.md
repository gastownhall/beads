# Issue Compaction - Quick Reference

**Status:** Design Complete, Ready for Implementation
**Target:** Beads v1.1
**Estimated Effort:** 40-60 hours (MVP)

## What Is This?

Add intelligent database compaction to beads using Claude Haiku to semantically compress old, closed issues. Keep your database lightweight while preserving the meaning of past work.

## Why?

- **Agent Efficiency:** Smaller DBs → faster queries → clearer thinking
- **Context Management:** Agents need summaries, not verbose details
- **Pragmatic:** Most work is throwaway; forensic value decays exponentially
- **Git-Friendly:** Smaller JSONL exports

## How It Works

### Two-Tier Compression

**Tier 1: Standard (30 days)**
- Closed ≥30 days, all dependents closed (2 levels deep)
- Haiku summarizes to ~300 words
- Keeps: title, summary with key decisions
- Clears: verbose design/notes/criteria
- **Result:** 70-85% space reduction

**Tier 2: Aggressive (90 days)**
- Already Tier 1 compressed
- Closed ≥90 days, deep dependencies closed (5 levels)
- ≥100 git commits since closure (measures "project time")
- Ultra-compress to single ≤150 word paragraph
- Optionally prunes old events
- **Result:** 90-95% space reduction

### Safety First

- **Snapshots:** Full original content saved before compaction
- **Restore:** `bd compact --restore <id>` undoes compaction
- **Dry-Run:** `bd compact --dry-run` previews without changing anything
- **Git Backup:** JSONL commits preserve full history
- **Opt-In:** Disabled by default (`auto_compact_enabled = false`)

## CLI

```bash
# Preview candidates
bd compact --dry-run

# Compact Tier 1
bd compact --all

# Compact Tier 2
bd compact --tier 2 --all

# Compact specific issue
bd compact --id bd-42

# Restore compacted issue
bd compact --restore bd-42

# Show statistics
bd compact --stats
```

## Cost

**Haiku Pricing:**
- ~$0.0008 per issue (Tier 1)
- ~$0.0003 per issue (Tier 2)
- **~$1.10 per 1,000 issues total**

Negligible for typical usage (~$0.05/month for active project).

## Example Output

### Before Compaction
```
bd-42: Fix authentication bug [CLOSED]

Description: (800 words about the problem)

Design: (1,200 words of implementation notes)

Notes: (500 words of testing/deployment details)

Acceptance Criteria: (300 words of requirements)

Total: 2,341 bytes
```

### After Tier 1 Compaction
```
bd-42: Fix authentication bug [CLOSED] 🗜️

**Summary:** Fixed race condition in JWT token refresh logic causing
intermittent 401 errors. Implemented mutex-based locking. All users
can now stay authenticated reliably.

**Key Decisions:**
- Used sync.RWMutex for simpler reasoning
- Added exponential backoff to prevent thundering herd
- Preserved token format for backward compatibility

**Resolution:** Deployed Aug 31, zero 401s after 2 weeks monitoring.

---
💾 Restore: bd compact --restore bd-42
📊 Original: 2,341 bytes | Compressed: 468 bytes (80% reduction)

Total: 468 bytes (80% reduction)
```

## Implementation Plan

### Phase 1: Foundation (16 hours)
1. Schema changes (compaction columns, snapshots table)
2. Config keys
3. Candidate identification queries
4. Migration testing

### Phase 2: Core Compaction (20 hours)
5. Haiku client with prompts
6. Snapshot creation/restoration
7. Tier 1 compaction logic
8. CLI command (`bd compact`)

### Phase 3: Advanced Features (12 hours)
9. Tier 2 compaction
10. Restore functionality
11. Stats command
12. Event tracking

### Phase 4: Polish (12 hours)
13. Comprehensive tests
14. Documentation (README, COMPACTION.md)
15. Examples and workflows

**Total MVP:** ~60 hours

### Optional Enhancements (Phase 5+)
- Auto-compaction triggers
- Git commit counting
- Local model support (Ollama)
- Custom prompt templates

## Architecture

```
Issue (closed 45 days ago)
    ↓
Eligibility Check
    ↓ (passes: all deps closed, >30 days)
Create Snapshot
    ↓
Call Haiku API
    ↓ (returns: structured summary)
Update Issue
    ↓
Record Event
    ↓
Export to JSONL
```

## Key Files

**New Files:**
- `internal/compact/haiku.go` - Haiku API client
- `internal/compact/compactor.go` - Core compaction logic
- `internal/storage/sqlite/compact.go` - Candidate queries
- `cmd/bd/compact.go` - CLI command

**Modified Files:**
- `internal/storage/sqlite/schema.go` - Add snapshots table
- `internal/storage/sqlite/sqlite.go` - Add migrations
- `internal/types/types.go` - Add EventCompacted
- `cmd/bd/show.go` - Display compaction status

## Schema Changes

```sql
-- Add to issues table
ALTER TABLE issues ADD COLUMN compaction_level INTEGER DEFAULT 0;
ALTER TABLE issues ADD COLUMN compacted_at DATETIME;
ALTER TABLE issues ADD COLUMN original_size INTEGER;

-- New table
CREATE TABLE issue_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    snapshot_time DATETIME NOT NULL,
    compaction_level INTEGER NOT NULL,
    original_size INTEGER NOT NULL,
    compressed_size INTEGER NOT NULL,
    original_content TEXT NOT NULL,  -- JSON
    archived_events TEXT,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);
```

## Configuration

```sql
INSERT INTO config (key, value) VALUES
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

## Haiku Prompts

### Tier 1 (300 words)
```
Summarize this closed software issue. Preserve key decisions,
implementation approach, and outcome. Max 300 words.

Title: {{.Title}}
Type: {{.IssueType}}
Priority: {{.Priority}}

Description: {{.Description}}
Design Notes: {{.Design}}
Implementation Notes: {{.Notes}}
Acceptance Criteria: {{.AcceptanceCriteria}}

Output format:
**Summary:** [2-3 sentences: problem, solution, outcome]
**Key Decisions:** [bullet points of non-obvious choices]
**Resolution:** [how it was closed]
```

### Tier 2 (150 words)
```
Ultra-compress this old closed issue to ≤150 words.
Focus on lasting architectural impact.

Title: {{.Title}}
Original Summary: {{.Description}}

Output a single paragraph covering:
- What was built/fixed
- Why it mattered
- Lasting impact (if any)
```

## Success Metrics

- **Space:** 70-85% reduction (Tier 1), 90-95% (Tier 2)
- **Quality:** Summaries preserve essential context
- **Safety:** 100% restore success rate
- **Performance:** <100ms candidate query, ~2s per issue (Haiku)
- **Cost:** <$1.50 per 1,000 issues

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Info loss in summaries | Dry-run review, restore capability, prompt tuning |
| API rate limits | Exponential backoff, respect limits, batch sizing |
| Accidental compression | Dry-run required, snapshots permanent |
| JSONL conflicts | Compaction is deterministic, git handles well |

## Next Steps

1. **Review design:** `COMPACTION_DESIGN.md` (comprehensive spec)
2. **File issues:** `bd create -f compaction-issues.md` (16 issues)
3. **Start implementation:** Begin with schema + config (Issues 1-2)
4. **Iterate:** Build MVP (Tier 1 only), then enhance

## Questions?

- Full design: `COMPACTION_DESIGN.md`
- Issue breakdown: `compaction-issues.md`
- Reference implementation in Phase 1-4 above

---

**Ready to implement in another session!**
