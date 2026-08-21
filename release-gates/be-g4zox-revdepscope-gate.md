# Release gate — reverse-dependency-scoped test-selection tool (be-g4zox)

- **Deploy bead:** be-g4zox (needs-deploy, routed beads/deployer, from:be-paugu)
- **Build bead:** be-mv0ww.3.1 — tdd_red `5247ad94e`, tdd_green `95a142720`, shadow-mode harness `51750d3fa`
- **Review bead:** be-paugu — verdict **PASS** (round 1), closed with reason `pass`
- **Commit deployed:** `3cef8d53d89ea337bb1867142a9f04ec2505ff42` (post-rebase; reviewed source was `51750d3fa53fc567c8955e29bca5442f7f68ab9c`, identical tree, rebased onto current `origin/main` — see criterion 6)
- **Source branch:** `builder/be-mv0ww.3.1` — provenance only, never a push target
- **Related beads:** be-mv0ww.3 (parent design bead, architecture + empirical method-comparison), be-mv0ww.3.1 (this build), be-paugu (review)
- **Deploy branch:** `deploy/be-paugu-gate` — name taken from be-g4zox's own explicit branch-name instruction (derived via `resolve_deploy_branch_target "be-paugu" <sha>`, which reduces to the same deterministic `deploy/<id>-gate` pattern)
- **Push target:** `headfork` (`quad341/beads-sec003-contrib.git`) — `origin` push is disabled by design on this rig (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`)
- **PR:** (recorded below once opened)
- **Evaluated:** 2026-08-21 by beads/deployer

## Scope

New CLI tool `scripts/ci/revdepscope` computes the reverse-dependency-scoped
set of Go packages that need retesting for a given set of changed packages,
using `go list -test -deps` (never a build-only or single-hop import scan —
both were empirically proven on this repo to miss real callers). Includes a
mandatory full-suite fallback when the scoped set exceeds a tunable fraction
of all packages (default 50%), and a shadow-mode validation harness that
diffs scoped output against full-suite ground truth on real commits. Per its
own hard requirements, this tool is explicitly **not** wired into any
deploy-gate criterion, CI requirement, or acceptable-command list — that
remains out of scope pending a stronger shadow-mode validation sample.

Diff is exactly 3 additive files under `scripts/ci/revdepscope/`, 557
insertions, 0 deletions, 0 modifications elsewhere: `main.go`,
`revdepscope_test.go`, `shadow-mode.sh`.

Single feature theme: all 3 commits in the deploy range cite `be-mv0ww.3.1`,
confirmed by `assert_deploy_ancestry_scope` (see criterion 6).

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 0 | Already merged? (pre-flight) | **NO** | `gh pr list -R gastownhall/beads --search "revdepscope"` (state all) → empty. `gh api repos/gastownhall/beads/commits/51750d3fa.../pulls` → empty. `git merge-base --is-ancestor 51750d3fa... origin/main` → not an ancestor. Proceeded. |
| 1 | Review PASS present | **PASS** | be-paugu, `verdict: pass`, round 1, closed with reason `pass`. Reviewer ran independently in an isolated worktree at the true branch tip (caught and corrected a one-commit SHA-staleness issue in the build bead's own recorded metadata before reviewing — documented in be-paugu's notes). |
| 2 | Acceptance criteria met | **PASS** | Independently re-verified against source, not just the reviewer's say-so. Read `main.go` directly: `go list -test -deps` at line 116 (not build-only, not single-hop); threshold fallback at lines 49-51, default 0.5, tunable via `REVDEPSCOPE_THRESHOLD` (line 150); no hand-maintained package lists anywhere — `allPackages`/`deps` computed fresh via `go list` every invocation; plain newline-delimited package-list output (lines 177-179), no new test runner. Read `revdepscope_test.go`: threshold boundary cases (exactly-at vs exceeding) and a real-repo smoke test against `internal/storage/schema` are present and exercised, not just table-driven synthetic cases. Read `shadow-mode.sh`: matches its own documented design — builds the binary, runs one ground-truth full-suite pass, samples N real `origin/main` commits, diffs scoped output against `FAILED_PACKAGES`, reports divergences; limitations (current-tree package resolution, single reused ground truth) honestly disclosed in its own header. Out-of-scope boundary independently confirmed: `deployer.md.tmpl` / `mol-deployer-gate.formula.toml` / be-mv0ww.1's gate criterion untouched — diffstat is exactly the 3 files above. |
| 3 | Tests pass (diff-owned) | **PASS** | Re-ran the reviewer's exact documented command myself, independently, post-rebase: `go build ./scripts/ci/revdepscope/... && go vet ./scripts/ci/revdepscope/... && go test -v ./scripts/ci/revdepscope/...` → **17 PASS, 0 FAIL, 0 SKIP** (TestScope×8 subtests, TestScopeThresholdIsFractionOfAllPackagesNotReverseDepSet, TestParseThreshold×7 subtests, TestRealRepoSchemaChangeStaysScoped 10.80s real-repo smoke test) — identical count to the reviewer's own independent run. `gofmt -l scripts/ci/revdepscope/` clean. `go build ./...` (full module) clean, exit 0. |
| 3a | Pre-existing-failure attribution | **N/A** | 0 FAIL — nothing to attribute. Diff is purely additive (3 new files, 0 modifications to existing files), so no pre-existing test could be affected. |
| 3b | Policy / lint lane | **PASS (named exception)** | `make ci-pr-policy`: build-tag policy PASS, go-install guidance PASS, version-consistency FAILs solely on `.githooks/commit-msg` missing BEGIN/END BEADS INTEGRATION markers. Independently re-verified this file is **not a tracked repo file** (`git ls-files --error-unmatch` fails) and **is git-ignored** (`git check-ignore -v` → matches `.git/info/exclude:40`) — a per-session shim regenerated by `worktree-setup.sh`, not diff-related. Known environmental false positive, previously root-caused (be-jy56/be-jygq) and reconfirmed across be-p7dzx, be-y1jo, be-g3iz8, be-krza3; reconfirmed again here. `shellcheck scripts/ci/revdepscope/shadow-mode.sh`: only an info-level SC1091 (cannot follow the sourced `.buildflags` path outside `-x` mode) — same finding the reviewer documented, non-actionable. |
| 4 | No open HIGH findings | **PASS** | Reviewer's OWASP-style walk: no findings (blocker/major/minor) across all 10 categories. Independently re-confirmed the injection-relevant claim myself by reading the source directly: all 3 `exec.Command` calls (`main.go:83,102,116`) use argv-array form, zero shell-string concatenation; `grep -nE 'eval |sh -c|bash -c|http\.(Get\|Post)'` across all 3 files → no matches. No new dependencies (stdlib only). No credentials/PII/network I/O in scope. |
| 5 | Branch clean | **PASS** | `git status --short` empty after removing a stray `revdepscope` build artifact left by this gate's own independent `go build` run (not part of the diff — a local binary produced by the build command, `.gitignore`-eligible, never staged). |
| 6 | Diverges cleanly from main | **PASS (self-rebase applied)** | Deploy SHA `51750d3fa` was 1 commit behind `origin/main` (`1617f3a85`, unrelated `storage/dolt/*.go` follow-up — no path overlap with `scripts/ci/revdepscope/`). `assert_deploy_ancestry_scope origin/main 51750d3fa... be-mv0ww.3.1` → rc=0 pre-rebase. `PUSH_REMOTE=headfork attempt_bounded_self_rebase deploy/be-paugu-gate main` → rc=0, `BEFORE_SHA=51750d3fa53fc567c8955e29bca5442f7f68ab9c`, `AFTER_SHA=3cef8d53d89ea337bb1867142a9f04ec2505ff42`, and — unlike the be-krza3/be-z3iuv precedent where this function's internal push was hardcoded to the disabled `origin` remote — its internal force-with-lease push against `headfork` **succeeded** this round (the canonical script now honours `$PUSH_REMOTE`, confirmed by reading `rebase-resolve-lib.sh:502,572` directly). Landing independently verified via `git ls-remote headfork refs/heads/deploy/be-paugu-gate` → `3cef8d53d...`, matching local `HEAD` exactly. `git merge-base HEAD origin/main` now equals `origin/main`'s own tip (`1617f3a85`) — zero divergence. `assert_safe_push_target deploy/be-paugu-gate` → rc=0. |
| 7 | Single feature theme | **PASS** | All 3 commits (`tdd_red`, `tdd_green`, shadow-mode-harness) cite `be-mv0ww.3.1`; `assert_deploy_ancestry_scope` found no stray commits and no `.claude/**` paths. |

## Branch recreation note (this round)

This worktree's between-bead session-restart reset (`worktree-setup.sh
--reset-main`) silently reset the local `deploy/be-paugu-gate` branch pointer
back to `origin/main` before this gate's markdown/commit step had run in a
prior incarnation of this session — confirmed via `git reflog show
deploy/be-paugu-gate` (two entries only: branch-creation from `51750d3fa`,
then a bare `reset: moving to origin/main`; no commit in between, so no work
was lost). No remote copy existed yet (`git ls-remote fork/headfork/prhead`
all empty for this branch name pre-recreation), so nothing was prematurely
pushed either. Recreated cleanly via `resolve_deploy_branch_target "be-paugu"
51750d3fa53fc567c8955e29bca5442f7f68ab9c` and re-ran every criterion fresh
against the restored state before proceeding — same recovery pattern as
be-krza3's own gate file documents for an analogous reset.

## Push target

`origin` (`gastownhall/beads`) denies push
(`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR` sentinel); `headfork`
(`quad341/beads-sec003-contrib.git`) accepts. PR opens cross-repo against
`gastownhall/beads:main` with head `quad341:deploy/be-paugu-gate`.

## Merge authority

`gastownhall/beads` is a contributor-only repo for this rig — no rig agent
has merge access. Per established precedent (be-vc1m, be-gd3v, be-79jh,
be-39ss, be-pp7e, be-r3ysh, be-krza3, be-2ym7w), the deployer's job ends at
the open, verified PR. No merge-request is routed to mayor/mpr; gate result
reported to mayor via mail.

## Verdict

**PASS 7/7 (1 N/A, 1 named environmental exception)** — branch recreated
after a between-bead reset and re-verified from scratch, self-rebased cleanly
onto current main (push succeeded, landing independently confirmed), tests
and policy/lint independently re-run and matched against the reviewer's
results, security scan independently reconfirmed. Proceeding to push + open
PR.
