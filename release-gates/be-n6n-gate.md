# Release gate — be-n6n (gate compat migrations with tracking table)

**Date:** 2026-04-27
**Deployer:** beads/deployer-1
**Bead (review):** be-bwp — Review: be-n6n gate RunCompatMigrations with compat_migrations tracking table
**Feature bead:** be-n6n (closed)
**Source commit:** `76d4215b` on `be-vzu-rebase-fix`
**Cherry-picked as:** `92cb2022` on `release/be-n6n`
**Base:** `origin/main` @ `0bc4c725` (feat: add JSONL bulk dep add (#3530))

## Verdict: PASS

| # | Criterion | Status | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | beads/reviewer-1 verdict in be-bwp notes: triangulated review (eng-mgr / principal-eng / security-eng), all findings INFO non-blocking, "Verdict: **PASS** — ready to deploy." |
| 2 | Acceptance criteria met | PASS | Per reviewer's done-when checklist walk: tracking table created, `compat_migrations` added to `migrationTables` (line 111), both regression tests added in dolt package and pass, FAIL on stash-revert verified, concurrent-init tests pass, lint clean. The two non-checkbox items (repo-wide flakes and before/after timing) carve out under reviewer-policy (c). |
| 3 | Tests pass | PASS | Targeted: `go test -tags gms_pure_go -run 'TestRunCompatMigrations\|TestInitSchemaConcurrent' ./internal/storage/dolt/...` → `ok 2.106s`. Full `make test`: failures present, all verified pre-existing by reverting `runner.go` + `compat_migrations_test.go` to `origin/main` and re-running the same tests — same set fails on baseline, no new regressions from be-n6n. Driver: `BEADS_DOLT_SERVER_PORT=28231` is set in the deployer worktree env (gc rig), which breaks `TestApplyConfigDefaults_*` and other port-isolation-sensitive tests on origin/main too. Orthogonal to this fix. |
| 4 | No high-severity review findings open | PASS | Three INFO-only findings, all marked non-blocking. Zero MAJOR/HIGH/BLOCKING findings. |
| 5 | Final branch is clean | PASS | `git status` clean against tracked files; only untracked are pre-existing scaffolding (`.gc/`, `.gitkeep`, `release-gates/be-pq5-gate.md` from a prior FAIL deploy). |
| 6 | Branch diverges cleanly from main | PASS | Single cherry-pick of `76d4215b` onto `origin/main` applied with no conflicts. `git log origin/main..HEAD` = exactly one commit (`92cb2022`). 2 files changed, 121 insertions, 4 deletions — matches the source commit's stat exactly. |

## Cherry-pick decision

The source branch `be-vzu-rebase-fix` carried three unrelated commits:

- `d67652be` — be-vzu (already deployed via `release/be-vzu`)
- `76d4215b` — be-n6n (this deploy)
- `c835e2eb` — be-c5p (review be-z5w returned **REQUEST-CHANGES** — must NOT ship)

Only `76d4215b` was picked, by hash. The bead description names this commit
explicitly: *"Review of commit 76d4215b on be-vzu-rebase-fix."*

## Review findings (all INFO, non-blocking)

1. **Done-when "before/after timing" gap** — carve-out (c) applies. The
   bug's pathological cost is server-mode + cross-process contention; the
   embedded backend's in-process SQL is sub-millisecond, so local timings
   are within noise. Structural fix (~30 SQL queries → 3 on the fast
   path) is visible in the diff.
2. **Two documented spec divergences:** file path
   (`migrations/runner.go` vs spec's `migrations.go` — relocation in
   `f368f988`) and caller scope (embedded backend DOES call
   `RunCompatMigrations` post-relocation, fix benefits both callers).
   Builder flagged both transparently.
3. **Pre-existing test failures explicitly catalogued** by the builder
   (`TestPullWithAutoResolve_BranchTrackingFallback`,
   `TestSchemaParityAuxiliaryTables/*`, `TestSchemaParityIssuesVsWisps`,
   `TestSchemaRunsInitWhenStale`). Each verified to fail on
   stash-reverted code; not introduced by this fix.

## Push target

`origin` (gastownhall/beads) is upstream — `git push --dry-run origin HEAD`
returns 403 for quad341. `PUSH_REMOTE=fork` (quad341/beads). PR is cross-repo
with `--head quad341:release/be-n6n`.
