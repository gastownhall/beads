# Ultrathink: Solving status/closed_at Inconsistency (bd-224)

**Date**: 2025-10-15
**Context**: Individual devs, small teams, future agent swarms, new codebase
**Problem**: Data model allows `status='open'` with `closed_at != NULL` (liminal state)

## Executive Summary

**Recommended Solution**: **Hybrid approach - Database CHECK constraint + Application enforcement**

This provides defense-in-depth perfect for agent swarms while keeping the model simple for individual developers.

---

## Current State Analysis

### Where closed_at is Used

1. **GetIssue**: Returns it to callers
2. **CloseIssue**: Sets `closed_at = now()` when closing
3. **SearchIssues**: Includes in results
4. **GetStatistics** ⚠️ **CRITICAL**:
   ```sql
   SELECT AVG((julianday(closed_at) - julianday(created_at)) * 24)
   FROM issues
   WHERE closed_at IS NOT NULL
   ```
   Uses `closed_at IS NOT NULL` NOT `status='closed'` for lead time calculation!

### Impact of Inconsistency

- **Statistics are wrong**: Issues with `status='open'` but `closed_at != NULL` pollute lead time metrics
- **User confusion**: bd ready shows "closed" issues
- **Agent workflows**: Unpredictable behavior when agents query by status vs closed_at
- **Data integrity**: Can't trust the data model

### Root Cause: Import & Update Don't Manage Invariant

**Import** (cmd/bd/import.go:206-207):
```go
if _, ok := rawData["status"]; ok {
    updates["status"] = issue.Status  // ← Updates status
}
// ⚠️ Does NOT clear/set closed_at
```

**UpdateIssue** (internal/storage/sqlite/sqlite.go:509-624):
- Updates any field in the map
- Does NOT automatically manage closed_at when status changes
- Records EventClosed but doesn't enforce the invariant

**Concrete example (bd-89)**:
1. Issue closed properly: `status='closed'`, `closed_at='2025-10-15 08:13:08'`
2. JSONL had old state: `status='open'`, `closed_at='2025-10-14 02:58:22'` (inconsistent!)
3. Auto-import updated status to 'open' but left closed_at set
4. Result: Inconsistent state in database

---

## Solution Options

### Option A: Database CHECK Constraint ⭐ **RECOMMENDED FOUNDATION**

```sql
ALTER TABLE issues ADD CONSTRAINT chk_closed_at_status
  CHECK ((status = 'closed') = (closed_at IS NOT NULL));
```

**Pros:**
- ✅ Enforces invariant at database level (most robust)
- ✅ Catches bugs in ANY code path (future-proof)
- ✅ Works across all clients (CLI, MCP, future integrations)
- ✅ Simple to understand for developers
- ✅ **Perfect for agent swarms**: Can't break it with buggy code
- ✅ Prevents inconsistent exports (can't write bad data to JSONL)

**Cons:**
- ⚠️ Requires migration
- ⚠️ Need to fix existing inconsistent data first
- ⚠️ Update operations must manage closed_at (but we should do this anyway!)

**Migration complexity**: LOW - few users, can break things

### Option B: Application-Level Enforcement Only

Make UpdateIssue and Import smart about status changes.

**Pros:**
- ✅ No schema change needed
- ✅ Flexible for edge cases

**Cons:**
- ❌ Easy to forget in new code paths
- ❌ Doesn't protect against direct SQL manipulation
- ❌ Multiple places to maintain (import, update, close, etc.)
- ❌ **Bad for agent swarms**: One buggy agent breaks the model
- ❌ Still allows export of inconsistent data

**Verdict**: Not robust enough alone

### Option C: Add Explicit Reopened Support

Add `bd reopen` command that uses EventReopened and manages closed_at.

**Pros:**
- ✅ Makes reopening explicit and trackable
- ✅ EventReopened already defined (types.go:150) but unused

**Cons:**
- ⚠️ Doesn't solve the fundamental invariant problem
- ⚠️ Still need to decide: clear closed_at or keep it?
- ⚠️ More complex model if we keep historical closed_at

**Verdict**: Good addition, but doesn't solve root cause

### Option D: Remove closed_at Entirely

Make events table the single source of truth.

**Pros:**
- ✅ Simplest data model
- ✅ No invariant to maintain
- ✅ Events are authoritative

**Cons:**
- ❌ **Performance**: Lead time calculation requires JOIN + subquery
  ```sql
  SELECT AVG(
    (julianday(e.created_at) - julianday(i.created_at)) * 24
  )
  FROM issues i
  JOIN events e ON i.id = e.issue_id
  WHERE e.event_type = 'closed'
  ```
- ❌ Events could be missing/corrupted (no referential integrity on event_type)
- ❌ More complex queries throughout codebase
- ❌ **Statistics would be slower** (critical for dashboard UIs)

**Verdict**: Too much complexity/performance cost for the benefit

---

## Recommended Solution: **Hybrid Approach**

Combine **Option A (DB constraint)** + **Application enforcement** + **Option C (reopen command)**

### Part 1: Database Constraint (Foundation)

```sql
-- Migration: First clean up existing inconsistent data
UPDATE issues
SET closed_at = NULL
WHERE status != 'closed' AND closed_at IS NOT NULL;

UPDATE issues
SET closed_at = CURRENT_TIMESTAMP
WHERE status = 'closed' AND closed_at IS NULL;

-- Add the constraint
ALTER TABLE issues ADD CONSTRAINT chk_closed_at_status
  CHECK ((status = 'closed') = (closed_at IS NOT NULL));
```

### Part 2: UpdateIssue Smart Status Management

Modify `internal/storage/sqlite/sqlite.go:509-624`:

```go
func (s *SQLiteStorage) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
    // ... existing validation ...

    // Smart closed_at management based on status changes
    if statusVal, ok := updates["status"]; ok {
        newStatus := statusVal.(string)

        if newStatus == string(types.StatusClosed) {
            // Changing to closed: ensure closed_at is set
            if _, hasClosedAt := updates["closed_at"]; !hasClosedAt {
                updates["closed_at"] = time.Now()
            }
        } else {
            // Changing from closed to something else: clear closed_at
            if oldIssue.Status == types.StatusClosed {
                updates["closed_at"] = nil  // This will set it to NULL
                eventType = types.EventReopened
            }
        }
    }

    // ... rest of existing code ...
}
```

### Part 3: Import Enforcement

Modify `cmd/bd/import.go:206-231`:

```go
if _, ok := rawData["status"]; ok {
    updates["status"] = issue.Status

    // Enforce closed_at invariant
    if issue.Status == types.StatusClosed {
        // Status is closed: ensure closed_at is set
        if issue.ClosedAt == nil {
            now := time.Now()
            updates["closed_at"] = now
        } else {
            updates["closed_at"] = *issue.ClosedAt
        }
    } else {
        // Status is not closed: ensure closed_at is NULL
        updates["closed_at"] = nil
    }
}
```

### Part 4: Add Reopen Command

Create `cmd/bd/reopen.go`:

```go
var reopenCmd = &cobra.Command{
    Use:   "reopen [id...]",
    Short: "Reopen one or more closed issues",
    Args:  cobra.MinimumNArgs(1),
    Run: func(cmd *cobra.Command, args []string) {
        ctx := context.Background()
        reason, _ := cmd.Flags().GetString("reason")
        if reason == "" {
            reason = "Reopened"
        }

        for _, id := range args {
            // Use UpdateIssue which now handles closed_at automatically
            updates := map[string]interface{}{
                "status": "open",
            }
            if err := store.UpdateIssue(ctx, id, updates, getUser()); err != nil {
                fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", id, err)
                continue
            }

            // Add comment explaining why
            if reason != "" {
                store.AddComment(ctx, id, getUser(), reason)
            }
        }

        markDirtyAndScheduleFlush()
    },
}

func init() {
    reopenCmd.Flags().StringP("reason", "r", "", "Reason for reopening")
    rootCmd.AddCommand(reopenCmd)
}
```

---

## Why This Solution Wins

### For Individual Devs & Small Teams
- **Simple mental model**: `closed_at` is set ⟺ issue is closed
- **Hard to break**: DB constraint catches mistakes
- **Explicit reopen**: `bd reopen bd-89` is clearer than `bd update bd-89 --status open`
- **No manual management**: Don't think about closed_at, it's automatic

### For Agent Swarms
- **Robust**: DB constraint prevents any agent from creating inconsistent state
- **Race-safe**: Constraint is atomic, checked at commit time
- **Self-healing**: UpdateIssue automatically fixes the relationship
- **Import-safe**: Can't import inconsistent JSONL (constraint rejects it)

### For New Codebase
- **Can break things**: Migration is easy with few users
- **Sets precedent**: Shows we value data integrity
- **Future-proof**: New features can't violate the invariant
- **Performance**: No query changes needed, closed_at stays fast

---

## Migration Plan

### Step 1: Clean Existing Data
```sql
-- Find inconsistent issues
SELECT id, status, closed_at FROM issues
WHERE (status = 'closed') != (closed_at IS NOT NULL);

-- Fix them (choose one strategy)
-- Strategy A: Trust status field
UPDATE issues SET closed_at = NULL
WHERE status != 'closed' AND closed_at IS NOT NULL;

UPDATE issues SET closed_at = CURRENT_TIMESTAMP
WHERE status = 'closed' AND closed_at IS NULL;

-- Strategy B: Trust closed_at field
UPDATE issues SET status = 'closed'
WHERE status != 'closed' AND closed_at IS NOT NULL;

UPDATE issues SET status = 'open'
WHERE status = 'closed' AND closed_at IS NULL;
```

**Recommendation**: Use Strategy A (trust status) since status is more often correct.

### Step 2: Add Constraint
```sql
-- Test first
SELECT id FROM issues
WHERE (status = 'closed') != (closed_at IS NOT NULL);
-- Should return 0 rows

-- Add constraint
ALTER TABLE issues ADD CONSTRAINT chk_closed_at_status
  CHECK ((status = 'closed') = (closed_at IS NOT NULL));
```

### Step 3: Update Code
1. Modify UpdateIssue (sqlite.go)
2. Modify Import (import.go)
3. Add reopen command
4. Add migration function to schema.go

### Step 4: Test
```bash
# Test the constraint rejects bad writes
sqlite3 .beads/bd.db "UPDATE issues SET closed_at = NULL WHERE id = 'bd-1' AND status = 'closed';"
# Should fail with constraint violation

# Test update handles it automatically
bd update bd-89 --status open
bd show bd-89 --json | jq '{status, closed_at}'
# Should show: {"status": "open", "closed_at": null}

# Test reopen
bd create "Test issue" -p 1
bd close bd-226
bd reopen bd-226 --reason "Need more work"
# Should work without errors
```

---

## Alternative Considered: Soft Close with closed_at History

Keep closed_at as "first time closed" and use events for current state.

**Why rejected**:
- More complex model (two sources of truth)
- Unclear semantics (what does closed_at mean?)
- Lead time calculation becomes ambiguous (first close? most recent?)
- Doesn't simplify the problem

---

## Conclusion

The **hybrid approach** (DB constraint + smart UpdateIssue + import enforcement + reopen command) is the best solution because:

1. **Defense in depth**: Multiple layers prevent inconsistency
2. **Hard to break**: Perfect for agent swarms
3. **Simple for users**: Automatic management of closed_at
4. **Low migration cost**: Can break things in new codebase
5. **Clear semantics**: closed_at = "currently closed" (not historical)
6. **Performance**: No query changes needed

The DB constraint is the key insight: it makes the system robust against future bugs, new code paths, and agent mistakes. Combined with smart application code, it creates a self-healing system that's hard to misuse.

This aligns perfectly with beads' goals: simple for individual devs, robust for agent swarms.
