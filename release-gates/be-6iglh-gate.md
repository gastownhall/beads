# Release gate: be-6iglh — defer auto-wake never runs in dolt server mode

**Verdict: PASS.**

Branch: `deploy/be-6iglh-gate`
Base on origin: `origin/main` @ `cbfc505e39a60514c57dcdb5afe155c8659647ba`
HEAD: `36f5bcfc6693b249b5950d3e8fde676ff0ae0899`
Source bead: `be-0l89e` (deploy); review `be-6iglh`; build `be-vbhpf` (investigation `be-p4b1i`).

## Commit

| # | SHA | Subject |
|---|-----|---------|
| 1 | `942b23978` | test(dolt): red — defer auto-wake must run for classified-read-only server-mode opens (be-vbhpf) |
| 2 | `36f5bcfc6` | feat: green — defer auto-wake runs for classified-read-only server-mode opens (refs be-vbhpf) |

Isolated deploy branch cut from the reviewer-recorded commit `1e77cae4cce5e36f96fbdda9cd9dbda469bacbcb` (source: `builder/be-vbhpf`), then carried through one bounded self-rebase onto `origin/main` (criterion 6 below) to reach current HEAD. `git diff --stat origin/main HEAD`: 5 files changed, 157 insertions(+), 1 deletion(-) — `cmd/bd/defer_wake_server_test.go` (new, +100), `cmd/bd/main.go` (+4), `internal/storage/dolt/queries.go` (+19/-1), `internal/storage/dolt/readonly_server_policy_test.go` (+26), `internal/storage/dolt/store.go` (+9).

## Criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | PASS | `beads/reviewer` PASS verdict recorded on `be-6iglh`, re-confirmed via `be-0l89e` description: "Status: Reviewed and PASSED by beads/reviewer." Review bead `be-6iglh`. |
| 2 | Acceptance criteria met | PASS | Against `be-vbhpf`'s Done-when list: (1) `dolt.Config` carries an explicit `ClassifiedRead` signal set only from `cmd/bd/main.go` — `internal/storage/dolt/store.go` (+9), `cmd/bd/main.go` (+4). (2) `wakeExpiredDefers` no longer skips on classification-only read-only open, doc comment updated — `internal/storage/dolt/queries.go` (+19/-1). (3) Server-mode test proves an expired dated defer returns to open on `bd ready` and `bd list --ready`, failing on old code / passing after — `TestServerModeDeferAutoWake/expired_dated_defer_wakes_on_ready` + `/expired_dated_defer_wakes_on_list_ready`, both PASS (see criterion 3). (4) Strict-readonly/preview/foreign-project read-only still does not sweep — `TestDeferWakeSweepEligibleHonorsClassifiedRead/strict_read-only_never_sweeps` PASS. (5) Dateless defer still never woken — `TestServerModeDeferAutoWake/dateless_defer_never_wakes` PASS. (6) Existing read-only-command tests (incl. the auto-prune one at `cmd/bd/events_journal.go:116-121`) still pass — `cmd/bd` package `ok` in both the full-suite baseline and the targeted container run, no regressions. (7) "Manual check against a real server-mode store: an issue deferred with a past date is open after one `bd ready`" — not hand-performed separately; `TestServerModeDeferAutoWake/expired_dated_defer_wakes_on_ready` exercises this exact scenario against a real `dolthub/dolt-sql-server:2.2.0` container in genuine server mode (not a mock), which is the automated equivalent of the manual check and is noted here as such rather than silently substituted. |
| 3 | Tests pass | PASS | `test_cmd: TEST_COVER=1 ./scripts/test.sh` (`go test -p 4 -parallel 4 -timeout 25m -covermode=atomic -coverprofile ... ./...`) — `test_cmd_scope: full-suite`. `test_counts`: every package in the tree reports `ok` or "no test files/no statements" — **0 FAIL**, exit code 0, total coverage 39.1%. `diff_tests_executed`: run separately, container-backed (`BEADS_TEST_ENV_RUN_DOLT=1 BEADS_TEST_PROXIED_SERVER=1`, rootless podman, `dolthub/dolt-sql-server:2.2.0`), because the default full-suite env self-skips dolt-server tests to match real PR-CI: `TestDeferWakeSweepEligibleHonorsClassifiedRead` (`internal/storage/dolt/readonly_server_policy_test.go`, new func) — PASS, 4/4 subtests (`writable_open_sweeps`, `strict_read-only_never_sweeps`, `classified-read_still_sweeps_despite_readOnly`, `classifiedRead_without_readOnly_stays_eligible`), package `ok` 18.435s. `TestServerModeDeferAutoWake` (`cmd/bd/defer_wake_server_test.go`, new file) — PASS, 4/4 subtests (`expired_dated_defer_wakes_on_ready`, `expired_dated_defer_wakes_on_list_ready`, `dateless_defer_never_wakes`, `future_dated_defer_stays_hidden`), package `ok` 7.444s. Both diff-owned test files verified exhaustively (`grep '^func Test'`) — no other diff-owned test function exists in either file. |
| 3a | Pre-existing failures attributed | N/A (no failures observed) | The full-suite baseline (this criterion's own run) came back **100% clean, 0 FAIL across the entire tree** — nothing to attribute. The two conditions the reviewer had cited on `be-6iglh` (`be-9ogs6`: ambient `~/.beads` leak, 62 failures; `be-epvkz`: flaky `TestCloseAndFlushPersistsQueuedEvents`) did **not** reproduce in this run. Both trackers remain open (re-confirmed via `bd show`) for whenever they next reproduce; noted here for auditability only. |
| 3b | Policy/lint lane | PASS (attributed) | `policy_lane: make ci-pr-policy` run fresh on HEAD `36f5bcfc6`. `check-build-tags`: 99 files scanned, all clear. `go install` guidance: clean. Version consistency: 7/9 checks PASS (MCP pyproject.toml, MCP `__init__.py`, Claude plugin.json, Codex plugin.json, Claude marketplace.json, npm package.json, MCP uv.lock all `1.2.2`); 2 FAIL — `.githooks/commit-msg` missing BEGIN/END BEADS INTEGRATION markers. `policy_attribution`: both FAILs → `be-a0dxu` (gate-tracker, pre-existing, filed this session before this run). Clause 1 (not diff-owned): `.githooks/commit-msg` is not even a tracked repository file in this worktree (excluded via `.git/info/exclude:40`, confirmed by `git ls-files --error-unmatch` failing) — the version-consistency checker scans whatever sits on disk under `.githooks/`, not just tracked files, so this local per-clone shim trips on every gate round independent of diff content. Clause 3 (not caused by diff) via mechanism proof: this diff's 5 files (`cmd/bd/{defer_wake_server_test.go,main.go}`, `internal/storage/dolt/{queries.go,store.go,readonly_server_policy_test.go}`) share no code path with `.githooks/` or `scripts/ci/pr-policy.sh`'s version scanner. Clause 4 (no path overlap): confirmed disjoint. |
| 3c | CI-config diff lane | N/A | `ci_lane_run: n/a` — diff touches no CI job/matrix/timeout/required-check config; all 5 changed files are application/test code under `cmd/bd/` and `internal/storage/dolt/`. |
| 4 | No high-severity review findings open | PASS | `be-6iglh` review record: `style_findings: none`, `security_findings: none`. Zero open HIGH findings. |
| 5 | Final branch is clean | PASS | `git status` clean on all tracked files — zero modified/staged changes. Only untracked content present: 7 pre-existing orphan `release-gates/*.md` files from unrelated bead IDs (`be-7q688`, `be-ephml`, `be-f30dn`, `be-gwlps`, `be-h6t5y`, `be-qsgu8`, `be-ttfqp`) already present in this shared worktree before this session started, plus this gate's own markdown (written untracked, then committed onto this branch per the standard deploy workflow). |
| 6 | Branch diverges cleanly from main | PASS | Completed bounded self-rebase this session: `origin/main` had advanced one commit (`cbfc505e3`, gate-routing change with no file overlap with this diff) past the branch's original merge-base. `attempt_bounded_self_rebase` — `BEFORE_SHA=1e77cae4cce5e36f96fbdda9cd9dbda469bacbcb` → `AFTER_SHA=36f5bcfc6693b249b5950d3e8fde676ff0ae0899` — clean, zero conflicts. Verified independently (not exit code alone): `git ls-remote headfork` confirmed remote tip == `AFTER_SHA`; `assert_reviewed_sha_present` confirmed patch-id equivalence between the original reviewed content and the rebased HEAD. Current HEAD's merge-base with `origin/main` now equals `origin/main`'s own tip (`cbfc505e39a60514c57dcdb5afe155c8659647ba`) — fully current, nothing further to rebase. |
| 7 | Single feature theme | PASS | 5 files, 157 insertions(+)/1 deletion(-), entirely confined to one subsystem: dolt server-mode classified-read defer-wake sweep eligibility (`cmd/bd/main.go` config wiring, `internal/storage/dolt/{queries,store}.go` the fix itself, two new/extended test files covering both layers). No unrelated changes riding along. |

## Test environment

- Host: Linux 7.1.8-200.fc44.x86_64.
- Full-suite baseline: default env (`BEADS_TEST_ENV_RUN_DOLT` unset → dolt-server tests self-skip via `BEADS_TEST_SKIP=dolt`), matching real PR-CI's env exactly.
- Diff-owned targeted runs: `DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock`, `TESTCONTAINERS_RYUK_DISABLED=true`, `BEADS_TEST_ENV_RUN_DOLT=1`, `BEADS_TEST_PROXIED_SERVER=1` — rootless podman, image `dolthub/dolt-sql-server:2.2.0` (pinned, cached).
- TMPDIR/GOTMPDIR pinned to `~/.gotmp` (per `/tmp` tmpfs 12.5G per-user quota).
- `make ci-pr-policy`: run fresh on final HEAD, see criterion 3b.

## Pre-flight

- Not already merged: `git grep classifiedRead origin/main` finds only an unrelated pre-existing local-variable name in `TestServerOpenCanAutoStartHonorsDisableAutoStart`; the `dolt.Config.ClassifiedRead` field and `deferWakeSweepEligible` function this diff adds are not present on `origin/main`.
- No duplicate PR: `gh pr list --repo gastownhall/beads` search across `be-vbhpf`/`be-6iglh`/defer-auto-wake/classified-read terms returns no matching open or prior PR for this fix. (PR #5386, merged 2026-08-07, is a different, already-shipped fix for front-read/claim-path auto-wake — not this server-mode classified-read-only gap.)

## Push target

`gastownhall/beads` is contributor-only for this rig: `origin`'s push URL is hard-disabled (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`). `PUSH_REMOTE=headfork` (`quad341/beads-sec003-contrib`). Per the repo's contributor-only carve-out, this deploy's job ends at the open PR — no merge-request is routed to mayor, and no `release-gate/deploy-clearance` commit status is posted (that mechanism applies only to repos this rig can merge).

## Known pre-existing conditions (did not reproduce this run)

- `be-9ogs6` — ambient `~/.beads` leaks into tests assuming a clean workspace. Open, unfixed. Did not manifest in this session's full-suite baseline.
- `be-epvkz` — flaky `TestCloseAndFlushPersistsQueuedEvents` under host load. Open, unfixed. Did not manifest in this session's full-suite baseline.
- `be-a0dxu` — `ci-pr-policy` version-consistency false positive on local `.githooks/commit-msg` shim (gate-tracker). Reproduced this run exactly as tracked; attributed per criterion 3b.
