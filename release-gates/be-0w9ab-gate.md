# Release gate — be-0w9ab (Review: Fix: dolt test helpers leak the container testcontainers hands back on the reaper-failure path (ga-rv0rh9))

**Date:** 2026-09-13
**Deployer:** beads/deployer
**Bead (deploy):** be-0w9ab
**Source bead:** be-dm8ug — review verdict PASS; build bead `be-ly5bf`; source branch `builder/be-ly5bf`
**Source/deploy commit:** `7d662df1d10a9711fbad5791f7498a7fe9f92b3c` (base `f56632adcfabed7da6ed0aabe4e760066b472c46` = origin/main tip, re-fetched fresh this round — zero drift since review)
**Target repo / base branch:** `gastownhall/beads`, base `main`
**Branch to cut:** `deploy/be-0w9ab-gate` from `7d662df1d10a9711fbad5791f7498a7fe9f92b3c`
**Push target:** `headfork` (`quad341/beads-sec003-contrib`) — `origin` push is disabled (contributor-fork workflow)

## Verdict: 7/7 PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-dm8ug verdict: pass, closed with close_reason "pass". `tdd_red: 9b7c134fd9`, `tdd_green: 7d662df1d1` (= this deploy commit). |
| 2 | Acceptance criteria met | PASS | be-ly5bf's 3 done-when items (fix applied at all 3 leak sites in `internal/testutil/{container_provider.go,testdoltserver.go}`; reproducing test proves the reaper-failure leak path is genuinely exercised, not stubbed; coordination with PR#6522/be-a57h7 confirmed non-overlapping — reaper-disabled vs. reaper-enabled-but-failing cases) all independently verified by the reviewer — be-dm8ug's `uncovered_criteria: none`. |
| 3 | Tests pass | PASS | Full-suite re-run fresh at the exact deploy SHA in a standalone clone (see "Criterion 3" below for scope/method and results). |
| 3a | Pre-existing failure attribution | PASS | The one failure (`TestFindBeadsRepoRoot_WorktreeFallback`) attributed via dispositive BASE_REF reproduction, tracked `be-ey0oy` — see "Criterion 3" below. |
| 3b | Policy/lint lane | PASS | `make ci-pr-policy` (`./scripts/ci/pr-policy.sh`) exit 0, all 10 named check steps passed. One WARN (Check 3: legacy SQLite/database references in `docs/CLI_REFERENCE.md:300`, `docs/architecture/dolt.md:879`, `docs/getting-started/upgrading.md:264,338`, `AGENT_INSTRUCTIONS.md:59`) — pre-existing documented-feature text (sealed SQLite bridge, `--db` flag), none of it in this diff's 3 files. |
| 3c | CI-config diff lane | N/A | Diff (`f56632adc..7d662df1d`) is exactly 3 files — `internal/testutil/{container_provider.go,reaper_leak_repro_test.go,testdoltserver.go}` — no `.github/workflows/**` or `scripts/ci/*` touched. |
| 4 | No high-severity review findings open | PASS | be-dm8ug `style_findings`: none (gofmt -l and go vet clean on all 3 changed files, verified in an isolated detached worktree at the review commit). `security_findings`: none — test-only code, argv-array `exec.Command` (not shell interpolation), a 0o600 bogus socket file for the reaper-failure injection, no new dependencies, reaper kept ENABLED (not weakened) throughout. |
| 5 | Final branch is clean | PASS | `git status --short` at the deploy branch tip shows only pre-existing untracked `release-gates/be-{2ezox,caep7,fkmhv}-gate.md` from other beads' gates — no uncommitted changes to the tracked tree. |
| 6 | Branch diverges cleanly from base | PASS | Fresh `git fetch` this round: tip is `f56632adcfabed7da6ed0aabe4e760066b472c46` — **identical** to the review's recorded `base_commit`, i.e. zero drift since review. `git merge-base --is-ancestor` succeeds: the deploy commit is a clean linear descendant, exactly 2 commits ahead (TDD red + green) — no merge-tree conflict check even required. |
| 7 | Single feature theme | PASS | Single-bead (no `rollup-ship` label). Entire diff (+107/-0 across 3 files) is confined to one package, `internal/testutil` — one fix (container leak on the reaper-failure path) plus its reproducing test, no independent feature bundled. |

## Criterion 3 — full-suite test evidence

**Scope and method:** `make test`'s default full-suite scope (`TEST_COVER=1 ./scripts/test.sh` → `go test -p 4 -parallel 4 -timeout 25m -covermode=atomic -coverprofile ... ./...`), run at the exact deploy SHA `7d662df1d10a9711fbad5791f7498a7fe9f92b3c`. Run inside a disposable standalone clone (`git clone --no-hardlinks`, at `/var/tmp/beads-test-be-0w9ab.NsOLXs`, removed after use), not this worktree — per the mandated safe-testing recipe for this host, since a worktree-run corrupts the shared rig's `.beads/.local_version` marker (tracked `gm-m3n37s`, unfixed). A single monolithic invocation completed within tool timeouts this round — no sharding was needed. Full output captured to `/tmp/claude-1000/be-0w9ab-full-test.log` (654 lines).

**Result:** exactly one FAIL package in the entire ~90+-package suite — `cmd/bd` (356.238s), due to a single failing test, `TestFindBeadsRepoRoot_WorktreeFallback`. Every other package reported `ok`, including the diff's own package: `internal/testutil` — `ok ... 0.074s coverage: 13.5% of statements`.

`test_cmd_scope: full-suite`. `diff_tests_executed`: `TestReaperFailureLeaksContainer` (`internal/testutil/reaper_leak_repro_test.go`) — RED at `9b7c134fd93e40b4b558e3a54162bb8812b52313` failed as expected (leaked container observed, per be-ly5bf's builder record); GREEN at `7d662df1d10a9711fbad5791f7498a7fe9f92b3c` passed (60.95s), a real podman-backed run genuinely terminating a live container, not a stub — corroborated independently by both the builder's own run (60.17s, "Terminating container: 48482128cc9b") and the reviewer's own run (60.95s).

`waiver_ref: none` — the one FAIL resolved by attribution, not waiver.

### 3a — attribution

**`TestFindBeadsRepoRoot_WorktreeFallback` (`cmd/bd`) → `be-ey0oy`**
(i) not diff-owned — `cmd/bd/config_worktree_test.go`, untouched by `f56632adc..7d662df1d` (diff confined to `internal/testutil/*`). (ii) tracked, pre-existing: `be-ey0oy`, already tracking this exact test and failure message before this run began — cited directly in be-0w9ab's own deploy-bead description. (iii) not caused by the diff — dispositive BASE_REF reproduction already established twice: by the reviewer (be-dm8ug notes: reproduced in an isolated worktree at the exact merge-base `f56632adcfabed7da6ed0aabe4e760066b472c46`, identical failure `findBeadsRepoRoot = "/tmp", want ".../main-repo"` at `config_worktree_test.go:83`) and independently noted by the builder (be-ly5bf preflight) as a pre-existing worktree-path-resolution artifact. This gate's own full-suite run at the deploy SHA (this section) reproduces the byte-identical failure — same file:line, same message — confirming the failure is unaffected by this diff, consistent with (not contradicted by) the base-level attribution. (iv) no path overlap — `cmd/bd/config_worktree_test.go` vs. `internal/testutil/*` — disjoint packages.

**Cross-reference:** both be-ly5bf (builder) and be-dm8ug (reviewer) independently attributed this identical failure before this gate began. This deployer's own fresh full-suite run at the deploy SHA is the independent re-verification the gate protocol requires regardless, and it corroborates — rather than contradicts — the prior attributions.

## Merge authority

`gastownhall/beads` is contributor-only for this rig — no rig agent has merge access. Per established precedent (be-vc1m, be-m3er6, be-guyc7, be-u9scu, be-8sfdy, be-a57h7, be-g69lt, and others), the deployer's job on PASS ends at the open, verified PR. No merge-request is routed to mayor.

## Disposition

**7/7 PASS.** Proceeding to push `deploy/be-0w9ab-gate` (cut from `7d662df1d10a9711fbad5791f7498a7fe9f92b3c`) to `headfork`, and open a PR against `gastownhall/beads` base `main` (per the per-bead PR-open instructions template — confirmed empty for be-0w9ab, no extra `PR-DESCRIPTION:BEGIN/END` block or `gc.pr_ping` metadata). No merge-request routed — contributor-only repo, PR-open-only.
