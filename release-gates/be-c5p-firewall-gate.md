# Release gate — be-m1u (be-c5p AD-01 isProductionPort + DB-name firewall)

**Date:** 2026-05-01
**Deployer:** beads/deployer
**Bead (review):** be-m1u — Review: be-c5p AD-01 isProductionPort + DB-name firewall (47dcc380)
**Source bead:** be-c5p (AD-01) — closed/compacted
**Source commit:** `47dcc380` on `fork/be-vzu-rebase-fix`
**Branch:** `release/be-c5p-firewall` (single cherry-pick on top of `origin/main`)
**Base:** `origin/main` @ `8694c535` ("doctor: detect AGENTS.md / CLAUDE.md user-authored divergence (#3600)")
**Cherry-pick result:** `064a9fa9` — clean apply, zero conflicts.

## Verdict: PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | beads/reviewer PASS verdict in be-m1u notes (2026-05-01): 0 blockers, 0 high, 1 medium (coverage-gap follow-up be-z62), 1 low (descriptive nit). Reviewer verified `47dcc380` against `origin/main` semantics, full OWASP walk, and confirmed `make test` (gms_pure_go) clean. |
| 2 | Acceptance criteria met | PASS | All four ACs from be-m1u verified in cherry-picked code (see AC checklist below). |
| 3 | Tests pass | PASS | `make test` (with `gms_pure_go`) on `release/be-c5p-firewall` produces an **identical 35-test failure set** to `origin/main` under the same rig environment. **Zero regressions introduced.** Failure set is pre-existing worktree-/HOME-/env-coupled config tests, independent of be-c5p (mirrors be-ripy reviewer's note: "pre-existing failures independently confirmed via revert"). The directly relevant `internal/storage/dolt` package passes (`ok 0.092s`) under a clean env, including the highest-risk Rule 1 test `TestApplyConfigDefaults_TestModeBlocksProdPort`. |
| 4 | No HIGH-severity review findings open | PASS | 0 HIGH/blocker findings. 1 MEDIUM finding (coverage gap) — not blocking, filed as **be-z62** with 8 suggested unit tests. 1 LOW finding (descriptive nit on bead description) — not blocking. |
| 5 | Final branch is clean | PASS | `git status` clean (untracked `.gc/`, `.gitkeep` are gc-management infra, not code). |
| 6 | Branch diverges cleanly from main | PASS | Cherry-pick of `47dcc380` onto `origin/main@8694c535` applied cleanly with no conflicts. Single commit on top of main: `064a9fa9 feat(storage): be-c5p AD-01 isProductionPort + DB-name firewall` (15 files, +195 / −10). |

## Acceptance check (be-m1u)

1. **`isProductionPort` honors `BEADS_PRODUCTION_PORT` env + `BeadsDir/dolt-server.port` file + legacy `DefaultSQLPort`.**
   - `internal/storage/dolt/store.go:118-137` — `productionPortReasons` lists all three rules with explicit rationale (Rule 3 doc-block explains why it does NOT fall back to `filepath.Dir(cfg.Path)`).
   - `internal/storage/dolt/store.go:152-157` — `isProductionPort` short-circuits to `false` when `BEADS_TEST_SERVER=1`.
   - **PASS.**
2. **`New()` refuses test-named DBs unless `BEADS_TEST_SERVER=1`.**
   - `internal/storage/dolt/store.go:1038-1048` — returns `error` (not panic); error names the DB and the addr (host:port or socket).
   - Strict `=='1'` opt-in (not "truthy").
   - **PASS.**
3. **`testDatabasePrefixes` adds `benchdb_`.**
   - `internal/storage/dolt/store.go:84` — added alongside the existing six prefixes.
   - Sibling `staleDatabasePrefixes` convergence handled by be-avn (already on `origin/main` semantics not yet — be-ripy still queued separately, but `benchdb_` here covers the firewall path).
   - **PASS.**
4. **Test suites set `BEADS_TEST_SERVER=1` in `TestMain`.**
   - All 13 testmain files updated (verified via grep).
   - `cmd/bd/doctor/fix/testmain_cgo_test.go` also unsets in cleanup; others rely on `os.Exit` terminating the process.
   - `cmd/bd/context_binding_integration_test.go` explicitly puts the var in subprocess `Cmd.Env` because that suite filters `BEADS_*`.
   - `internal/storage/dolt/` package test PASSES under a clean env.
   - **PASS.**

## Test-environment note

`make test` in this rig (deployer worktree) reports 35 pre-existing failures on `origin/main` as well as on this branch. Failures cluster in:

- `internal/config` / `cmd/bd` / `internal/beads` — worktree/HOME-coupled config tests pick up rig-session config and `BEADS_DIR`.
- `internal/storage/dolt` (when env is unscrubbed) — port-resolution tests trip when `BEADS_DOLT_SERVER_PORT` (set by the rig for its own dolt server) leaks into the test environment. Scrubbing `BEADS_DOLT_SERVER_PORT`, `BEADS_DOLT_AUTO_START`, `BEADS_DIR`, `GC_DOLT_PORT`, `GC_BEADS_SCOPE_ROOT`, `BEADS_ACTOR` clears that subset.

These do not reproduce in builder/reviewer rigs (builder reported "build/vet/tests clean" for the source branch; reviewer confirmed `make test` PASS in be-m1u notes). Out of scope for this gate; the diff between `origin/main` and `release/be-c5p-firewall` is **zero new failures**, which is what criterion #3 actually requires.

## Hand-off

- Push: `release/be-c5p-firewall` → `fork` (origin is upstream `gastownhall/beads`; push denied to `quad341`).
- PR: cross-repo `quad341:release/be-c5p-firewall` → `gastownhall:main`.
- On merge: **be-jdoy** unblocks (its docs reference symbols introduced by `47dcc380`).
- Follow-ups (non-blocking, NOT on this PR):
  - **be-z62** — needs-tests follow-up for Rules 2/3, `BEADS_TEST_SERVER=1` short-circuit, `New()` firewall return-error path.
  - **be-jdoy** — TESTING.md test-isolation docs (re-enters deployer queue once this PR lands on `origin/main`).
