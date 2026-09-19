# Release gate — hermeticInitEnv metrics-guard race fix (be-xxwt)

- **Builder bead (CLOSED):** be-xxwt — `hermeticInitEnv` in
  `cmd/bd/init_safety_test.go` stripped the two fixed metrics-guard env vars
  (`BD_DISABLE_METRICS`, `BD_DISABLE_EVENT_FLUSH`) along with the ambient
  `BD_*`/`BEADS_*` config it's meant to filter, letting an isolated-HOME init
  test spawn a detached `bd send-metrics` child that could race the test's
  own `t.TempDir()` cleanup.
- **Deploy bead:** be-358n
- **Review bead:** be-6qzy — verdict **PASS**, recorded on commit
  `62ee12acc6f62a351cf6afef94214899aa859de7`
- **Commits:** `4e0c9c6a43db87c6bbeb082ebacc519e0ffa62a9` (test, red) then
  `62ee12acc6f62a351cf6afef94214899aa859de7` (fix, green), 1 file over
  `origin/main`
- **Branch:** `builder/be-xxwt` (provenance only) → deploy branch
  `deploy/be-358n-gate` cut from `62ee12acc6f62a351cf6afef94214899aa859de7`,
  pushed to `headfork` (`quad341/beads-sec003-contrib`) — same
  rename-redirect avoidance as the be-uoat gate (`fork`/`quad341/beads` now
  redirects there).
- **Evaluated:** 2026-08-15 by beads/deployer

## Scope

`hermeticInitEnv` builds an isolated subprocess environment for the three
tests in `cmd/bd/init_safety_test.go` by copying `os.Environ()` and dropping
every `BD_*`/`BEADS_*`-prefixed var, so ambient user/CI beads config can't
leak into init behavior. That filter also removed the two metrics guards
`cmd/bd`'s `TestMain` installs package-wide
(`test_repo_beads_guard_test.go:139-140`) — `hermeticInitEnv` was the one
call site in the package that rebuilt its env from scratch instead of
appending to an already-guarded one. With both guards absent and `HOME`
pointed at a fresh `t.TempDir()`, `resolveMetricsEnabled()` returns true,
`metrics.Init` creates `$HOME/.beads/eventsData`, and a detached
`bd send-metrics` child can still be writing there when the test's
`t.TempDir()` cleanup runs `RemoveAll`.

Diff scope, confirmed via `git diff --name-only origin/main...HEAD` (1 file):

- `cmd/bd/init_safety_test.go` — re-adds the two guard vars before the
  function's existing `extra...` append, updates the doc comment to explain
  why they survive the strip, and adds a deterministic regression assertion
  (`eventsData` must not exist under the isolated HOME) to
  `TestInitFreshWithUnreachableGitOriginSucceeds`.

No production code touched — `internal/metrics/spawn.go` and the detached
metrics-flusher behavior are intentionally unchanged; this is a test-harness
fix, matching the fix spec's explicit "out of scope" list (no
`BEADS_TEST_MODE`, no widening the `BD_`/`BEADS_` strip filter, no
production metrics changes).

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-6qzy closed, `close_reason: pass`, notes record `verdict: pass` with `deploy_commit` matching exactly. |
| 2 | Acceptance criteria met | **PASS** | All functional done-when items confirmed directly against the diff: guard vars appended before `extra...`, doc comment updated, regression assertion added exactly as specified. |
| 3 | Tests pass | **PASS** | See "Tests run" below — independently re-verified, zero failures. |
| 4 | No unresolved HIGH findings | **PASS** | Review notes record zero findings of any severity. |
| 5 | Clean working tree | **PASS** | `git status` on the evaluated commit shows no staged/unstaged changes (only the pre-existing, unrelated untracked `release-gates/be-uoat-*.md` and `scripts/rebase-resolve-lib.sh` scratch files, never staged). |
| 6 | Clean divergence from `origin/main` | **PASS** | `git merge-base --is-ancestor origin/main HEAD` succeeds; HEAD is exactly the two commits (red + green) on top of `origin/main`. No rebase needed. |
| 7 | Single feature theme | **PASS** | One file, every hunk serves the one guard-restoration fix. No unrelated changes riding along. |

## Tests run on release branch (independent re-verification)

Static checks, independently re-run rather than trusted from the builder's
or reviewer's reports:

| Check | Result |
|---|---|
| `go build ./...` | clean, rc=0 |
| `go vet ./...` | clean, rc=0 |
| `gofmt -l` on the diff file | clean, 0 files listed |

Diff-owned tests (`-run 'TestInitFresh\|TestInitReinitLocal'`, 3 functions,
`./cmd/bd/`, matching the fix spec's own done-when scope):

| Run | Result |
|---|---|
| count=1, verbose | 3/3 PASS (32.3s) |
| count=10 | 30/30 PASS (81.2s), `ok` |

Zero "TempDir RemoveAll cleanup" failures across 33 independent
executions here. Combined with the builder's own quick-gate (count=5, 5/5
PASS) and the reviewer's soak (count=30 launched, 25/90 sub-iterations
complete at handoff, 0 FAIL, mechanism assessed as deterministic not
timing-based since `BD_DISABLE_METRICS=1` closes `resolveMetricsEnabled()`
before `eventsData` is ever created), this is ~63 independent zero-failure
executions against a baseline that reproduced the race in 1 of 20 clean-main
runs — strong, multi-source evidence the fix is structural rather than a
timing narrowing.

## Findings from review (no action required)

be-6qzy records zero HIGH/MEDIUM/LOW findings. One non-blocking aside in the
review notes: a courtesy `gc mail send` to `beads/reviewer` failed twice with
a named-session conflict; the reviewer flagged it for visibility only since
routing itself was independently verified via `gc sling` (routed=beads/reviewer,
ready=1). No deployer action required.

## Verdict

**PASS** — all 7 criteria pass. Deploy branch `deploy/be-358n-gate` pushed to
`headfork`; PR opened against `gastownhall/beads` main. `gastownhall/beads` is
a repo this team contributes to but does not maintain, so per protocol this
job stops at the opened PR — no merge-request routing, no `gh pr merge`.
