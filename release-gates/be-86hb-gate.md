# Release gate — be-86hb (PR #3540 hotfix: tier-aware compat migrations)

**Date:** 2026-04-30
**Deployer:** beads/deployer-1
**Bead (review):** be-86hb — Review: be-zjv6 tier-aware compat migrations (PR #3540 fix)
**Feature bead:** be-zjv6 (closed) — discovered from be-4m8o
**Source commit:** `145a67a4` on `be-xtf-readme`
**Cherry-picked as:** `75514126` on `release/be-n6n`
**Base:** `release/be-n6n` @ `720afaf3` (original be-n6n gate PASS commit)

## Verdict: PASS

This is a follow-up gate on top of the original `release-gates/be-n6n-gate.md`.
PR #3540 had landed `92cb2022` (be-n6n perf gate) which CI subsequently
caught as a regression in `TestEmbeddedOpenRunsCompatMigrations`: the
tracking-table gate silenced the GH#3412 schema-shape self-heal that
017_add_started_at_column relies on. be-zjv6 introduced the tier split
fix; this deploy lands it on top of the existing PR.

| # | Criterion | Status | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | reviewer-gm-po6pox3 verdict in be-86hb notes: "0 blockers, 0 high, 0 medium, 0 low." Commit 145a67a4 reviewed against be-zjv6 spec; tier semantics + test rewrite confirmed correct. |
| 2 | Acceptance criteria met | PASS | `CompatTier` enum + `Tier` field on `CompatMigration` (runner.go); 16 entries `CompatTierDrift`, only `backfill_custom_tables` is `CompatTierBackfill`; gate is `if m.Tier == CompatTierBackfill && applied[m.Name]`. `compat_migrations_test.go::TestRunCompatMigrationsSkipsWhenUpToDate` rewritten to assert backfill gate via `DELETE FROM custom_types` + post-call recount. Out-of-scope files (`embeddeddolt/compat_migrations_test.go`, `release-gates/be-n6n-gate.md`) untouched. |
| 3 | Tests pass | PASS | Targeted regression: `BEADS_TEST_EMBEDDED_DOLT=1 go test -tags gms_pure_go -count=1 -run TestEmbeddedOpenRunsCompatMigrations ./internal/storage/embeddeddolt/` → `ok 0.519s` (the CI failure that motivated this fix). `go test -tags gms_pure_go -count=1 -run TestRunCompatMigrations ./internal/storage/dolt/` → PASS. `go test -tags gms_pure_go ./internal/storage/dolt/migrations/...` → PASS. Build clean (`go build -tags gms_pure_go ./...`). Full `./internal/storage/dolt/` run shows the same pre-existing failure set documented on the original be-n6n gate (`TestApplyConfigDefaults_*` from `BEADS_DOLT_SERVER_PORT=28231` in the rig env, `TestPrePushFSCK_UnopenableDB` fixture issue) — no new regressions. |
| 4 | No high-severity review findings open | PASS | Reviewer reported 0 blockers, 0 high, 0 medium, 0 low. |
| 5 | Final branch is clean | PASS | `git status` shows only pre-existing untracked scaffolding (`.gc/`, `.gitkeep`, `release-gates/be-pq5-gate.md` from a prior FAIL deploy). Same set as original be-n6n gate. |
| 6 | Branch diverges cleanly from main | PASS | Cherry-pick of `145a67a4` onto `release/be-n6n` @ `720afaf3` applied with no conflicts (`git cherry-pick 145a67a4` → `[release/be-n6n 75514126] fix(dolt): be-zjv6 tier-aware compat migrations preserve drift self-heal`, 2 files changed, 61+/38−). |

## Cherry-pick decision

Source branch `be-xtf-readme` carries downstream commits beyond 145a67a4
(fd11920c be-xtf docs, ee67d5f9 be-avn staleDatabasePrefixes,
5e6d84b4 be-a5z list --skip-labels, 77f6bd34 be-z5w isProductionPort
docs). Only `145a67a4` is in scope here per be-86hb hand-off — the others
are tracked under their own review beads (be-jdoy, be-ripy, etc.) and
will deploy through their own ship paths.

## Trade-off acknowledged

Fast-path SQL savings change from "30 → 3 roundtrips" to "30 → ~17"
because drift-tier idempotency checks still run. This is the deliberate
trade-off documented in be-zjv6 and the runner doc comment. If gas-city
contention re-surfaces, the follow-up is an introspection cache or
fingerprint sentinel — out of scope for this fix.

## Pre-existing failures (acknowledged, not introduced)

`./internal/storage/dolt/...` full run shows the same set the original
be-n6n gate catalogued and the be-bpb validator independently flagged:

- `TestApplyConfigDefaults_*` (5 cases) — port-isolation tests that
  observe `BEADS_DOLT_SERVER_PORT=28231` set in the deployer rig env;
  failures reproduce on `origin/main` baseline, orthogonal to this fix.
- `TestPrePushFSCK_UnopenableDB` — pre-existing fixture issue.

None touch compat-migration code paths.

## Hand-off notes

- PR #3540 commit history after this deploy: `92cb2022` (be-n6n perf) →
  `720afaf3` (original gate) → `75514126` (this fix). No history rewrite;
  forward push only.
- Post-deploy: comment on PR #3540 enumerating the regression and the
  tier-split fix per be-zjv6 done-when.
