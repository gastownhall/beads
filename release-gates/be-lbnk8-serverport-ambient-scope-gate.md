# Deploy Gate: ResolveServerMode ambient port-var scoping + KillStaleServers safety-guard consistency

**Date:** 2026-08-21
**Deployer:** beads/deployer
**Bead:** be-mob5e
**Source bead (review):** be-lbnk8
**Source commit:** f5328d863153ecef94df9713ca2e3ed5e0ab0a95
**Branch:** builder/be-9i0yq.1 (provenance only — not a push target)
**Base:** origin/main
**Merge-base:** 1617f3a85cec67ad0f78ea1b8217bd3f1e00095d (== origin/main tip, exact)
**Merge-tree simulation:** clean — dry-run merge test produced no conflicts
**PR target:** gastownhall/beads:main ← quad341/beads-sec003-contrib:deploy/be-lbnk8-gate (PR URL recorded on be-mob5e once opened)

## Pre-flight

No existing PR found for be-mob5e / be-lbnk8 / commit `f5328d863153ecef94df9713ca2e3ed5e0ab0a95`. Clear to proceed.

## Verdict

**PASS** — proceeding to isolated deploy branch, PR, and merge-request routing to mayor.

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | `bd show be-lbnk8 --json`: `close_reason: "pass"`. Review notes final lines: `verdict: pass` (round 4), `deploy_bead: be-mob5e`, `deploy_commit: f5328d863153ecef94df9713ca2e3ed5e0ab0a95` — matches be-mob5e's own `metadata.commit` exactly. |
| 2 | Acceptance criteria met | PASS | 4-round adversarial review on be-lbnk8, read in full (43.8KB notes). Round 1 found a real regression (ambient `BEADS_DOLT_SERVER_PORT` broke 3 pre-existing tests: `TestResolveServerMode_Default`, `_EmbeddedMode`, `_EmbeddedHonoredWithoutServerEnv`) and requested changes. Round 2: reviewer independently re-verified and *withdrew* their own round-1 root-cause theory (proved, via an identical-observable-input argument, that directory-scoping the env check is impossible without breaking the diff's own `EnvPortImpliesExternal` test) — but caught a second, more serious diff-owned regression via before/after control at the same ambient env: `TestKillStaleServersPreservesOtherRepoServers` failing, tied to the GH#2430 journal-corruption safety guard (a data-integrity concern, not a style nit). Round 3 (builder): audited all 4 production call sites of `ResolveServerMode` (`open.go:47`, `open.go:182`, `bootstrap.go:914`, `store.go:1602`), fixed only the one genuinely affected (`killStaleServersForDir`, via new internal split `resolveServerModeIgnoringPortEnv`), left the other 3 unchanged with call-site-specific reasoning for each. Round 4: reviewer independently re-verified every item of the round-3 exit contract **by name**, not by trusting builder's notes — regression test now passes, caller audit re-confirmed by reading source directly at the reviewed commit, round-1 and round-2 tests still pass, follow-up bead `be-wbyau` filed for the 2 confirmed-pre-existing/unrelated host-inference tests. Round-4 verdict: pass. |
| 3 | Tests pass | PASS | This session's independent verification at deploy commit `f5328d863153ecef94df9713ca2e3ed5e0ab0a95`: `make ci-pr-core` full suite green, `CI_PR_CORE_EXIT=0`, 100+ packages `ok`, zero FAIL/SKIP (incl. `internal/doltserver` 5.056s). Separate verbose `-v` run of `internal/doltserver/...` under this rig's real ambient-env condition (shared-dolt convention, `BEADS_DOLT_SERVER_PORT` set): 100% pass across ~150 test names/subtests, including both previously-regression-prone tests (`TestResolveServerMode_HostInferredExternal`, `TestResolveServerMode_EnvHostBeatsEmbeddedMetadata`, tracked as be-wbyau) — independent reconfirmation beyond the review's own round-4 evidence, under the exact ambient condition the whole review chain was about. |
| 3a | Failure attribution (if invoked) | N/A | Not invoked — no failures observed in this round's independent run. |
| 3b | Policy/lint lane | PASS (2 named exceptions) | `ci-pr-policy` fails ONLY on the well-precedented `.githooks/commit-msg` BEGIN/END BEADS INTEGRATION marker check — a git-ignored, per-session shim, not a repo file (see memory `reference_githooks_commit_msg_false_positive.md`; 4+ prior gate rounds incl. be-jy56/be-jygq, be-p7dzx, be-y1jo, be-g3iz8). All other policy sub-checks (build-tag policy, go-install guidance, all version-consistency files bar this one) pass. `ci-pr-lint`: gofmt clean; golangci-lint reports 6 gosec findings, all confirmed non-blocking: 3× G304 under `../builder/worktrees/be-gepv/...` — confirmed that worktree no longer exists on disk (`ls` → "No such file or directory"); golangci-lint's own log shows "no such file or directory" for these exact paths yet still emitted findings, proving stale reuse from its shared, non-worktree-scoped result cache (`~/.cache/golangci-lint`) — a newly-observed environmental hazard, distinct from the commit-msg one. 3× G602 under `backend/conformance/{cycle_detector_contract.go,importer_contract.go}` — confirmed byte-identical to `origin/main` (`git diff origin/main HEAD -- <those files>` empty) and entirely outside this diff's 3-file scope (`git diff --stat origin/main...HEAD` touches only `internal/doltserver/*`). |
| 4 | No high-severity findings open | PASS | The one substantive finding across all 4 rounds (round 2's `TestKillStaleServersPreservesOtherRepoServers` / GH#2430 data-integrity regression) was fixed in round 3 and independently re-verified fixed in round 4 — both by the reviewer (by name, re-run at the reviewed commit) and by this session's own clean- and ambient-env test runs. Round 4 `security_findings: none blocking`. No open findings at the deploy commit. |
| 5 | Feature branch clean | PASS | `git status --short` at detached HEAD `f5328d863153ecef94df9713ca2e3ed5e0ab0a95` — empty. |
| 6 | Branch diverges cleanly from base | PASS | `git merge-base HEAD origin/main` == `git rev-parse origin/main` == `1617f3a85cec67ad0f78ea1b8217bd3f1e00095d` exactly; dry-run merge-tree simulation clean, no conflicts. |
| 7 | Single feature theme | PASS | `git diff --stat origin/main...HEAD`: 3 files changed, all under `internal/doltserver/` (`doltserver.go`, `doltserver_test.go`, `servermode.go`), 147 insertions(+), 6 deletions(-). Single coherent theme: ambient `BEADS_DOLT_SERVER_PORT`/`BEADS_DOLT_PORT` handling — precedence in `ResolveServerMode` (GH#2949-family, round 1) plus a consistency fix scoping the `killStaleServersForDir` orphan-cleanup guard so it no longer conflates port-routing with lifecycle ownership (GH#2430 safety contract, round 3). |

## Disposition

PASS. Proceeding to cut isolated deploy branch `deploy/be-lbnk8-gate` from `f5328d863153ecef94df9713ca2e3ed5e0ab0a95` via `resolve_deploy_branch_target`, commit this gate file on that branch, run `assert_safe_push_target`, push to `headfork` (`quad341/beads-sec003-contrib.git`), open a PR against `gastownhall/beads:main` via `gh pr create` (never `gh pr merge`), record the PR URL on `be-mob5e`, and route a verified merge-request to mayor. Merge authority is operator/mayor/mpr only.

## Post-gate history

None yet — initial gate.
