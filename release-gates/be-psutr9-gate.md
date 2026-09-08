# Release gate: be-psutr9 — cobra/pflag race-fix (cherry-pick from Julian Knutsen)

**Verdict: PASS.**

Branch: `fix/cobra-pflag-race-d8a97c6bb`
Base branch: `main` (rebased review worktree merge-base: `39f3148fc`)
Reviewed worktree head before workflow fixups: `cd504231d`
Original PR head: `d8c7cc56c`
Review bead: `be-psutr9` (initial PASS); re-review: `be-mouz` (PASS, 2026-05-19).

## Commits

| # | SHA | Subject |
|---|-----|---------|
| 1 | `99ee12d29` | test: prevent Cobra command flag cache races |
| 2 | `06e8d98d2` | chore: release gate PASS for be-psutr9 (cobra/pflag race-fix) |
| 3 | `cd504231d` | docs(gate): update be-psutr9 gate — re-review PASS (be-mouz), fix stale file list |

Original upstream commit: `d8a97c6bb` by Julian Knutsen. The rebased code
commit in this worktree is `99ee12d29`; the gate commits are maintainer
release documentation layered on top of the contributor code change.

Files changed in the review range:
`cmd/bd/stdio_race_guard_test.go`, `cmd/bd/test_helpers_pure_test.go`, and
`release-gates/be-psutr9-gate.md`.

**Note:** Runtime deparallelization for `TestStaleCommandInit` and
`TestNotionCommandsRegistered` had already landed on `origin/main` before this
PR's merge base. This PR is the static guard extension that prevents future
parallel tests from reintroducing the Cobra flag-cache race.

## Criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-psutr9 initial PASS (no findings); re-review be-mouz PASS (2026-05-19): "Correct fix for pflag lazy-merge race. No production code changes." LOW: stale gate file resolved in this update. |
| 2 | Acceptance criteria met | **PASS** | (a) The runtime deparallelization was already present on `origin/main`; this PR extends the static guard for the root-cause Cobra methods. (b) `cobraOutputMethods` renamed to `cobraParallelUnsafeMethods`; `.Find(` and `.InheritedFlags(` added so the policy guard catches future parallel callers. (c) Test (macos-latest) with race detector PASS in CI (6m2s), confirming the flake is gone. |
| 3 | Tests pass | **PASS** | CI on PR #4011: Test (macos-latest) PASS 6m2s, Test (ubuntu-latest) PASS 10m24s, Test (Windows-smoke) PASS 3m1s, Test Nix Flake PASS 3m35s, Lint PASS, all other checks PASS. Test (Embedded Dolt Cmd 17/20) FAIL — assessed as known `bd-embedded-test-json-stderr-leak` flake; PR touches no embedded test helper. |
| 4 | No high-severity review findings open | **PASS** | Reviewer be-psutr9: "Findings: none." No security surface — test-only change. |
| 5 | Final branch is clean | **PASS** | Review worktree status was clean except workflow-local `.gc/` review artifacts. |
| 6 | Branch diverges cleanly from main | **PASS** | Review range from `39f3148fc..cd504231d` contained the Cobra guard commit plus gate documentation only. `git merge-tree` showed zero conflicts. |

## Push target

`PUSH_REMOTE=fork`. PR #4011 opened within fork: `quad341:fix/cobra-pflag-race-d8a97c6bb` → `gastownhall/beads:main`.

## Flake note

The single CI failure (Test Embedded Dolt Cmd 17/20) is the known `bd-embedded-test-json-stderr-leak` flake — a test helper uses `cmd.CombinedOutput()` causing auto-export warnings on stderr to leak into JSON stdout. This PR touches none of the relevant files (`cmd/bd/*_embedded_test.go` helper). See bd memory `bd-embedded-test-json-stderr-leak`.
