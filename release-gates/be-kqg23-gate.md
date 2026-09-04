# Release gate — be-kqg23 (Fix: differential-regression CI budget is under the suite's own passing wall time (5 spurious reds/day))

**Date:** 2026-09-04
**Deployer:** beads/deployer
**Bead (deploy):** be-23wcp
**Source bead:** be-kqg23 — review verdict PASS. style_findings: none blocking (one info note: the bead's own pre-edit line-number reference in its Done-when checklist went stale after the 12-line comment insertion; file content itself is correct). security_findings: none blocking (one info note: raising the job-level ceiling 25m→45m bounds worst-case CI-minute cost on a hang, consistent with the existing `main.yml` `Test` job precedent; `pull_request` trigger, not `pull_request_target`, so forked PRs run with a restricted token and no repo secrets). spec_findings: tests_green true (single-file YAML diff — no Go/JS/Python test suite applies at review time; verified via actionlint + `yaml.safe_load()` + direct content assertions against all pre-PR-checkable Done-when items). Build bead: `be-3r5pr`.
**Source commit:** `3afba49b69ff6ae7c935f22b5293384ce18fe54a` (parent `c0d8da42de5fd15c95adac85e342ba4a121da0fb` = origin/main's own tip — confirmed via fresh `git fetch origin main` this round: `origin/main` == `merge-base(origin/main, HEAD)` == this parent. Zero divergence, no rebase needed.)
**Branch:** `deploy/be-kqg23-gate`, cut directly at the reviewed SHA via `resolve_deploy_branch_target be-kqg23 3afba49b69ff6ae7c935f22b5293384ce18fe54a`
**Push target:** `headfork` (`quad341/beads-sec003-contrib`) — `origin` push-disabled by design (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`). Pushed and independently re-verified this round: `git ls-remote headfork refs/heads/deploy/be-kqg23-gate` → `3afba49b69ff6ae7c935f22b5293384ce18fe54a`, matches local HEAD exactly.
**PR:** [gastownhall/beads#6271](https://github.com/gastownhall/beads/pull/6271) — OPEN, MERGEABLE, cross-repo (`quad341:deploy/be-kqg23-gate` → `gastownhall:main`)

## Verdict: 7/7 — raw PASS, no waivers

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-kqg23: VERDICT PASS. No blocking style/security findings (two non-blocking info notes, see header). spec_findings tests_green true via actionlint (independently re-run, exit 0) + YAML parse check. |
| 2 | Acceptance criteria met | PASS | Diff matches the bead's stated fix theme exactly: `.github/workflows/regression.yml` only, `timeout-minutes: 25→45`, `go test -timeout=20m→40m`, plus an explanatory comment. Mirrors the precedent already applied to `main.yml`'s `Test` job for the identical failure mode (budget tighter than the suite's own observed wall time); content verified against `main.yml:453-467` at review time. |
| 3 | Tests pass (full-suite, never narrowed) | PASS | Full-suite `make test` run at the exact deploy SHA this round. 62 FAIL, all independently attributed to tracked pre-existing bead `be-9ogs6` via 4-part proof. See "Criterion 3" below. |
| 4 | No open HIGH-severity findings | PASS | be-kqg23 security_findings: no blockers, no majors, one non-blocking info note (CI-minute cost, bounded, precedented). Diff is a static CI-workflow value change — no injection, auth, secrets, access-control, or deserialization surface touched. |
| 5 | Final branch is clean | PASS | `git status --porcelain` clean on `deploy/be-kqg23-gate`, verified this round. |
| 6 | Branch diverges cleanly from base | PASS | Cut directly from the reviewed SHA; parent is origin/main's current tip (re-confirmed via fresh fetch this round). `assert_reviewed_sha_present origin/main 3afba49b69ff6ae7c935f22b5293384ce18fe54a` → rc=0. |
| 7 | Single feature theme / ancestry scope | PASS (raw, no waiver) | `assert_deploy_ancestry_scope origin/main 3afba49b69ff6ae7c935f22b5293384ce18fe54a be-23wcp be-3r5pr be-kqg23` → rc=0. Sole non-merge commit's message ("... (refs be-3r5pr)") cites an accepted bead id directly; no denylisted `.claude/**` paths in range. |

## Criterion 3 — full-suite test evidence + CI-config-diff live-run (3c)

- `test_cmd`: `make test` (full `./...` scope), run at the exact pushed deploy SHA `3afba49b69ff6ae7c935f22b5293384ce18fe54a` on `deploy/be-kqg23-gate`, this deploy round (2026-09-04).
- `test_cmd_scope`: full-suite — not narrowed or filtered to the changed file.
- `test_counts`: 62 FAIL, all attributed below as pre-existing and untouched by this diff; remaining suite PASS/SKIP consistent with `be-9ogs6`'s own documented baseline (standard pre-existing SKIP population — Dolt opt-in integration suites, e.g. default `BEADS_TEST_SKIP=dolt` — unrelated to this diff, same shape as prior gate records e.g. be-3vzut).
- `diff_tests_executed`: none — the diff touches only `.github/workflows/regression.yml`; zero Go/test files in the diff (confirmed at review time), so no suite test directly exercises the changed content. The diff's actual correctness evidence is the live CI job it modifies (`ci_lane_run` below), not a unit test.
- `failure_attribution` (4-part proof, non-diff-owned-gate-failure protocol):
  1. **Not diff-owned** — all 62 failing tests are Go unit tests (e.g. `TestBackupDir_NoWorkspaceReturnsActiveWorkspaceError`, `TestFindAllDatabases_Unit`, `TestDefaultSearchPaths_FallsBackToCwdFormulaDirWithoutBeadsProject`); none reside in or exercise `.github/workflows/regression.yml`.
  2. **Tracked bead** — `be-9ogs6` (P2, OPEN): "pre-existing test failure: test isolation: ambient ~/.beads leaks into tests assuming a clean workspace."
  3. **Not caused by diff** — two independent proofs: (a) `be-9ogs6` reproduces the identical 62-failure set on a clean `origin/main` checkout with zero code changes (byte-for-byte identical `--- FAIL` names between a diff-tip run and a fresh scratch-worktree run, per `comm -23/-13`); (b) diff-specific mechanism proof — the changed file is a GitHub Actions workflow YAML, not reachable via any Go import path, so a CI-YAML-only diff structurally cannot alter Go runtime/test behavior at all.
  4. **No path overlap** — diff path is `.github/workflows/regression.yml`; `be-9ogs6`'s failures live in Go source under `cmd/bd` and related packages (repo-root/config walk-up logic, `backupDir()`, `DefaultSearchPaths()`) — disjoint from the diff's path.
- `attribution_evidence`: `be-9ogs6` bead body — reproduction methodology (two full-suite runs, diff tip vs. clean scratch worktree pinned to `origin/main`, byte-for-byte identical 62-test-name overlap via `comm -23/-13` on sorted `--- FAIL` names).
- `ci_lane_run` (criterion 3c — CI-config-diff handling): **PASS.** This diff modifies the "Differential Regression" job's own budget; the correct live gate is that job actually running green under the new numbers, not a substitute (actionlint/YAML-parse were already independently verified at review time). Confirmed on PR #6271: the job self-triggered via the workflow's own `risky_pattern` match on the changed file itself and ran to completion — **Differential Regression (v0.49.6 baseline): pass, 11m15s** ([run 33868684258 / job 101009355610](https://github.com/gastownhall/beads/actions/runs/33868684258/job/101009355610)). This is the first real run under the new 40m/45m budget and is the actual evidence this change works, not a proxy for it. Both PR-time-only Done-when criteria carried forward from the source bead (could not be evaluated before a PR existed) are now satisfied:
  1. PR body states the exact budget numbers are a data-backed proposal and the final call belongs to the maintainers — present verbatim: *"The specific numbers (40m / 45m) are a data-backed proposal based on the measurement above — the final call on the right budget is yours to make."*
  2. The Differential Regression job is scheduled and green on this PR — confirmed above.
- `waiver_ref`: none needed.
- `uncovered_criteria`: none.

All other required checks on PR #6271 also pass — full `gh pr checks 6271` enumeration: Build (all variants), Lint, formatting, doc-freshness (3 platforms), migration hygiene, version consistency, full `Test` matrix (Embedded/Proxied/Server Dolt, Windows, macOS), Historical v0.9.1→v1.1.2 corpus (11 versions), Upgrade smoke (5 versions), Contract corpus, Storage backend conformance, PR preflight (3 platforms), Package Gate (MCP/npm), CI Gate / Required — zero failing or pending required checks. `[code]smith` shows `skipping` (optional advisory autofix bot, not a required check).

## Merge authority

`gastownhall/beads` is contributor-only for this rig — no rig agent has merge access. Per established precedent (be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3, be-vc1m [PR #5792], be-7q688 [PR #6003], be-6iglh/be-0l89e [PR #6082], be-c8kgv [PR #6221], be-1wwre [PR #6247], be-3vzut [PR #6262]), the deployer's job ends at the open, verified PR regardless of gate outcome. No merge-request is routed to mayor; PR URL reported for visibility only.

## Disposition

7/7 — raw PASS, no waivers. Full-suite test run at the exact deploy SHA with all 62 failures independently attributed to tracked pre-existing bead be-9ogs6 via 4-part proof, including a diff-specific mechanism argument (CI YAML cannot affect Go test behavior). Both PR-time-only Done-when criteria carried forward from review are satisfied: the PR body states the maintainer-call framing, and this PR's own Differential Regression job ran green under the new budget. Closing be-23wcp and reporting PR #6271 to mayor for visibility; no merge-request routing (contributor-only rig).
