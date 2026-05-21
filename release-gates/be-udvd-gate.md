# Release Gate: be-udvd — error handling standardization

**PR**: https://github.com/gastownhall/beads/pull/4055
**Branch**: `fix/be-udvd-error-handling-standardize` (quad341/beads)
**Date**: 2026-05-21
**Deployer**: beads/deployer

## Gate Result: PASS

| # | Criterion | Evidence | Result |
|---|-----------|----------|--------|
| 1 | Review PASS present | be-kive: PASS — "Clean refactor; FatalError/FatalErrorWithHint signatures used correctly; JSON output path preserved" | ✅ PASS |
| 2 | Acceptance criteria met | See below | ✅ PASS |
| 3 | Tests pass | CI run 26184123931 — all 41 checks PASS | ✅ PASS |
| 4 | No high-severity findings | be-kive: no findings — mechanical transformation with no logic changes | ✅ PASS |
| 5 | Final branch is clean | `git status` — clean; only rig artifacts untracked | ✅ PASS |
| 6 | Branch diverges cleanly from main | 1 commit ahead of `origin/main`; merge state CLEAN | ✅ PASS |

## Acceptance Criteria Verification (be-udvd)

| Criterion | Evidence | Result |
|-----------|----------|--------|
| 127 `fmt.Fprintf(os.Stderr,...)+os.Exit(1)` pairs replaced with `FatalError`/`FatalErrorWithHint` | Reviewer confirmed "127 replacements"; `git diff --stat` shows changes across 12 `cmd/bd/` files | ✅ |
| Correct `FatalError(format, args...)` signature used | Reviewer confirmed "signatures used correctly throughout" | ✅ |
| `FatalErrorWithHint` callers pre-format message with `fmt.Sprintf` where needed | Reviewer confirmed | ✅ |
| JSON output path preserved | "errors.go wrappers handle jsonOutput flag" — reviewer confirmed | ✅ |
| No logic changes | "Mechanical refactor" — reviewer confirmed; only error emission pattern changed | ✅ |
| Remaining ~85 standalone `os.Exit` calls tracked as follow-up | bd-qioh filed for complex patterns (multi-fprintf loops, standalone exits after helper fns) | ✅ |

## Scope

12 files modified: `audit.go`, `compact.go`, `config.go`, `dolt.go`, `help_all.go`, `init.go`, `init_proxied_server.go`, `linear.go`, `ready.go`, `restore.go`, `search.go`, `sql.go`.

Excluded from this PR (tracked in bd-qioh): standalone `os.Exit` after functions that print themselves, multi-paragraph guidance messages, exit-code-only signals, `main.go` error handling.

## Commits

| SHA | Description |
|-----|-------------|
| `1a593ee78` | refactor(errors): replace fmt.Fprintf+os.Exit with FatalError* (be-udvd) |
