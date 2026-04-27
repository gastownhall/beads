# Release gate — be-z5w (AD-01 test isolation)

**Date:** 2026-04-27
**Deployer:** beads/deployer
**Bead (review):** be-z5w — Review: be-c5p AD-01 test isolation (isProductionPort + DB firewall)
**Feature bead:** be-c5p (closed)
**Builder commits:** `c835e2eb` (AD-01 implementation) + `594aca7e` (INFO-1 doc caveat)
**Source branch:** `be-vzu-rebase-fix`
**Final branch:** `release/be-z5w` (cut off `origin/main`)
**Base:** `origin/main`

## Verdict: PASS

## What this ships

Two-part defense-in-depth against test processes connecting to the production
Dolt server:

1. **`isProductionPort` helper + DB-name firewall** (commit `c835e2eb`) —
   replaces literal port comparisons with a three-rule detection
   (`DefaultSQLPort`, `BEADS_PRODUCTION_PORT`, `<BeadsDir>/dolt-server.port`),
   adds a database-name firewall (`testdb_`, `bd_test_`, `benchdb_` prefixes
   refuse to connect to a production server unless `BEADS_TEST_SERVER=1`),
   updates the panic message to a multi-line scannable form, and wires
   `BEADS_TEST_SERVER` into 11 testmain files + 1 subprocess env-filter site.

2. **Doc caveat** (commit `594aca7e`) — adds the operator-responsibility note
   above `isProductionPort`: setting `BEADS_TEST_SERVER=1` disables both AD-01
   guards, so a misconfigured `BEADS_DOLT_SERVER_PORT` can still connect to
   production. This was INFO-1 in the first-pass review.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | beads/reviewer-1 (initial REQUEST-CHANGES → resolution → re-review by reviewer-gm-md5piee = PASS). Single-pass per current process. |
| 2 | Acceptance criteria met | PASS | All 7 ACs walked by builder + reviewer. AC#2 / AC#3 (no-opt-in) / AC#4 unit coverage routed to validator via be-69n (`needs-tests`); FR-01 amendment routed to architect via be-6g7 (`needs-architecture`). Both follow-ups tracked, neither blocks deploy. |
| 3 | Tests pass | PASS | `make test` on `release/be-z5w` with clean env. Only pre-existing failures observed (verified by re-running the same failing tests on `origin/main` with the same env). AD-01 unit tests (`TestApplyConfigDefaults_*`, all 7) PASS. See "Test summary" below. |
| 4 | No HIGH-severity review findings open | PASS | Reviewer flagged only INFO items; no `severity: high` findings remain unresolved. |
| 5 | Final branch is clean | PASS | `git status` clean before / after cherry-pick. |
| 6 | Branch diverges cleanly from main | PASS | Two cherry-picks applied with no conflicts: `c835e2eb` → `1404f780`, `594aca7e` → `03c0da24`. |

## Cherry-pick log

```
$ git checkout -B release/be-z5w origin/main
$ git cherry-pick c835e2eb
[release/be-z5w 1404f780] feat(storage): be-c5p AD-01 isProductionPort + DB-name firewall
 15 files changed, 195 insertions(+), 10 deletions(-)
$ git cherry-pick 594aca7e
[release/be-z5w 03c0da24] docs(storage): be-z5w isProductionPort BEADS_TEST_SERVER caveat
 1 file changed, 5 insertions(+)
```

## Test summary

`env -u BEADS_DOLT_SERVER_PORT -u BEADS_DIR -u BEADS_DOLT_AUTO_START -u GC_DOLT_PORT make test`
on `release/be-z5w`. The env-unset is required because the deployer's gc rig
exports those vars (which point at the gc-management bd server on port 28231)
and they leak into otherwise-hermetic Go unit tests; without unsetting them
the AD-01 unit tests fail spuriously by reading the leaked production port.
Note that this leakage is itself the kind of thing AD-01 protects against in
production, but the `TestApplyConfigDefaults_*` unit tests pre-date the
firewall/opt-in escape hatch and assume those vars are unset.

### AD-01 tests on `release/be-z5w`: PASS

- `TestApplyConfigDefaults_TestModeUseSentinelPort` ✓
- `TestApplyConfigDefaults_TestModeWithPort` ✓
- `TestApplyConfigDefaults_TestModeBlocksProdPort` ✓ (rule #1 path, no-opt-in)
- `TestApplyConfigDefaults_EnvOverridesConfig` ✓
- `TestApplyConfigDefaults_ProductionFallback` ✓
- `TestApplyConfigDefaults_SocketFromEnv` ✓
- `TestApplyConfigDefaults_SocketExplicitOverridesEnv` ✓

### Pre-existing failures (verified on `origin/main` with same clean env)

| Test | Package | On `release/be-z5w` | On `origin/main` |
|------|---------|---------------------|------------------|
| `TestConcurrentInitSchema` | `internal/storage/dolt` | FAIL | FAIL |
| `TestPullWithAutoResolve_BranchTrackingFallback` | `internal/storage/dolt` | FAIL | FAIL |
| `TestEnginePullSkipsNoopUpdate` | `internal/tracker` | FAIL | FAIL |
| `TestEnginePushUsesBatchTrackerWhenAvailable` | `internal/tracker` | FAIL | FAIL |
| `TestEngineDryRunUsesBatchPreviewWhenAvailable` | `internal/tracker` | FAIL | FAIL |
| `TestEngineConflictResolution` | `internal/tracker` | FAIL | FAIL |
| `TestEngineSyncDoesNotCreateFalseConflictsAfterPull` | `internal/tracker` | FAIL | FAIL |
| `TestResolvePartialID_Wisp/wisp_prefix_with_hash` | `internal/utils` | FAIL | FAIL |

None of these touch surface area the AD-01 commits modify. Builder's bead
notes pre-flagged the dolt failures as pre-existing on the source branch
and the deployer reproduced them on `origin/main` directly, so they're not
introduced by this PR.

## Tracked follow-ups (do not block this deploy)

- **be-69n** (validator) — direct unit coverage for AC#2 (`BEADS_PRODUCTION_PORT`
  env), AC#3 no-opt-in (`<BeadsDir>/dolt-server.port`), AC#4 (firewall error
  string format). The decision logic is end-to-end exercised; these are
  pedestrian unit tests on the helpers.
- **be-6g7** (architect) — FR-01 amendment in be-crd to ratify
  `BEADS_TEST_SERVER=1` as an opt-in to the AD-01 panic guard, removing the
  literal-spec deviation noted in INFO-2.
