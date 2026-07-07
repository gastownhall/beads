# Release Gate: bd-3852 — orphaned children doctor check

**PR**: https://github.com/gastownhall/beads/pull/4053
**Branch**: `fix/bd-3852-orphaned-children-check` (gastownhall/beads)
**Date**: 2026-05-21
**Deployer**: beads/deployer

## Gate Result: PASS

| # | Criterion | Evidence | Result |
|---|-----------|----------|--------|
| 1 | Review PASS present | be-qodg: PASS — 2 LOW notes, no blockers | ✅ PASS |
| 2 | Acceptance criteria met | See below | ✅ PASS |
| 3 | Tests pass | CI run 26173640486 — all 41 checks PASS (ubuntu, macOS, Windows smoke, Embedded Dolt cmd 1–20, Storage, lint, fmt, doc freshness, upgrade smokes) | ✅ PASS |
| 4 | No high-severity findings | be-qodg: "None blocking" — LOW-1 cosmetic `%d+` unconditional; LOW-2 no functional impact | ✅ PASS |
| 5 | Final branch is clean | `git status` — clean; only rig artifacts untracked | ✅ PASS |
| 6 | Branch diverges cleanly from main | 1 commit ahead of `origin/main`; merge state CLEAN | ✅ PASS |

## Acceptance Criteria Verification (bd-3852)

| Criterion | Evidence | Result |
|-----------|----------|--------|
| `checkOrphanedChildren(db)` added to `RunDeepValidation` | `result.OrphanedChildren = checkOrphanedChildren(db)` at line 121; wired into `AllChecks` at line 122 | ✅ |
| `OrphanedChildren DoctorCheck` field in `DeepValidationResult` | `OrphanedChildren DoctorCheck \`json:"orphaned_children"\`` at line 22 | ✅ |
| Query uses `SUBSTRING_INDEX` to find children whose parent prefix is not in issues | SQL uses `SUBSTRING_INDEX(id, '.', 1) NOT IN (SELECT id FROM issues) LIMIT 10` | ✅ |
| Reports as `StatusWarning`, not `StatusError` | `check.Status = StatusWarning` on orphans found path | ✅ |
| Shows up to 3 example IDs | `orphans[:min(3, len(orphans))]` in Detail field | ✅ |
| Fix hint provided | `check.Fix = "Delete orphaned issues with 'bd admin delete <id>' or re-create the parent"` | ✅ |
| `rows.Err()` checked | Present after row scan loop | ✅ |
| `defer rows.Close()` in place | Present | ✅ |
| Dolt-backend guard prevents `SUBSTRING_INDEX` on SQLite | `RunDeepValidation` returns early if `backend != BackendDolt` (reviewer confirmed) | ✅ |

**Note on LOW-1**: Message says `"Found %d+ child issues whose parent no longer exists"` — the `+` is unconditional even when count < LIMIT (e.g., "Found 3+ child issues" is misleading when exactly 3 exist). This is cosmetic and non-blocking per reviewer verdict.

## Commits

| SHA | Description |
|-----|-------------|
| `94298a169` | feat(doctor): detect orphaned child issues in deep validation (bd-3852) |
