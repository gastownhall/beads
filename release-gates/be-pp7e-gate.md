# Release gate — PR-CI lane for `TestBenchDBPurgeDoesNotLeak` against a live Dolt server (be-aiy5)

- **Builder bead:** be-aiy5 — add a PR-CI lane that runs
  `TestBenchDBPurgeDoesNotLeak` against a live Dolt server instead of letting
  `checkDolt` SKIP it.
- **Deploy bead:** be-pp7e
- **Review bead:** be-w90n — verdict **PASS**, recorded on commit
  `7b7732178f4c5c8ef7dd6c787e139d8c869dc8d1`
- **Commits:** `6e0674c9c2808d53d1f1bac700688a06deff5c01` (tdd_red) then
  `7b7732178f4c5c8ef7dd6c787e139d8c869dc8d1` (tdd_green), 2 commits over
  `origin/main`, both citing `be-aiy5`
- **Branch:** `builder/be-aiy5` (provenance only, not a push target — pushed
  to both `fork` and `headfork`, i.e. `quad341/beads-sec003-contrib`)
- **Evaluated:** 2026-08-18 by beads/deployer

## Scope

Adds a new PR-CI job to `.github/workflows/pr-risk.yml` that runs
`TestBenchDBPurgeDoesNotLeak` against a real, live Dolt server instead of
relying on the existing `checkDolt`-gated lane, which self-skips when no
live server is reachable. Diff scope, confirmed via
`git diff --stat origin/main...7b7732178f4c5c8ef7dd6c787e139d8c869dc8d1`
(4 files, 124 insertions, 6 deletions, no `.claude/**` paths):

- `.github/workflows/pr-risk.yml` — new job, primary diff content
- `scripts/ci_workflow_test.go` — new meta-test asserting the new job's shape
- `scripts/ci_capability_selector_test.go` — modified expectations
- `scripts/pull_dolt_image_test.go` — modified expectations

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-w90n records `verdict: pass`, `deploy_bead: be-pp7e`, `deploy_commit: 7b7732178f4c5c8ef7dd6c787e139d8c869dc8d1` — exact match. |
| 2 | Acceptance criteria met | **PASS** | be-w90n's `spec_findings` map all 4 of be-aiy5's exit-contract items (real PASS/FAIL not SKIP; gate not loosened; stretch fail-loud scoped via single `BEADS_TEST_ENV_RUN_DOLT` occurrence; scope contained to gastownhall/beads) with `uncovered_criteria: none`. |
| 3 | Tests pass | **PASS** | All 3 diff-owned tests independently re-run by deployer, real PASS (see below), matching reviewer's own record. |
| 3a | Pre-existing-failure attribution | **N/A — attributed** | Two unrelated failures hit while gating (see below); both confirmed pre-existing on `origin/main` and tracked (be-jy56, be-vf95 — the latter filed by this gate). Neither touches a diff file. |
| 3b | Policy/lint lane | **PASS** | `make ci-pr-policy`: build-tag policy and go-install-guidance sub-checks clean; the one failing sub-check (version-marker) is be-jy56, pre-existing on `origin/main`, unrelated. `make ci-pr-lint`: `gofmt` clean; repo-wide `golangci-lint` noise is be-vf95 (cross-worktree cache contamination + unrelated `backend/conformance` findings); diff-scoped `golangci-lint run ./scripts/...` reports **0 issues**. |
| 4 | No unresolved HIGH findings | **PASS** | be-w90n: zero HIGH/MEDIUM findings across style and security passes — "No blockers or majors found" / "No style issues found." All items INFO-level, none blocking. |
| 5 | Clean working tree | **PASS** | `git status` on the evaluated commit shows no staged/unstaged changes — only the pre-existing, unrelated untracked scratch files already present in this worktree (`release-gates/be-hi97-*.md`, `release-gates/be-k9js-*.md`, `release-gates/be-uoat-*.md`, `scripts/rebase-resolve-lib.sh`), never staged. |
| 6 | Clean divergence from `origin/main` | **PASS** | `origin/main` is already an ancestor of `7b7732178f4c5c8` — clean fast-forward relationship, zero rebase work required. `assert_deploy_ancestry_scope origin/main 7b7732178f4c5c8ef7dd6c787e139d8c869dc8d1 be-pp7e be-aiy5` → rc=0 (both commits cite `be-aiy5`, the accepted sibling builder bead). |
| 7 | Single feature theme | **PASS** | All 4 files serve the one CI-lane feature: the workflow job itself plus the 3 test files whose expectations that job's existence changes. No unrelated changes riding along. |

## Tests run on release branch (independent re-verification)

Environment: `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock`,
`TESTCONTAINERS_RYUK_DISABLED=true`, per the test-evidence-integrity
protocol.

Diff-owned tests, run by exact name (`go test ./scripts/... -run '<names>' -v`):

| Test | Result |
|---|---|
| `TestCICapabilitySelectorWorkflowPreservesExistingAuthority` | PASS |
| `TestPRRiskGateReachesFullServerDoltStorageSuite` | PASS |
| `TestDoltImagePullWorkflowsUseRetryHelper` | PASS |

All 3/3 pass, matching the reviewer's own `diff_tests_executed` record.
Reviewer's own evidence (trusted, not re-run here given cost): focused
`go test ./scripts/... -v` — 60 PASS, 0 FAIL, 1 SKIP (non-diff-owned,
self-skipping-by-design `TestTestScriptPrebuiltBinaryLaunchProbe`, justified
in be-w90n's notes); full `TEST_COVER=1 make test` — 93 packages ok, 0 FAIL,
`internal/storage/dolt` itself ran and passed for real (confirming live
podman/Dolt infra, not silently bypassed).

Static checks, independently re-run:

| Check | Result |
|---|---|
| `make ci-pr-policy` | FAIL — but solely on be-jy56 (pre-existing, unrelated; see criterion 3a) |
| `make ci-pr-lint` | FAIL — but solely on be-vf95 (pre-existing/cross-worktree noise, unrelated; see criterion 3a) |
| `golangci-lint run ./scripts/...` (diff-scoped) | clean, 0 issues |
| `gofmt` (repo-wide, via `make ci-pr-lint`) | clean |

## Findings from reviews (no action required)

From be-w90n: no HIGH or MEDIUM findings, style or security. All items
INFO-level: gofmt/go vet/actionlint clean; workflow trigger is
`pull_request` (not `pull_request_target`), no secrets usage, no
PR-controlled context interpolated into new steps, pinned action SHAs
consistent with existing jobs, the new job's Dolt-install curl-pipe-sudo
step is a pre-existing idiom already used at 2 other call sites (not a new
risk), no new dependencies, touched Go test files only change expected
literals (no new exec/file-write surface).

## Verdict

**PASS** — all 7 criteria (plus 3a/3b) clear. Proceeding to cut the isolated
`deploy/be-pp7e-gate` branch from `7b7732178f4c5c8ef7dd6c787e139d8c869dc8d1`
and open the PR against `gastownhall/beads`. Per this repo's contributor-only
status, the job ends at the opened PR — no merge-request will be routed to
mayor and no deploy-clearance status will be posted; merge authority belongs
to the upstream maintainers.

**Side note for the record (no action taken here):** be-pp7e's underlying
CI-lane fix is very likely the piece that be-vc1m's criterion 3 has been
waiting on (both concern `TestBenchDBPurgeDoesNotLeak`/live-Dolt-server
PR-CI coverage). be-vc1m remains parked on `hold:mayor`, blocked on
`be-w90n`, and is intentionally left untouched by this gate.
