# Release gate: be-nx7 — D4v2 composite (status, updated_at) + defer_until indexes

**Verdict: PASS.**

Branch: `release/be-nx7-d4v2-indexes`
Base: `origin/main` @ `0fa5f210`
HEAD: `684fb884` (after cherry-pick of 2 commits below)

## Commits

| # | SHA on `release/be-nx7-d4v2-indexes` | Source on `quad341/beads:rebase/be-nx7-be-1n9-stack` | Subject |
|---|--------------------------------------|----------------------------------------------------|---------|
| 1 | `e36e6430` | `f2091639` | perf(schema): D4v2 composite (status,updated_at) + defer_until indexes (be-s54) |
| 2 | `684fb884` | `a3bb599f` | fix(schema): be-nx7 test assertion robust to migration additions |

Two cherry-picks; both clean.

## Criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | PASS | Reviewer-1 PASS verdict on migration content carries through both rebases (per builder note 2026-05-02T17:13Z and 2026-05-02T17:35Z); gemini second-pass disabled per current policy. |
| 2 | Acceptance criteria met | PASS | Migration 0033 round-trip + schema test pass; planner picks both new indexes (per reviewer-1 evidence on be-s54 design plan). |
| 3 | Tests pass | PASS | `go test -tags gms_pure_go ./internal/storage/schema/ -count=1` PASS (was FAIL prior to a3bb599f's test fix — reproduced first to confirm). `go test -tags gms_pure_go ./internal/storage/dolt/ -run Migration0033 -count=1` PASS. `make test` shows only pre-existing rig-env-leakage failures (`TestApplyConfigDefaults_*`, `TestPrePushFSCK_UnopenableDB`, `TestDefaultSearchPaths_FallsBackToCwdFormulaDirWithoutBeadsProject`) — all reproduce on clean `origin/main` without any be-nx7 changes; not regressions. |
| 4 | No high-severity review findings open | PASS | None outstanding on bead. |
| 5 | Final branch is clean | PASS | `git status` clean (untracked `.gc/`, `.gitkeep` are rig artifacts outside the tree). |
| 6 | Branch diverges cleanly from main | PASS | `e36e6430` (D4v2) and `684fb884` (test fix) cherry-pick atop `origin/main@0fa5f210` with no conflicts. |

## Pre-existing failures noted (not blocking)

These reproduce on a clean `origin/main` checkout in this rig and are caused by the rig environment (`GC_DOLT_PORT=28231`, host `~/.beads/formulas`) leaking into test processes. Filed for owner attention separately; not be-nx7 regressions.

- `internal/storage/dolt`:
  - `TestApplyConfigDefaults_TestModeUseSentinelPort`
  - `TestApplyConfigDefaults_TestModeWithPort`
  - `TestApplyConfigDefaults_TestModeBlocksProdPort`
  - `TestApplyConfigDefaults_EnvOverridesConfig`
  - `TestApplyConfigDefaults_ProductionFallback`
  - `TestPrePushFSCK_UnopenableDB`
- `internal/formula`:
  - `TestDefaultSearchPaths_FallsBackToCwdFormulaDirWithoutBeadsProject`

## Push target

`PUSH_REMOTE=fork` (origin = `gastownhall/beads` is upstream-not-pushable for this rig user; fork = `quad341/beads` is the cross-repo PR head).

## Provenance block

Per be-08pl §7.2 the integration PR body includes the architect-mandated provenance block verbatim with `<verification-bead-id>` substituted to `be-6e8s`.
