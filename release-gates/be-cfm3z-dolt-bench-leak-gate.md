# Release gate — be-k4hyk (be-cfm3z dolt-benchmark prod-DB-leak fix)

**Date:** 2026-07-25
**Deployer:** beads/deployer
**Bead (deploy):** be-k4hyk — Deploy: be-cfm3z dolt-benchmark prod-DB-leak fix (reviewed PASS)
**Source bead:** be-cfm3z — closed, review verdict PASS
**Source commit:** `2d20764f4d08ab9855ee860b6e796a0d546182f5` (provenance branch `gc-builder-d7bc3eb5bd82`, review worktree `/var/tmp/be-cfm3z-review`)
**Branch:** `deploy/be-k4hyk-gate` (isolated, cut fresh at the reviewed SHA — never a shared `gc-builder-*` branch)
**Base:** `origin/main` @ `c2796999d` ("fix(reclaim): repair a mojibake em dash in the replica help text")
**Merge-base:** `b7e25f091` ("Merge pull request #4997 from gastownhall/db/gate-check")
**Merge-tree simulation:** `git merge-tree --write-tree origin/main 2d20764f4` → tree `4c500b895`, exit 0, **zero conflicts**

## Verdict: PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | beads/reviewer PASS verdict in be-cfm3z notes: 0 blockers, build/vet/lint clean, 173/173 package tests, both headline claims (clean-skip under live ambient danger; opt-in path against a real throwaway dolt server) empirically verified end-to-end. One non-blocking count nitpick (48 vs claimed 49 benchmark sub-cases), fully reconciled, not a blocker. |
| 2 | Acceptance criteria met | PASS | All 4 "Done-when" items from be-cfm3z independently verified against the actual diff (see Acceptance check below). |
| 3 | Tests pass | PASS | `./scripts/test.sh -v ./internal/storage/dolt/...` on `deploy/be-k4hyk-gate` (commit `2d20764f4`) → **exit 0, 173 PASS / 0 FAIL**, matching the reviewer's report exactly. `go build ./...` and `go vet ./...` both exit 0. See Test-environment note for a methodology correction. |
| 4 | No HIGH-severity findings open | PASS | Reviewer notes record zero HIGH/blocker findings. |
| 5 | Final branch is clean | PASS | `git status` on `deploy/be-k4hyk-gate` at `2d20764f4`: "nothing to commit, working tree clean." |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main 2d20764f4` succeeds with a single merged tree and no conflict markers. |
| 7 | Single feature theme | PASS | `git diff --stat b7e25f091 2d20764f4` — exactly 2 files (`dolt_benchmark_test.go` modified, `dolt_benchmark_safety_test.go` new), 131 insertions(+), 2 deletions(-). One theme: stop the dolt benchmark suite from leaking test databases onto the shared production Dolt server. |

## Acceptance check (be-cfm3z "Done-when")

1. **`setupBenchStore`/`BenchmarkBootstrapEmbedded` skip when `BEADS_BENCH_DOLT_PORT` is unset; never resolve server port from ambient `BEADS_DOLT_SERVER_PORT`.**
   - `dolt_benchmark_test.go`: both call sites scrub `BEADS_DOLT_SERVER_PORT`/`BEADS_DOLT_PORT` to `""` via `b.Setenv` *before* calling the new `benchDoltServerPort()` helper, then `b.Skip(reason)` when it reports `skip=true`.
   - Directly re-ran the 4 new pure-logic tests in `dolt_benchmark_safety_test.go` (`TestBenchDoltServerPort_{SkipsWithoutOptIn,UsesExplicitOptIn,RejectsInvalidOptIn,RejectsZeroOrNegative}`) under this rig's real live ambient value (`BEADS_DOLT_SERVER_PORT=28231`) — all 4 PASS, confirming the skip decision is correct under the exact real danger condition, not just a simulated one.
   - **PASS.**
2. **`cleanup()` drops the bench database.**
   - `dropBenchDatabase()` opens a short-lived admin connection and issues `DROP DATABASE IF EXISTS` for the exact database name used by setup; wired into both benchmark cleanup paths.
   - **PASS.**
3. **Regression test (pure logic, no server) proving the skip behavior; plus a test that `setupBenchStore` skips under the default agent env.**
   - `dolt_benchmark_safety_test.go` is a new file of exactly this shape: 4 table tests against the pure decision helper `benchDoltServerPort()`, no network, no server dependency — cannot itself reach a real Dolt server regardless of ambient env.
   - **PASS.**
4. **`CGO_ENABLED=0 go test -tags gms_pure_go -count=1 ./internal/storage/dolt/...` green.**
   - Ran the canonical repo test entrypoint (`./scripts/test.sh`, which wraps this exact invocation with the repo's standard hermetic env setup) — 173/173 green. See Test-environment note.
   - **PASS.**

## Test-environment note (methodology correction, non-blocking)

Independently re-running tests surfaced a discrepancy worth recording. A raw

```
CGO_ENABLED=0 go test -tags gms_pure_go -count=1 ./internal/storage/dolt/...
```

invocation (i.e., *without* going through `scripts/test.sh`) on `deploy/be-k4hyk-gate` at `2d20764f4` fails 5 tests: `TestApplyConfigDefaults_{TestModeUseSentinelPort,TestModeWithPort,TestModeBlocksProdPort,EnvOverridesConfig,ProductionFallback}` in `store_unit_test.go`. This is **not** a regression from be-cfm3z's diff (which never touches `store.go` or `store_unit_test.go`); it is the exact same baseline pre-existing failure the builder's own preflight note already documented, root-caused to `store_unit_test.go` clearing legacy `BEADS_DOLT_PORT` but not the newer primary `BEADS_DOLT_SERVER_PORT=28231` this rig's shell exports.

Verified two ways this is not a real gate blocker:

- **Correct test entrypoint.** `scripts/test.sh` sources `scripts/ci/lib/test-env.sh`, whose `beads_test_env_enter()` explicitly `unset`s `BEADS_DOLT_SERVER_PORT`/`BEADS_DOLT_PORT` (and other ambient vars) before running Go tests, for a hermetic environment. Running the canonical `./scripts/test.sh -v ./internal/storage/dolt/...` on the same commit produces **173 PASS / 0 FAIL** — matching the reviewer's reported result exactly.
- **Merge-result cross-check.** Before finding the above, I also built a local, unpushed merge-simulation commit (`git commit-tree <merge-tree> -p origin/main -p 2d20764f4`) and ran the raw (non-hermetic) suite there: 181 PASS / 0 FAIL, 0 skipped-as-fail. `store.go`/`store_unit_test.go` changed substantially on `origin/main` since this branch's merge-base (`internal/storage/dolt/store.go` +213/−?, `store_unit_test.go` +66, via PR #3632/AD-01's production-port firewall generalization), and since be-cfm3z's diff doesn't touch either file, merging cleanly inherits main's fix regardless of test entrypoint.

One correction to the reviewer's stated rationale: the reviewer attributed their clean 173/173 run to "this branch's rebase onto a post-#3632 main," but `git merge-base --is-ancestor <PR-3632-merge-commit> 2d20764f4` returns false — PR #3632 is **not** actually an ancestor of the reviewed commit. The reviewer's bottom-line result (173/173, PASS) is fully correct and independently reproduced; the actual reason is that `scripts/test.sh`'s hermetic env wrapper scrubs the ambient port vars, making the branch's relationship to PR #3632 irrelevant to this specific test outcome. Recorded here so the next deployer doesn't have to re-derive it. Not a blocker for this deploy.

## Hand-off

- Push: `deploy/be-k4hyk-gate` → `sec003` (`quad341/beads-sec003-contrib`) — re-verified current precedent via `gh pr view 5028 --json headRepository` before pushing (origin is push-disabled upstream `gastownhall/beads`; `fork` and `sec003` both resolve to `quad341`-owned remotes in this rig, `sec003` is the active convention per the immediately-prior deploy be-q5hh6).
- PR: cross-repo `quad341:deploy/be-k4hyk-gate` → `gastownhall:main`.
- Merge decision routed to mayor (deployer does not merge).
- No follow-ups required: be-cfm3z's Part 2 (store.go firewall generalization) was deliberately deferred and is superseded by already-merged PR #3632/AD-01.
