# Regression Discovery Log

This is an **exploratory log** from systematic regression testing of the
SQLite→Dolt backend migration. It documents bugs found, protocol invariants
confirmed, and test ideas for future work. Not all findings here are
actionable — some are by-design tradeoffs. The audit column tracks triage.

## Session Log

| Date | What we did | Outcome |
|------|-------------|---------|
| 2026-02-22 | Manual testing of dep tree, blocking, close guard, labels, status filtering, reparenting, concurrency, validation | Found 14 bugs, confirmed 23 protocol invariants. Wrote `discovery_test.go` (34 tests). |
| 2026-02-22 | Audit of all bugs for fix vs wontfix | 5-6 clear fix PRs, 2 need design discussion, 5-6 wontfix/by-design |
| 2026-02-22 | Code review of labels.go, schema.go, dependencies.go for BUG-5 and BUG-7 root cause | BUG-5 upgraded to INVESTIGATE (not clearly wontfix). BUG-7 downgraded to FILE ISSUE (intentionally coded upsert, needs product decision). BUG-4 upgraded to DOCS FIX (help text promises "blocked" as a status). |
| 2026-02-22 | **Phase 1-3: Snapshot harness + full parity run** | Replaced bd export with snapshot (list+show). Fixed database isolation (unique prefixes per workspace). Normalization for show-vs-export field differences. **Result: 95+ PASS, 15 FAIL (all known bugs), 10 SKIP.** |
| 2026-02-22 | **Phase 4: Ship fix PRs with tests** | BUG-2+3 already merged (PR #1992). BUG-10 PR #2014, BUG-11+12+14 PR #1994, BUG-4 PR #2017. All PRs include protocol tests. |
| 2026-02-22 | **Session 3: Candidate-only discovery (lane 3)** | Found 5 new bugs (BUG-16 through 20) + 3 code-review-only findings. External blockers, conditional-blocks, count/list discrepancy, waits-for gating, parent-child blocked consistency. Filed DECISION PRs #2025, #2026. |
| 2026-02-22 | **Session 4: Deep discovery (search, lifecycle, batch, deps)** | Found 7 more bugs (BUG-21 through 27). Update bypasses close guard, reopen superseded corruption, defer past date invisible, wisp sort order, conditional-blocks cycle, epic wisp children. 2 new protocol tests. |
| 2026-02-22 | **Session 5: Filter, flag interaction, migration seams** | Found 4 more bugs (BUG-28 through 31). Dead label-pattern filter, claim+status overwrite, --ready overrides --status, assignee empty string. Code review: pull doesn't check merge conflicts, schema migration non-transactional, import drops deps/comments silently. |
| 2026-02-22 | **Session 6: Routing, validation, sort, edge cases** | Found 10 more bugs (BUG-32 through 42) + 2 protocol tests (BUG-35, 39). Stale negative days, sort unknown field, reparent cycle, reversed ranges, negative limit, whitespace title, config ambiguity, dep rm false positive. Code review: createInRig skips prefix validation, same-prefix rig ambiguity, batch import no UTC. |
| 2026-02-22 | **Session 7: State corruption, filter conflicts, hierarchy** | Found 4 more bugs (BUG-43 through 46) + 5 protocol tests (BUG-47 through 51). Deferred without date, comma status, assignee conflict, child of closed parent. Documented: custom dep types, in_progress vs claim, --all filter, empty type rejection, show JSON array. |

## Audit Summary

| Bug | Verdict | Status | PR/Issue |
|-----|---------|--------|----------|
| BUG-1 | WONTFIX | RESOLVED | Snapshot harness (PR #2012) |
| BUG-2 | **FIX PR** | **MERGED** | PR #1992 |
| BUG-3 | **FIX PR** | **MERGED** | PR #1992 |
| BUG-4 | **DOCS FIX** | **PR OPEN** | PR #2017 |
| BUG-5 | **INVESTIGATE** | OPEN | — |
| BUG-6 | WONTFIX | RESOLVED | Unique prefix per workspace |
| BUG-7 | **DECISION PR** | **PR OPEN** | PR #1999 |
| BUG-8 | **DECISION PR** | **PR OPEN** | PR #2001 |
| BUG-9 | WONTFIX | RESOLVED | — |
| BUG-10 | **FIX PR** | **PR OPEN** | PR #2014 |
| BUG-11 | **FIX PR** | **PR OPEN** | PR #1994 |
| BUG-12 | **FIX PR** | **PR OPEN** | PR #1994 |
| BUG-13 | **DECISION PR** | **PR OPEN** | PR #2000 |
| BUG-14 | **FIX PR** | **PR OPEN** | PR #1994 |
| BUG-15 | **INVESTIGATE** | OPEN | — |
| BUG-16 | **DECISION PR** | **PR OPEN** | PR #2025 |
| BUG-17 | **DECISION PR** | **PR OPEN** | PR #2026 |
| BUG-18 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-19 | **INVESTIGATE** | OPEN (not PR'd yet) | — |
| BUG-20 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-21 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-22 | **DECISION** | OPEN (not PR'd yet) | — |
| BUG-23 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-24 | **BUG** (code review) | OPEN (not PR'd yet) | — |
| BUG-25 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-26 | **DECISION** | OPEN (not PR'd yet) | — |
| BUG-27 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-28 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-29 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-30 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-31 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-32 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-33 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-34 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-35 | **PROTOCOL** | PASS (correct behavior) | — |
| BUG-36 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-37 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-38 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-39 | **PROTOCOL** | PASS (correct behavior) | — |
| BUG-40 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-41 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-42 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-43 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-44 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-45 | **BUG** | OPEN (not PR'd yet) | — |
| BUG-46 | **DECISION** | OPEN (not PR'd yet) | — |
| BUG-47 | **PROTOCOL** | PASS (by design) | — |
| BUG-48 | **PROTOCOL** | PASS (docs behavior) | — |
| BUG-49 | **PROTOCOL** | PASS (correct) | — |
| BUG-50 | **PROTOCOL** | PASS (correct) | — |
| BUG-51 | **PROTOCOL** | PASS (correct) | — |

### Shipped fix PRs (all include protocol tests)

1. **BUG-2+3**: dep tree ParentID + ready annotation — PR #1992 **MERGED**
2. **BUG-10**: exit codes for close guard / claim failures — PR #2014
3. **BUG-11+12+14**: input validation gaps (status, title, label) — PR #1994
4. **BUG-4**: clarify --status flag vs bd blocked — PR #2017

### DECISION PRs (need PO review)

5. **BUG-7**: dep add type overwrite — PR #1999
6. **BUG-8**: reparent dual parent — PR #2001
7. **BUG-13**: reopen clears defer_until — PR #2000
8. **BUG-16**: external blockers ignored by readiness — PR #2025
9. **BUG-17**: conditional-blocks not evaluated — PR #2026

### Not yet PR'd (holding for maintainer feedback)

10. **BUG-18**: count vs list default filter discrepancy
11. **BUG-19**: waits-for bare dep doesn't block (needs investigation)
12. **BUG-20**: children of blocked parent not in bd blocked
13. **BUG-21**: update --status closed bypasses close guard
14. **BUG-22**: reopen superseded = semantic corruption (DECISION)
15. **BUG-23**: defer with past date creates invisible issue
16. **BUG-24**: wisp sort order loss (code review, no test)
17. **BUG-25**: conditional-blocks cycle undetected
18. **BUG-26**: reopen superseded = semantic corruption (DECISION)
19. **BUG-27**: defer with past date creates invisible issue
20. **BUG-28**: --label-pattern filter is dead code (HIGH)
21. **BUG-29**: --claim + --status overwrite conflict
22. **BUG-30**: --ready silently overrides --status
23. **BUG-31**: --assignee "" becomes no-filter
24. **BUG-32**: stale --days negative inverts logic (HIGH)
25. **BUG-33**: list --sort unknown field silent no-op
26. **BUG-34**: reparent parent to child creates cycle
27. **BUG-35**: overdue comparison edge case (PROTOCOL — works correctly)
28. **BUG-36**: --priority-min > --priority-max silently returns empty
29. **BUG-37**: --created-after > --created-before silently returns empty
30. **BUG-38**: bd list -n -1 accepted as unlimited (no validation)
31. **BUG-39**: bd duplicate on closed issue succeeds (PROTOCOL — correct)
32. **BUG-40**: update --title whitespace-only accepted (extends BUG-12)
33. **BUG-41**: config set "" then config get shows "(not set)" — ambiguous
34. **BUG-42**: dep rm nonexistent says "Removed dependency" — false positive
35. **BUG-43**: update --status deferred without --defer = permanently deferred
36. **BUG-44**: list --status "open,closed" silently returns empty
37. **BUG-45**: list --assignee alice --no-assignee contradictory, returns empty
38. **BUG-46**: create --parent of closed issue succeeds (DECISION)
39. **BUG-47**: dep add --type custom accepted by design (PROTOCOL)
40. **BUG-48**: --status in_progress doesn't auto-assign (PROTOCOL — documents difference vs --claim)
41. **BUG-49**: list --all includes closed (PROTOCOL — correct)
42. **BUG-50**: create --type "" rejected (PROTOCOL — correct)
43. **BUG-51**: show --json always returns array (PROTOCOL — correct)

### Investigate further

8. **BUG-5**: concurrent label race — need to determine if Dolt working-set merge is the root cause or if beads-level batching/serialization would fix it

---

## CONFIRMED BUGS

### BUG-1: `bd export` command removed from main — **RESOLVED in test harness**

**Severity: HIGH** — Broke entire regression test suite
**Affected:** `tests/regression/` — all 85 tests relied on `compareExports()` → `bd export`
**Status:** ✅ RESOLVED — Snapshot harness (`fix/regression-snapshot-harness` branch)

The `bd export` command was removed during the JSONL→Dolt-native refactor
(commit 1e1568fa). The test harness now uses `snapshot()` (list+show) instead.
The `export()` method translates old flags and delegates to `snapshot()`.
`bd import` was also removed — tests that relied on it are now SKIP.

---

### BUG-2: `dep tree` shows no children — ParentID never set (GH#1954)

**Severity: HIGH** — Core feature completely broken
**File:** `internal/storage/dolt/dependencies.go:646-649`
**Root cause:** `buildDependencyTree()` creates `TreeNode` without setting `ParentID`:

```go
node := &types.TreeNode{
    Issue: *issue,
    Depth: depth,  // ← Depth is set correctly
    // ParentID is NEVER set ← BUG
}
```

The `renderTree()` function at `cmd/bd/dep.go:721-729` builds a children map
keyed by `ParentID`. Since `ParentID` is always empty, all children go into
`children[""]` instead of `children[rootID]`. Root's children lookup returns empty.

**Fix:** Pass parent ID into recursive `buildDependencyTree` and set `node.ParentID`:

```go
func (s *DoltStore) buildDependencyTree(ctx context.Context, issueID string,
    depth, maxDepth int, reverse bool, visited map[string]bool,
    parentID string) ([]*types.TreeNode, error) {
    // ...
    node := &types.TreeNode{
        Issue:    *issue,
        Depth:    depth,
        ParentID: parentID,  // ← FIX
    }
    // ...
    for _, childID := range childIDs {
        children, err := s.buildDependencyTree(ctx, childID, depth+1,
            maxDepth, reverse, visited, issueID)  // ← pass issueID as parent
```

---

### BUG-3: `dep tree` shows `[READY]` for blocked root issue

**Severity: MEDIUM**
**File:** `cmd/bd/dep.go:835`

```go
if node.Status == types.StatusOpen && node.Depth == 0 {
    line += " " + ui.PassStyle.Bold(true).Render("[READY]")
}
```

The ready check only looks at `status == open && depth == 0`. It doesn't check
whether the issue has open blocking dependencies. A blocked issue at depth 0
(the root of a "down" tree) shows `[READY]` when it should show `[BLOCKED]`.

**Fix:** Check for open blocking dependencies before showing `[READY]`. Either
query the store or compute from the tree data.

---

### BUG-4: `list --status blocked` and `count --status blocked` return empty

**Severity: MEDIUM** — Documented status value doesn't work
**Affects:** `bd list --status blocked`, `bd count --status blocked`, `bd query "status=blocked"`

The help text for `list` says: `--status string  Filter by status (open, in_progress, blocked, deferred, closed)`

But "blocked" is a computed status derived from dependency relationships, never
stored in the `issues.status` column (which stays "open"). So:
- `bd blocked` → 4 issues ✓
- `bd list --status blocked` → 0 issues ✗
- `bd count --status blocked` → 0 ✗

**Fix options:**
1. Materialize blocked status: When a blocking dep is added, update status to "blocked"
2. Compute on query: In the list/count SQL, join with dependencies to detect blocked
3. Remove "blocked" from the documented status values and point users to `bd blocked`

---

### BUG-5: Concurrent label operations produce race conditions

**Severity: MEDIUM** — Data loss under concurrency
**Reproduction:**

```bash
# Parallel adds — expect 5 labels, get 0
for i in 1 2 3 4 5; do
  bd label add <id> "stress-$i" &
done
wait
bd show <id> --json  # labels: []
```

Sequential label adds work perfectly (5/5). Parallel adds produce 0 labels
visible immediately. After subsequent operations, some labels eventually appear.

**Root cause:** Likely a lost-update race in the Dolt server. Each concurrent
`label add` reads the current label set, adds its label, writes back. If two
writers read the same state, the last writer wins and the other's label is lost.

**Fix:** Use row-level INSERT into a labels junction table instead of
read-modify-write on a labels array/column. Or use SELECT FOR UPDATE / SERIALIZABLE
transactions.

---

### BUG-6: Workspace data isolation with shared Dolt server — **RESOLVED in test harness**

**Severity: LOW for end users, HIGH for test infrastructure**
**Status:** ✅ RESOLVED — Unique prefix per workspace

All `bd init --prefix test` workspaces on the same Dolt server (127.0.0.1:3307)
share the same `beads_test` database. This is by design for collaborative use.

The regression harness now uses unique prefixes per workspace (FNV hash of temp
dir path), creating separate `beads_t<hash>` databases. Each test workspace
is fully isolated.

---

### BUG-7: `dep add` silently overwrites when changing dep type on same pair

**Severity: HIGH** — Silent data loss of blocking relationships
**Reproduction:**

```bash
bd dep add A B --type blocks    # ✓ Added dependency
bd dep add A B --type caused-by # ✓ Added dependency  (SILENTLY REPLACES blocks)
# DB now only has caused-by — blocks relationship is LOST
# A is no longer blocked!
```

The `dependencies` table has a unique constraint on `(issue_id, depends_on_id)`
without including `type`. Adding a second dep type on the same pair does an
upsert, replacing the existing type. Both operations report success.

**Impact:** A user who adds `caused-by` to an already-blocked pair silently
removes the blocking relationship. The issue becomes unblocked without warning.

**Fix:** Either:
1. Make the unique key `(issue_id, depends_on_id, type)` to allow multiple dep types
2. Reject the second `dep add` with an error: "dependency already exists with type X"
3. Warn the user: "changing dep type from X to Y"

---

### BUG-8: Reparented child appears under BOTH old and new parent

**Severity: MEDIUM** — Confusing behavior after reparenting
**File:** `internal/storage/dolt/queries.go:211`
**Root cause:** Parent filter uses `OR id LIKE CONCAT(?, '.%')` in addition to
dependency lookup. After `bd create --title X --parent P1` creates `P1.1`,
reparenting with `bd update P1.1 --parent P2` correctly updates the
parent-child dep to P2, but the ID `P1.1` still matches `P1.%` via LIKE.

```sql
(id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?)
 OR id LIKE CONCAT(?, '.%'))
```

**Impact:** `bd children P1` shows `P1.1` even after reparenting to P2.
`bd children P2` also correctly shows it. The child appears under BOTH parents.

**Fix options:**
1. After reparent, rename the issue ID to match new parent (e.g., `P1.1` → `P2.1`)
2. Remove the LIKE clause from parent filtering (rely solely on dependency table)
3. Add EXCEPT clause: `AND id NOT IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id != ?)`

---

### BUG-9: `list --ready` includes blocked issues (documented but confusing)

**Severity: LOW** (documented in help text)
**File:** `bd list --ready` help says "Note: 'bd list --ready' is NOT equivalent"

`bd list --ready -n 0` returns 34 issues including blocked ones.
`bd ready -n 0` returns 29 truly ready issues (excludes blocked).

The discrepancy of 5 issues = exactly the issues with open `blocks` dependencies.
The help text documents this, but the `--ready` flag name is misleading.

---

### BUG-10: Commands exit 0 on soft failures (close guard, claim, etc.)

**Severity: MEDIUM** — Breaks scripting and automation
**Affects:** `bd close` (close guard), `bd update --claim` (already claimed), likely others
**Files:** `cmd/bd/close.go:117`, `cmd/bd/update.go:278`

When close guard prevents closing a blocked issue, the command prints a message
to stderr ("cannot close X: blocked by open issues") but exits with code 0.
Similarly, `update --claim` on an already-claimed issue prints "already claimed"
to stderr but exits 0.

The pattern is: `fmt.Fprintf(os.Stderr, ...) + continue` in a loop, with no
tracking of whether any operations actually succeeded. When the loop finishes,
the command exits 0 regardless.

**Impact:** Scripts and CI/CD pipelines cannot detect these failures via exit code.
They must parse stderr text instead, which is fragile.

**Fix:** Track `errorCount` and call `os.Exit(1)` if `errorCount > 0` and
`closedCount == 0` at end of the command.

---

### BUG-11: `bd update --status` accepts arbitrary values

**Severity: MEDIUM** — Data integrity issue
**File:** `cmd/bd/update.go`

`bd update X --status "bogus"` succeeds and stores "bogus" as the status.
Valid statuses should be: open, in_progress, closed, deferred.
The `--type` flag correctly validates against a whitelist, but `--status` does not.

**Impact:** Invalid statuses are stored in the DB. Issues with invalid status
won't appear in any filtered list (they're not open, not closed, not deferred).

**Fix:** Add status validation in update command, same pattern as type validation.

---

### BUG-12: `bd update --title ""` accepts empty title

**Severity: LOW** — Data quality issue
**File:** `cmd/bd/update.go`

`bd create --title ""` correctly fails with "title required".
`bd update X --title ""` succeeds and stores an empty title.
Validation is inconsistent between create and update.

**Fix:** Add empty-title check in update command.

---

### BUG-13: Reopen of closed+deferred issue creates limbo state

**Severity: MEDIUM** — Issue becomes invisible
**Reproduction:**

```bash
bd defer X --until 2099-12-31   # status=deferred
bd close X                      # status=closed, defer_until preserved
bd reopen X                     # status=open, defer_until STILL SET
```

After reopening, the issue has status "open" but defer_until is still set.
- Not in `bd ready` (excluded by defer_until check) ✓
- Not in `bd list --status deferred` (status is "open", not "deferred") ✗
- Appears in `bd list --status open` but won't show in ready ✗

The issue is effectively invisible to normal workflows.

**Fix options:**
1. `reopen` should clear defer_until when setting status to "open"
2. `reopen` should restore "deferred" status if defer_until is still in the future
3. `close` should clear defer_until when closing a deferred issue

---

### BUG-14: `bd label add` accepts empty string label

**Severity: LOW** — Data quality issue

`bd label add X ""` succeeds and stores an empty string as a label.
This creates invisible/confusing entries in the label list.

**Fix:** Validate label is non-empty before inserting.

---

### BUG-15: Labels missing from dependent sub-objects in `bd show --json` (NEW — parity run)

**Severity: LOW** — Cosmetic data difference in nested view
**Discovered:** Phase 3 parity run, TestUpdateDoesNotClobberRelationalData
**Reproduction:**

```bash
bd create --title "Data-rich issue" --type feature --priority 0
bd label add <id> important
bd label add <id> v2
bd dep add <other> <id> --type blocks
bd show <other> --json
# Dependent sub-object for <id> is missing "labels" field on Dolt
# Baseline (v0.49.6 SQLite) includes labels in the dependent view
```

The dependent/dependency sub-objects returned by `bd show --json` on the Dolt
backend don't include the `labels` array, even though the baseline does. This
affects only the nested view — `bd show <id> --json` for the issue itself
correctly shows labels.

**Triage: INVESTIGATE** — Need to check if this is a deliberate field selection
difference in the Dolt backend's show query vs the SQLite backend.

---

### BUG-16: External blockers silently ignored by `computeBlockedIDs()` (NEW — session 3)

**Severity: HIGH** — Silent loss of blocking semantics
**Discovered:** Lane 3 candidate-only discovery, code review + test
**File:** `internal/storage/dolt/queries.go:902-915`
**PR:** #2025 (DECISION)
**Test:** `TestDiscovery_ExternalBlockerIgnoredByReady`

`computeBlockedIDs()` only marks issues blocked if BOTH issue AND blocker are
in the local `activeIDs` map. External blockers (`external:project:capability`)
are never in `activeIDs`, so issues with external blocking deps silently appear
in `bd ready` and can be closed without close guard intervention.

**Impact:** Cross-project blocking relationships are completely ignored for
readiness and close guard. An issue that should be blocked by an external
dependency is treated as unblocked.

**DECISION:** Should external blockers gate readiness or remain advisory-only?

---

### BUG-17: `conditional-blocks` deps not evaluated in readiness (NEW — session 3)

**Severity: MEDIUM** — Inconsistency between type system and query engine
**Discovered:** Lane 3 candidate-only discovery, code review + test
**File:** `internal/storage/dolt/queries.go:885-888`
**PR:** #2026 (DECISION)
**Test:** `TestDiscovery_ConditionalBlocksNotEvaluated`

`types.AffectsReadyWork()` returns true for `conditional-blocks`, but
`computeBlockedIDs()` SQL only queries `WHERE type IN ('blocks', 'waits-for')`.
`conditional-blocks` is never evaluated, so issues that should be conditionally
blocked appear as ready.

**DECISION:** Should conditional-blocks gate readiness while precondition is open?

---

### BUG-18: `bd count` vs `bd list` disagree on default filtering (NEW — session 3)

**Severity: LOW-MEDIUM** — Silent discrepancy between related commands
**Discovered:** Lane 3 candidate-only discovery, code review + test
**File:** `cmd/bd/count.go:106-110` vs `cmd/bd/list.go:410-412`
**Test:** `TestDiscovery_CountVsListDefaultFilter`

`bd count` (no flags) counts ALL issues including closed.
`bd list` (no flags) excludes closed issues by default.
Running `bd count` and `bd list -n 0 --json | jq length` gives different numbers.

**Root cause:** `count.go` doesn't apply `ExcludeStatus` for closed issues,
while `list.go:410` does: `filter.ExcludeStatus = []types.Status{types.StatusClosed}`.

---

### BUG-19: `waits-for` dep doesn't block readiness despite AffectsReadyWork() (NEW — session 3)

**Severity: MEDIUM** — Type system says blocking, query engine disagrees
**Discovered:** Lane 3 candidate-only discovery, test
**File:** `internal/storage/dolt/queries.go:916-932`
**Test:** `TestDiscovery_WaitsForBlocksReadiness`

`waits-for` IS included in the `computeBlockedIDs()` SQL, but a bare `waits-for`
dep (created via `bd dep add X Y --type waits-for` without gate metadata) does
NOT block readiness. The issue appears in `bd ready` and is not in `bd blocked`.

Likely cause: the waits-for processing path in `computeBlockedIDs()` (lines
916-932) requires specific gate metadata to evaluate. A bare waits-for dep
without metadata may be silently skipped.

**Triage: INVESTIGATE** — need to determine if bare waits-for is expected to not
block (gate metadata required) or if this is a bug.

---

### BUG-20: Children of blocked parent not in `bd blocked` (NEW — session 3)

**Severity: LOW-MEDIUM** — Inconsistency between ready and blocked commands
**Discovered:** Lane 3 candidate-only discovery, test
**Test:** `TestDiscovery_ParentBlockedChildrenConsistency`

`computeBlockedIDs()` correctly propagates blocking to children of blocked
parents for `bd ready` (children excluded). But `bd blocked` does NOT list
these transitively-blocked children. This creates an inconsistency: the child
is not in ready, not in blocked — invisible to the user.

**Impact:** User runs `bd ready` → child missing. Runs `bd blocked` → child
not there either. Has no way to discover why the child isn't showing in ready.

---

### BUG-21: `bd update --status closed` bypasses close guard (NEW — session 4)

**Severity: HIGH** — Silent bypass of safety mechanism
**Discovered:** Session 4 deep discovery, test
**File:** `cmd/bd/update.go` (no blocker check) vs `cmd/bd/close.go:109-119`
**Test:** `TestDiscovery_UpdateStatusClosedBypassesCloseGuard`

`bd close X` checks for open blockers and rejects the close. `bd update X
--status closed` does NOT check for blockers — it sets status directly,
bypassing the close guard, gate checks, and close hooks. It also leaves
`close_reason` empty (losing audit trail).

**Impact:** Scripts or agents using `bd update --status closed` can close
blocked issues silently, violating the blocking contract.

---

### BUG-22: Reopening superseded issue creates semantic corruption (NEW — session 4)

**Severity: MEDIUM** — Semantically incoherent state
**Discovered:** Session 4 deep discovery, test
**Test:** `TestDiscovery_ReopenSupersededSemanticCorruption`

`bd supersede A --with B` creates a `supersedes` dep and closes A. `bd reopen A`
sets status to open but does NOT remove the supersedes dep. Result: A is "open
but superseded by B" — a contradictory state. The `supersedes` dep is non-blocking,
so A appears in `bd ready` as actionable work despite being superseded.

**DECISION:** Should reopen remove supersedes/duplicates deps, or should reopen
be rejected for superseded/duplicated issues?

---

### BUG-23: Defer with past date creates invisible issue (NEW — session 4)

**Severity: MEDIUM** — Issue becomes invisible to all workflows
**Discovered:** Session 4 deep discovery, test
**File:** `cmd/bd/defer.go:37-44` (no past-date validation)
**Test:** `TestDiscovery_DeferPastDateInvisible`

`bd defer X --until 2020-01-01` sets status=deferred and defer_until to a past
date. Nothing transitions status back to open when defer_until passes. The issue
is not in `bd ready` (status is deferred, not open) and not in any other
actionable view. Note: `bd update --defer` warns about past dates but
`bd defer` does NOT.

**Impact:** The user thinks "it will reappear after 2020-01-01" but it never
does. The issue is silently lost.

---

### BUG-24: `scanWispIDs` loses SQL sort order (NEW — session 4, code review)

**Severity: MEDIUM** — Silent sort order violation
**Discovered:** Session 4 deep discovery, code review
**File:** `internal/storage/dolt/wisps.go:719-738`

`searchWisps()` uses `ORDER BY priority ASC, created_at DESC` but `scanWispIDs()`
calls `getWispsByIDs()` which issues `SELECT ... WHERE id IN (...)` with no
ORDER BY and no order-restoration logic. Compare with `scanIssueIDs` in
`dependencies.go:869-883` which has explicit order restoration (GH#1880 fix).

Additionally, `SearchIssues()` at `queries.go:321-337` appends wisp results
after Dolt results without re-sorting the merged list. A P0 wisp appears after
a P4 permanent issue.

**Impact:** Any query returning both permanent and ephemeral issues has broken
sort order. Hard to notice with small wisp counts.

---

### BUG-25: Cycle detection doesn't catch conditional-blocks cycles (NEW — session 4)

**Severity: MEDIUM** — Undetected dependency cycle
**Discovered:** Session 4 deep discovery, test
**File:** `internal/storage/dolt/dependencies.go:54` (only checks `blocks`)
**Test:** `TestDiscovery_ConditionalBlocksCycleUndetected`

Cycle detection at `AddDependency` only runs for `type == blocks`. `DetectCycles`
also only follows `blocks` edges. A cycle like A blocks B, B conditional-blocks A
is not detected — `bd dep cycles` reports "No dependency cycles detected."

Since `conditional-blocks` is declared as `AffectsReadyWork()`, cycles through
it could create deadlocks that are never detected.

---

### BUG-26: Reopening superseded issue = semantic corruption (NEW — session 4)

**Severity: MEDIUM** — Semantically incoherent state
**Discovered:** Session 4 deep discovery, test
**Test:** `TestDiscovery_ReopenSupersededSemanticCorruption`

`bd supersede A --with B` creates a `supersedes` dep and closes A. `bd reopen A`
sets status to open but does NOT remove the supersedes dep. Result: A is "open
but superseded by B." The supersedes dep is non-blocking, so A appears in
`bd ready` as actionable work despite being semantically obsolete.

Same issue applies to `bd duplicate` — reopening a duplicate creates
"open but duplicate-of" state.

**DECISION:** Should reopen remove supersedes/duplicates deps, or should
reopen be rejected for superseded/duplicated issues?

---

### BUG-27: Defer with past date creates invisible issue (NEW — session 4)

**Severity: MEDIUM** — Issue silently lost
**Discovered:** Session 4 deep discovery, test
**File:** `cmd/bd/defer.go:37-44` (no past-date validation)
**Test:** `TestDiscovery_DeferPastDateInvisible`

`bd defer X --until 2020-01-01` sets status=deferred and defer_until to a past
date. Nothing transitions status back to open when defer_until passes. The issue
is not in `bd ready` (status is "deferred", not "open") and IS in
`list --status deferred`, but the user expects it to reappear automatically.
Note: `bd update --defer` warns about past dates but `bd defer` does NOT.

---

### BUG-28: `--label-pattern` and `--label-regex` filters are dead code (NEW — session 5)

**Severity: HIGH** — Complete silent filter failure
**Discovered:** Session 5 deep discovery, code review + test
**File:** `cmd/bd/list.go:436-441` (sets filter) vs `internal/storage/dolt/queries.go` (never reads it)
**Test:** `TestDiscovery_LabelPatternFilterDeadCode`

`bd list --label-pattern "tech-*"` sets `filter.LabelPattern` in the IssueFilter
struct, but `SearchIssues()` in queries.go NEVER reads or processes `LabelPattern`
or `LabelRegex`. The SQL query builder completely ignores these fields. The user
gets unfiltered results while believing they filtered by label glob/regex.

---

### BUG-29: `--claim` + `--status` flag overwrite conflict (NEW — session 5)

**Severity: MEDIUM** — Silent contradictory state
**Discovered:** Session 5 deep discovery, test
**File:** `cmd/bd/update.go:276-306` (sequential non-transactional ops)
**Test:** `TestDiscovery_ClaimThenStatusOverwrite`

`bd update X --claim --status open` first calls `ClaimIssue` (sets status=in_progress),
then calls `UpdateIssue` with status=open — silently overwriting the claim. The
user sees "Updated" with no warning about the contradictory flags.

---

### BUG-30: `--ready` silently overrides `--status` on bd list (NEW — session 5)

**Severity: MEDIUM** — Silent filter override
**Discovered:** Session 5 deep discovery, test
**File:** `cmd/bd/list.go:401-408` (if-else precedence)
**Test:** `TestDiscovery_ListReadyOverridesStatusFlag`

`bd list --status closed --ready` silently discards `--status closed` because
`--ready` takes precedence in the if-else chain. Returns open issues instead of
closed — completely wrong results with no warning.

---

### BUG-31: `--assignee ""` silently becomes no-filter (NEW — session 5)

**Severity: MEDIUM** — Silent filter bypass
**Discovered:** Session 5 deep discovery, test
**File:** `cmd/bd/list.go:423-425` (empty string check)
**Test:** `TestDiscovery_AssigneeEmptyStringVsNoAssignee`

`bd list --assignee ""` fails the `!= ""` check, so the assignee filter is never
set. Returns ALL issues instead of unassigned ones. Meanwhile `--no-assignee`
correctly filters. A user expecting empty string to mean "unassigned" gets
silently wrong results.

---

### BUG-32: `bd stale --days -1` silently inverts staleness logic (NEW — session 6)

**Severity: HIGH** — Completely wrong results, silently
**Discovered:** Session 6 discovery, test
**File:** `cmd/bd/stale.go:22,71` (no validation) + `internal/storage/dolt/queries.go:767`
**Test:** `TestDiscovery_StaleNegativeDaysSilentlyInverts`

`bd stale --days -1` is accepted without error. The cutoff computation at
`queries.go:767` uses `time.Now().UTC().AddDate(0, 0, -filter.Days)`. With
`Days=-1`, this becomes `AddDate(0,0,1)` = tomorrow. All issues updated before
tomorrow (i.e., everything) appear as "stale." The user gets all issues returned
while expecting "issues not updated in the last day."

---

### BUG-33: `bd list --sort unknown_field` silently ignored (NEW — session 6)

**Severity: MEDIUM** — Silent degradation of sort behavior
**Discovered:** Session 6 discovery, test
**File:** `cmd/bd/list.go:238-240` (default case in sort switch)
**Test:** `TestDiscovery_ListSortUnknownFieldSilentNoOp`

`bd list --sort nonexistent_field` succeeds without error. The sort comparator
at `list.go:238-240` has a `default` case that returns 0 (all items compare
equal), effectively disabling sorting. The user believes results are sorted by
their specified field but gets arbitrary ordering.

---

### BUG-34: Reparent parent to child creates parent-child cycle (NEW — session 6)

**Severity: HIGH** — Structural corruption of issue hierarchy
**Discovered:** Session 6 discovery, test
**File:** `cmd/bd/update.go:328-342` (reparent logic, no cycle check)
**Test:** `TestDiscovery_ReparentCreatesParentChildCycle`

`bd update parent --parent child` (where child is already a child of parent)
succeeds without validation. The reparent logic updates the parent-child
dependency but does NOT check if the new parent is a descendant, creating a
mutual parent-child cycle. Both issues claim the other as parent.

This is different from BUG-25 (conditional-blocks cycle) — this is a
parent-child hierarchy corruption that could cause infinite loops in
tree-walking code.

---

### BUG-35: `bd list --overdue` timezone edge cases (NEW — session 6)

**Severity: LOW** — Documenting correct behavior for reference
**Discovered:** Session 6 discovery, test
**File:** `internal/storage/dolt/queries.go:237-239`
**Test:** `TestDiscovery_OverdueComparisonEdgeCase`

`bd list --overdue` compares `due_at` against `time.Now().UTC()`. The test
verifies basic overdue semantics (past due included, future due excluded, no
due date excluded). More investigation needed to determine if timezone mismatch
occurs at UTC boundary edge cases.

**Triage: INVESTIGATE** — basic overdue works, but UTC midnight edge cases
need more thorough testing with controlled time.

---

### BUG-36: `--priority-min 4 --priority-max 0` silently returns empty (NEW — session 6)

**Severity: MEDIUM** — Silent wrong results from reversed range
**Discovered:** Session 6 discovery, test
**File:** `cmd/bd/list.go:522-535` (independent validation, no min<=max check)
**Test:** `TestDiscovery_PriorityMinMaxReversedSilentEmpty`

`bd list --priority-min 4 --priority-max 0` produces SQL `priority >= 4 AND
priority <= 0` which is always false. Returns empty array with no error. Each
bound is validated independently (0-4 range) but the pair is never checked.

---

### BUG-37: `--created-after` > `--created-before` silently returns empty (NEW — session 6)

**Severity: MEDIUM** — Silent wrong results from reversed date range
**Discovered:** Session 6 discovery, test
**File:** `cmd/bd/list.go:466-508` (independent parsing, no range check)
**Test:** `TestDiscovery_DateRangeReversedSilentEmpty`

`bd list --created-after 2099-12-31 --created-before 2020-01-01` produces SQL
`created_at >= 2099 AND created_at <= 2020` which is always false. Returns
empty with no error.

---

### BUG-38: `bd list -n -1` silently accepted as unlimited (NEW — session 6)

**Severity: LOW** — Negative limit not validated
**Discovered:** Session 6 discovery, test
**File:** `cmd/bd/list.go:666-668` (`effectiveLimit > 0` check)
**Test:** `TestDiscovery_NegativeLimitNotRejected`

`bd list -n -1` is silently accepted and acts as unlimited (same as `-n 0`).
The check `effectiveLimit > 0` lets negative values pass through. Should
either reject negative values or document that negative means unlimited.

---

### BUG-39: `bd duplicate` on already-closed issue succeeds (NEW — session 6)

**Severity: LOW** — Documenting correct behavior
**Discovered:** Session 6 discovery, test
**Test:** `TestDiscovery_DuplicateAlreadyClosedSucceeds`

`bd duplicate <closed-id> --of <original>` succeeds even when the duplicate
is already closed. The status update is idempotent and the duplicate-of dep
is correctly added. This is reasonable behavior — the dep link is what matters,
not the status transition. Classified as PROTOCOL (correct behavior).

---

### BUG-40: `bd update --title "   "` accepts whitespace-only title (NEW — session 6)

**Severity: LOW** — Data quality issue (extension of BUG-12)
**Discovered:** Session 6 discovery, test
**File:** `cmd/bd/update.go:66-68` (no whitespace validation)
**Test:** `TestDiscovery_WhitespaceOnlyTitleAccepted`

`bd update X --title "   "` succeeds and stores a whitespace-only title. The
issue becomes effectively untitled. `bd create` rejects empty titles but
`bd update` doesn't validate for whitespace-only content.

---

### BUG-41: `bd config get` shows "(not set)" for empty-string values (NEW — session 6)

**Severity: LOW** — UX ambiguity
**Discovered:** Session 6 discovery, test
**File:** `cmd/bd/config.go:207` (empty string check)
**Test:** `TestDiscovery_ConfigEmptyValueAmbiguous`

After `bd config set key ""`, `bd config get key` displays "(not set)" — the
same as for a key that was never set. JSON output correctly distinguishes:
`{"key": "test.key", "value": ""}` vs `{"key": "test.key", "value": null}`.
But human-readable output is ambiguous.

---

### BUG-42: `bd dep rm` on nonexistent dep reports success (NEW — session 6)

**Severity: LOW** — False positive confirmation
**Discovered:** Session 6 discovery, test
**File:** `internal/storage/dolt/dependencies.go:89-109` (no rows-affected check)
**Test:** `TestDiscovery_DepRmNonexistentSilentSuccess`

`bd dep rm A B` where no dep exists between A and B reports
"Removed dependency: A no longer depends on B" even though nothing was removed.
The DELETE statement succeeds with 0 rows affected but the command doesn't
check the result.

---

### BUG-43: `bd update --status deferred` without `--defer` = permanently deferred (NEW — session 7)

**Severity: MEDIUM** — State corruption, issue can't wake up
**Discovered:** Session 7 discovery, test
**File:** `cmd/bd/update.go:43-199` (status and defer are independent)
**Test:** `TestDiscovery_DeferredStatusWithoutDate`

`bd update X --status deferred` (without `--defer`) sets status=deferred but
leaves defer_until empty. The issue is excluded from bd ready (status check)
and appears in `list --status deferred`, but has no date to ever transition
back to open. User must manually `bd undefer` or `bd update --status open`.

---

### BUG-44: `bd list --status "open,closed"` silently returns empty (NEW — session 7)

**Severity: MEDIUM** — Silent wrong results
**Discovered:** Session 7 discovery, test
**File:** `cmd/bd/list.go:255,405` (GetString not GetStringSlice)
**Test:** `TestDiscovery_CommaStatusSilentlyReturnsEmpty`

`--status` is a simple string flag, not a slice. "open,closed" is treated as a
single literal status value, matching no issues. Returns empty array with no
error. Contrast with `--id` which DOES parse comma-separated values.

---

### BUG-45: `--assignee alice --no-assignee` contradictory, returns empty (NEW — session 7)

**Severity: MEDIUM** — Contradictory flags silently produce wrong results
**Discovered:** Session 7 discovery, test
**File:** `cmd/bd/list.go:423-424,514-515` (both set independently)
**Test:** `TestDiscovery_AssigneeAndNoAssigneeConflict`

Both flags are processed independently and both set on the filter struct.
The storage layer applies both constraints (assignee=alice AND no_assignee=true),
which is always false. Returns empty with no error or warning.

---

### BUG-46: `bd create --parent <closed>` succeeds (NEW — session 7)

**Severity: MEDIUM** — Creates child under dead parent
**Discovered:** Session 7 discovery, test
**File:** `cmd/bd/create.go:422-437` (only checks existence, not status)
**Test:** `TestDiscovery_CreateChildOfClosedParent`

`bd create --parent <closed-issue>` validates that the parent exists but does
NOT check if the parent is open/active. The child is created and appears in
`bd ready` even though its parent is closed.

**DECISION:** May be intentional for post-mortem documentation. Maintainer
should decide if children of closed parents are valid.

---

### BUG-47: `bd dep add --type custom` accepted by design (NEW — session 7)

**Severity: N/A** — Documenting correct behavior
**Discovered:** Session 7 discovery, test
**File:** `internal/types/types.go:715-717` (length check only)
**Test:** `TestDiscovery_DepAddInvalidTypeSilentlyAccepted`

`bd dep add A B --type "not-a-real-type"` succeeds and stores the custom dep
type. This is by design — custom dep types are supported. The only validation
is non-empty and ≤50 chars. Classified as PROTOCOL (correct behavior).

---

### Code review findings (session 7, not CLI-testable)

**`bd sync` is a deprecated no-op:** `cmd/bd/sync.go:9-37` — all flags
(--message, --dry-run, --no-push, --import, --export) are accepted but
ignored. Silently succeeds with no actual work done.

**Multiple `bd doctor --fix` checks are no-ops:** `cmd/bd/doctor_fix.go:253-315` —
"Sync Divergence", "JSONL Config", "Duplicate Issues", "Test Pollution",
"Git Conflicts", "Large Database" fixes all print messages but do nothing.

---

### Code review findings (session 6, not CLI-testable)

**`createInRig()` skips prefix validation:** `internal/rig/rig.go` — when
creating issues inside a rig, `createInRig()` skips ALL prefix validation
including the `--force` check. Rig-created issues bypass the normal validation
pipeline. HIGH severity for multi-rig setups.

**Same-prefix rig dep resolution ambiguity:** When two rigs share the same
prefix, `dep add` may silently resolve to the wrong local issue instead of the
intended cross-rig target. MEDIUM severity.

**Batch import doesn't normalize timestamps:** `import_shared.go:43-57` calls
`store.CreateIssuesWithFullOptions` without pre-validating timestamp format.
If `created_at` has an invalid RFC3339 value, the `.UTC()` call in
`issues.go:35-46` may panic. LOW severity.

---

### Code review findings (session 5, not CLI-testable)

**Pull doesn't check merge conflicts:** `DoltStore.Pull()` at `store.go:979-1006`
returns nil on success but never checks `dolt_conflicts` table. Silent merge
conflicts can corrupt query results.

**Schema migration non-transactional:** `initSchemaOnDB` at `store.go:591-676`
runs DDL as individual statements without a transaction. If interrupted, partial
schema state is possible. Migrations are individually idempotent but there is no
migration tracking table — all migrations re-run every time.

**Import silently drops dependencies:** `ImportIssues` at `issues.go:276-282`
silently skips deps whose target doesn't exist. `ImportResult.Created` is always
`len(issues)` regardless of how many were actually created or how many deps were
dropped.

**SQLite-to-Dolt migration drops all comments:** `extractFromSQLite` at
`migrate_dolt.go:249-418` extracts issues, labels, deps, events, config — but
NOT the `comments` table. Structured comments are silently lost during migration.

---

### Code review findings (session 4, not CLI-testable)

**`bd update --status closed` audit trail gap:** Issues closed via update have
`close_reason = ''` (empty) and generate different event data format than
`bd close`. `IsFailureClose()` always returns false for these, silently
preventing conditional-blocks from firing. Also runs `EventUpdate` hook instead
of `EventClose` hook. See BUG-21.

**Wisp merge in SearchIssues breaks sort order:** At `queries.go:321-337`, wisps
are appended after Dolt results without re-sorting. A P0 wisp appears after
a P4 permanent issue. See BUG-24.

**Epic eligible for closure misses wisp children:** `GetEpicsEligibleForClosure`
at `queries.go:637-715` only queries the `issues` table, not `wisps`. Molecules
with wisp steps are never reported as eligible for closure.

---

### Code review findings (session 3, not CLI-testable)

**Interactions table not cleaned on delete:** `DeleteIssue()` at `issues.go:602`
cleans up dependencies, events, comments, labels — but NOT the `interactions`
table. Orphaned records accumulate silently. LOW severity.

**PromoteFromEphemeral() non-atomic:** At `ephemeral_routing.go:57-128`, wisp
promotion runs multiple operations without a wrapping transaction. If the
process crashes mid-promotion, data is in an inconsistent state (issue in both
ephemeral and permanent tables). Event and comment copy errors are silently
swallowed with `_, _ = s.execContext()`. HIGH severity (code review only).

**Transaction isolation:** `RunInTransaction()` uses default isolation level
(`nil` TxOptions). Concurrent updates to the same field are last-writer-wins
with no conflict detection. MEDIUM severity.

---

## MINOR ISSUES / OBSERVATIONS

### OBS-1: `bd supersede` and `bd duplicate` don't set close_reason

When `bd supersede X --with Y` or `bd duplicate X --of Y` closes issue X,
the `close_reason` field is empty. The relationship is tracked via a
`supersedes`/`duplicate-of` dependency, but there's no close_reason like
"superseded" or "duplicate" set on the issue. Users querying closed issues
by reason would miss these.

### OBS-2: `count --by-status` doesn't show "blocked" count

`count --by-status` shows only "open" and "closed" (and "in_progress",
"deferred" when applicable). Issues with open blocking dependencies show as
"open", not "blocked". This is consistent with BUG-4 but may confuse users.

### OBS-3: `bd sql` allows arbitrary writes (no safety check)

`bd sql "UPDATE issues SET title = 'X'"` succeeds without warning. Only
`--readonly` flag prevents it (but blocks ALL sql, even reads). There's no
write-specific safety prompt or `--force` requirement for mutating SQL.

### OBS-4: `bd label rm` is not a recognized alias for `bd label remove`

Running `bd label rm <id> <label>` shows the `bd label` help text instead of
an error message. Users might expect `rm` as a common alias. The `bd delete`
command uses `--force` not `--yes`.

### OBS-3: `bd label add` syntax is `[issue-id...] [label]` (last arg = label)

The syntax treats all args except the last as issue IDs and the last as the
label. This means you can label multiple issues at once, but only one label
at a time. This is correct but potentially confusing — `bd label add id lab1 lab2`
adds label "lab2" to issues "id" and "lab1".

---

## PROTOCOL TEST IDEAS

These are candidates for porting to the protocol test suite (PR #1910) once it
lands. Tests are classified as:

- **DATA INTEGRITY**: invariants about data correctness (cycle prevention,
  dep cleanup, data preservation). These are hard protocol guarantees.
- **POLICY/UX**: invariants about behavior that could reasonably change
  (epic auto-close, claim semantics, message text). Useful as regression
  tests but not immutable protocol.
- **MESSAGE CONTRACT**: tests that assert specific CLI output strings.
  Brittle — useful for regression detection but will break if wording changes.

### PT-1: Close guard respects dep types — DATA INTEGRITY

```
GIVEN issue A with caused-by dep on open issue B
WHEN close A
THEN close succeeds (caused-by is non-blocking)

GIVEN issue C with blocks dep on open issue D
WHEN close C
THEN close is rejected with suggestion to use --force
```

Already tested manually — works correctly. Good protocol invariant to formalize.

### PT-2: Epic lifecycle — children don't auto-close parent — POLICY/UX

```
GIVEN epic E with children C1, C2
WHEN close C1, close C2 (all children closed)
THEN E remains open
AND E appears in bd ready output
WHEN close E
THEN E is closed
```

Works correctly. Note: auto-close-on-all-children-done is a reasonable
alternative policy. This test documents current behavior, not a hard invariant.

### PT-3: Delete cleans up dependency links — DATA INTEGRITY

```
GIVEN A depends on B (blocks)
WHEN delete B --force
THEN A has no dependencies
AND A appears in bd ready output
```

Works correctly. CASCADE DELETE on FK ensures this at the schema level.

### PT-4: Reopen preserves dependencies — DATA INTEGRITY

```
GIVEN A depends on B (caused-by)
WHEN close A, then reopen A
THEN A still has dep on B
```

Works correctly.

### PT-5: `dep tree` shows full tree (BLOCKED by BUG-2) — DATA INTEGRITY

```
GIVEN diamond dependency: A→B, A→C, B→D, C→D
WHEN dep tree A
THEN output shows all 4 nodes at correct depths
AND D appears twice (or once with "shown above" marker)
```

Currently broken — only root shows. Needs BUG-2 fix first.

### PT-6: Ready semantics exclude blocked issues — DATA INTEGRITY

```
GIVEN A→B (blocks), A→C (blocks), D (no deps)
WHEN bd ready
THEN A is NOT in ready list (blocked by B and C)
AND B is in ready list (no blockers)
AND C is in ready list (no blockers)
AND D is in ready list
```

Works correctly.

### PT-7: Deferred issues excluded from ready — DATA INTEGRITY

```
GIVEN A deferred until 2099-12-31
WHEN bd ready
THEN A is NOT in ready list
WHEN undefer A
THEN A IS in ready list
```

Works correctly.

### PT-8: Concurrent create is safe — DATA INTEGRITY

```
WHEN 10 parallel bd create commands
THEN all 10 issues exist with unique IDs
AND count matches expected total
```

Works correctly.

### PT-9: Concurrent label add is NOT safe (documents BUG-5) — DATA INTEGRITY

```
WHEN 5 parallel bd label add <id> "label-N"
THEN only 0-4 labels survive (lost update race)
```

This would be a regression test to verify when the fix lands.

### PT-10: `list --status blocked` should match `blocked` output — POLICY/UX

```
GIVEN A→B (blocks), both open
THEN bd list --status blocked should include A
AND bd blocked should include A
AND counts should match
```

Currently fails — documents BUG-4.

### PT-11: Status transitions round-trip — DATA INTEGRITY

```
open → in_progress → open → closed → open (via update)
open → deferred → open (via defer/undefer)
All transitions preserve issue data (deps, labels, comments)
```

Works correctly.

### PT-12: Notes append vs overwrite — DATA INTEGRITY

```
GIVEN issue with notes "Original"
WHEN update --notes "Replaced"
THEN notes = "Replaced" (overwrite)
WHEN update --append-notes "Extra"
THEN notes = "Replaced\nExtra" (append with newline)
```

Works correctly.

### PT-13: Special characters in fields — DATA INTEGRITY

```
GIVEN bd create --title 'Test "quotes" & <brackets>'
THEN show --json correctly escapes and preserves the title
```

Works correctly.

### PT-14: Export command existence (BLOCKED by BUG-1) — POLICY/UX

```
WHEN bd export
THEN command exists and produces JSONL output
```

Currently fails — export removed from main.

### PT-15: Supersede creates dependency and closes issue — DATA INTEGRITY

```
GIVEN issue A and B
WHEN bd supersede A --with B
THEN A is closed
AND A has supersedes dependency on B
```

Works correctly (though close_reason is empty — see OBS-1).

### PT-16: Duplicate marks issue as closed with dependency — DATA INTEGRITY

```
GIVEN issue A and B
WHEN bd duplicate B --of A
THEN B is closed
AND B has duplicate-of dependency on A
```

Works correctly (though close_reason is empty — see OBS-1).

### PT-17: Type change round-trip — DATA INTEGRITY

```
GIVEN task T
WHEN update T --type bug, then update T --type epic
THEN type=epic
```

Works correctly.

### PT-18: Transitive blocking chain — DATA INTEGRITY

```
GIVEN A→B→C→D (all blocks)
THEN only D is ready, A/B/C are blocked
WHEN close D: only C becomes ready
WHEN close C: only B becomes ready
WHEN close B: only A becomes ready
```

Works correctly. Good chain-invariant test.

### PT-19: Circular dependency prevention — DATA INTEGRITY

```
GIVEN A→B→C (blocks)
WHEN dep add C→A (blocks)
THEN error "would create a cycle"
AND the dependency is NOT added
AND dep cycles shows no cycles
```

Works correctly. Critical invariant.

### PT-20: Close --force overrides close guard — POLICY/UX

```
GIVEN A→B (blocks), B is open
WHEN close A (no force)
THEN rejected
WHEN close A --force
THEN A is closed
```

Works correctly.

### PT-21: Claim semantics (atomic) — POLICY/UX + MESSAGE CONTRACT

```
WHEN update X --claim
THEN X.status = in_progress, X.assignee = current user
WHEN update X --claim (again)
THEN error "already claimed"
```

Works correctly.

### PT-22: Create with --parent creates dotted ID — DATA INTEGRITY

```
WHEN create --title "Child" --parent P
THEN child ID is P.N (e.g., P.1)
AND children P shows the child
AND child has parent-child dep on P
```

Works correctly.

### PT-23: Create with --deps creates blocks dependency — DATA INTEGRITY

```
WHEN create --title "X" --deps B
THEN X has blocks dep on B
AND X is in blocked list
```

Works correctly.

### PT-24: count --by-status, --by-type, --by-priority grouping — DATA INTEGRITY

```
GIVEN mixed issues with various statuses, types, priorities
THEN count --by-status groups correctly
AND count --by-type groups correctly
AND count --by-priority groups correctly
AND totals match count without filter
```

Works correctly.

### PT-25: Due date and defer round-trip — DATA INTEGRITY

```
GIVEN issue I
WHEN update I --due "2099-06-15"
THEN show --json has due_at with 2099-06-15 date
WHEN defer I --until 2099-12-31
THEN status=deferred, defer_until has 2099-12-31 date
```

Works correctly.

### PT-26: dep rm unblocks issue — DATA INTEGRITY

```
GIVEN A→B (blocks)
WHEN dep rm A B
THEN A is in ready list
AND A is NOT in blocked list
```

Works correctly.

### PT-27: Self-dependency prevention — DATA INTEGRITY

```
WHEN dep add A A --type blocks
THEN error "would create a cycle"
```

Works correctly (caught by cycle detection).

### PT-28: Create with --deps creates blocking dep — DATA INTEGRITY

```
GIVEN issue B
WHEN create --title "X" --deps B
THEN X is blocked by B
AND B is in ready list
AND X is NOT in ready list
```

Works correctly.

### PT-29: Label add/remove round-trip — DATA INTEGRITY

```
GIVEN issue I with no labels
WHEN label add I "bug-fix"
WHEN label add I "urgent"
THEN I has 2 labels
WHEN label remove I "bug-fix"
THEN I has 1 label ("urgent")
```

Works correctly.

### PT-30: Comments preserved through close/reopen — DATA INTEGRITY

```
GIVEN issue I with 2 comments
WHEN close I, reopen I
THEN I still has 2 comments
```

Works correctly.

### PT-31: Due date round-trip — DATA INTEGRITY

```
GIVEN issue I
WHEN update I --due "2099-06-15"
THEN show --json has due_at containing "2099-06-15"
```

Works correctly.

### PT-32: Status transition round-trip — DATA INTEGRITY

```
open → in_progress → open → closed → open (reopen)
All transitions work, data preserved at each step
```

Works correctly.

---

## PRIOR ART: Dolt migration bugs already fixed

These were found and fixed before this discovery session. Documented here so
future investigators don't re-discover them. All are merged to main.

| PR | What it fixed | Why it matters for regression testing |
|---|---|---|
| #1969 (nmelo) | `execContext` didn't commit writes under `--no-auto-commit` | Root cause of many "data disappears" bugs. `execContext` now wraps each statement in `BeginTx/Commit`. Directly relevant to BUG-5 investigation — concurrent `Commit()` to Dolt working set may still race. |
| #1966 (turian) | Labels, comments, deps lost during batch import | `ImportIssues` didn't persist associated data. |
| #1967 (turian) | `scanIssueIDs` lost ORDER BY | `ready` and `list` returned results in wrong order. |
| #1968 (turian) | `UpdateIssue` didn't normalize metadata/waiters | Nullable JSON fields stored as `null` instead of `{}`, breaking downstream code. |
| #1914 (turian) | Column drift in issue scan projection | Centralized column list prevents SELECT * from silently gaining/losing columns after schema migration. |
| #1816 (sjsyrek) | Silent empty results on Dolt lock errors | Dolt lock contention returned empty results instead of errors. |
| #1797 (sjsyrek) | Locking, migration, compaction stability | Major stabilization pass on Dolt backend. |
| #1948 (Xexr) | Parent-child deps mixed with blocking deps in `bd list` | `list --parent` was showing blocking deps as children. |
| #1909 (zjrosen) | `AddDependency`/`RemoveDependency` not in explicit transactions | Writes could be lost under `--no-auto-commit`. Directly relevant to BUG-7 — the upsert at `dependencies.go:78` is now inside an explicit tx. |

### Key Dolt constraints learned from prior fixes

- **`execContext` wraps in BeginTx/Commit**: Every write is its own mini-transaction (store.go:214). This means two writes to the same table from different goroutines each commit independently to the Dolt working set, which can race.
- **Close rows before nested queries**: Dolt with `MaxOpenConns=1` deadlocks if you open a second query while iterating the first. This is why `GetIssuesByLabel` collects IDs first, closes rows, then fetches issues.
- **Schema version check skips init**: PR #1765 added version check so `ensureSchema` doesn't re-run DDL on every command. If you add a new table/column, bump the schema version.

---

## TEST INFRASTRUCTURE NOTES

### Snapshot harness (DONE — branch fix/regression-snapshot-harness)

The regression harness has been adapted to work without `bd export`:

1. **`snapshot()` method** replaces `bd export` with `bd list --json -n 0` + `bd show <id> --json` per issue, emitting JSONL for the existing normalization pipeline.

2. **`export()` method** rewired to translate old export flags (`--status`, `--assignee`, `-o`) and delegate to `snapshot()`. All 71 direct `.export()` calls in scenarios_test.go work unchanged.

3. **`compareExports()`** calls `snapshot()` directly on both workspaces.

4. **Database isolation**: Each workspace gets a unique prefix derived from FNV hash of its temp directory path (e.g., `t12345`). This creates separate Dolt databases (`beads_t12345`) per test workspace. Note: `BEADS_TEST_MODE=1` in env is kept but the real isolation comes from the unique prefix → `cfg.DoltDatabase = "beads_<prefix>"` in metadata.json.

5. **Normalization additions for show-vs-export field differences**:
   - Strip `content_hash`, `events` (show-only fields)
   - Strip `closed_at`, `close_reason`, `created_at`, `updated_at`, `created_by`, `thread_id` from dep/dependent sub-objects
   - Rename `dependency_type` → `type` in dep sub-objects
   - Canonicalize `parent` field (raw issue ID → ISSUE-N)
   - Handle `dependents` array same as `dependencies`
   - Handle metadata `"{}"` vs nil

6. **Prerequisites**: Dolt sql-server must be running on 127.0.0.1:3307. Start with `dolt sql-server --host 127.0.0.1 --port 3307 --data-dir /tmp/dolt-regression-server`.

### Parity run results (2026-02-22)

| Category | Count | Details |
|----------|-------|---------|
| PASS | 95+ | All basic lifecycle, labels, deps, comments, types, priorities, dates, due/defer, ready, blocked, count, search, delete, children, tree, query, stale |
| FAIL (known bugs) | 10 | BUG-4,7,8,10(×2),11,12,13,14 + TestExportByAssigneeFilter (export removed) |
| FAIL (new findings) | 3 | TestUpdateDoesNotClobberRelationalData (labels missing in dependent view), TestBlockedEpicChildrenNotReady (GH#1495), TestListResolvedBlockerAnnotation (GH#1858) |
| SKIP (pre-existing) | 10 | Export/import removed, waits-for baseline gap, sorting GH#1880, metadata GH#1912, etc. |

### BEADS_DOLT_SERVER_DATABASE bypass (discovered during harness work)

The `BEADS_TEST_MODE=1` env var is intended to create isolated test databases via FNV hash of `cfg.Path`. However, `main.go:543` calls `cfg.GetDoltDatabase()` which pre-fills `cfg.Database` BEFORE `applyConfigDefaults` checks for test mode. This means the test mode hash is bypassed.

The regression harness works around this by using unique prefixes per workspace, which causes `bd init` to create unique databases (`beads_t<hash>`). The `BEADS_TEST_MODE` bypass should be fixed in the main codebase for other test consumers.
