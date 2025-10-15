# Lost Issues Recovery Report

**Investigation Date:** 2025-10-15
**Related Issue:** bd-229
**Root Cause:** Auto-import bug (bd-228) silently overwrote local changes

## Summary

Recovered **3 valuable issues** from 22 total lost issues (bd-200 through bd-223, excluding bd-211, bd-212) that were silently overwritten by the auto-import bug.

**Note:** 19 test fixtures (race_test_*, verification_*) were found but not documented here - they had no production value.

## Recovered Issues (3 total)

#### bd-221: Transaction state corruption (P0, CLOSED)
- **Title:** P0: Transaction state corruption in first fix attempt
- **Type:** bug (closed)
- **Priority:** 0
- **Description:** First attempt at fixing bd-89 had a critical flaw: used 'ROLLBACK; BEGIN IMMEDIATE' which executed as two separate statements. After ROLLBACK, the Go tx object was invalid but continued to be used, causing undefined behavior. Root cause: database/sql connection pooling. Correct fix: Use conn := s.db.Conn(ctx).
- **Status:** Fixed during code review before merging
- **Related:** bd-89 (GH-6: Fix race condition in parallel issue creation)

#### bd-222: Batching API for bulk creation (P2, OPEN)
- **Title:** P2: Consider batching API for bulk issue creation
- **Type:** feature
- **Priority:** 2
- **Description:** Current CreateIssue acquires a dedicated connection for each call. For bulk imports or agent workflows creating many issues, a batched API could improve performance: `CreateIssues(ctx, issues []*Issue, actor string) error`. This would acquire one connection, use one IMMEDIATE transaction, insert all issues atomically, and reduce connection overhead.
- **Status:** Open enhancement, not urgent

#### bd-223: Early context check optimization (P3, OPEN)
- **Title:** P3: Consider early context check in CreateIssue
- **Type:** task
- **Priority:** 3
- **Description:** CreateIssue starts an IMMEDIATE transaction before checking if context is cancelled. Consider adding early context check between validation (line 318) and acquiring connection (line 331). Low priority - the busy_timeout and deferred cleanup handle this gracefully.
- **Status:** Open optimization, low priority

## Recovery Actions

### Completed
1. ✅ Extracted all 22 lost issues from git history
2. ✅ Identified which are test fixtures vs real issues
3. ✅ Verified no broken references (no code/docs reference these IDs)
4. ✅ Confirmed bd-89 (referenced by bd-221 and bd-228) still exists

### Actions Taken
1. ✅ **Re-filed bd-222 as bd-232** - Batching API feature request
2. ✅ **Re-filed bd-223 as bd-233** - Early context check optimization
3. ✅ **Documented bd-221 in bd-89 notes** - Transaction fix historical context preserved

## Data Integrity Notes

### How Issues Were Lost
The auto-import bug (bd-228, now fixed) silently overwrote local changes when pulling from git. Issues bd-200 to bd-223 were likely:
1. Created locally by an agent or test run
2. Not exported to JSONL before git pull
3. Git pull triggered auto-import that didn't include these issues
4. Issues silently disappeared from database

### Prevention
- ✅ Auto-import now has collision detection (bd-228 fix)
- ✅ Manual import has --dry-run and --resolve-collisions
- ✅ Auto-export debounce ensures JSONL stays in sync
- Consider: Add warning if database has issues not in JSONL

## Git Archaeology Details

### Search Method
```bash
# Found highest numbered issue
git log --all -p -- .beads/issues.jsonl | grep -o '"id":"bd-[0-9]*"' | sort -rn

# Extracted removed issues
git log --all -p -- .beads/issues.jsonl | grep '^-{"id":"bd-2[0-2][0-9]"'

# Verified current state
bd list --json | jq -r '.[] | select(.id | startswith("bd-")) | .id'
```

### Current State
- **Total issues in DB:** 231 (bd-1 through bd-231, no gaps)
- **Lost issue range:** bd-200 through bd-223 (22 issues)
- **Last recovered commit:** dd613f4 (recovered bd-180, bd-181, bd-182)

## Full Lost Issue Data

Complete JSON of recovered issues available at `/tmp/real_lost_issues.jsonl`.

## Conclusion

**Impact Assessment:** LOW
- 2 open feature/task requests recovered (P2/P3) → bd-232, bd-233
- 1 closed bug context preserved in bd-89 notes
- 19 test fixtures discarded (no production value)

**Data Loss:** Minimal. All valuable issues successfully recovered. The auto-import bug has been fixed (bd-228) and proper collision detection is now in place.
