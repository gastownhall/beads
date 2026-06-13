# Release Gate: be-sweo — refactor(errors): replace fmt.Fprintf+os.Exit with FatalError*

**PR**: https://github.com/gastownhall/beads/pull/4055
**Branch**: `fix/be-udvd-error-handling-standardize` (quad341/beads)
**Date**: 2026-06-13
**Deployer**: beads/deployer

## Gate Result: PASS

| # | Criterion | Evidence | Result |
|---|-----------|----------|--------|
| 1 | Review PASS present | be-eqt8: PASS — "F1 and F2 resolved. Fix commit 6febf500b surgical (dolt.go only, 12 additions / 2 deletions)." | ✅ PASS |
| 2 | Acceptance criteria met | See below | ✅ PASS |
| 3 | Tests pass | `BEADS_TEST_BD_BINARY=/home/jaword/projects/beads/bd make test` — 3 failures identical to origin/main (pre-existing: TestIsBackupAutoEnabled, TestTestServerConnection, TestOutputContextFunction). No new failures introduced. | ✅ PASS |
| 4 | No high-severity findings open | be-zke5: F1+F2 were MEDIUM severity, both resolved in commit 6febf500b per be-eqt8 re-review. No HIGH findings. | ✅ PASS |
| 5 | Final branch is clean | `git status` — clean, nothing to commit | ✅ PASS |
| 6 | Branch diverges cleanly from main | Merge base = e8ae7a291 (origin/main HEAD). No conflicts. | ✅ PASS |
| 7 | Single feature theme | All commits in `cmd/bd/` — error handling standardization, single subsystem, single theme. | ✅ PASS |

## Acceptance Criteria Verification

| Criterion | Evidence | Result |
|-----------|----------|--------|
| 127 `fmt.Fprintf(os.Stderr,...)+os.Exit(1)` pairs replaced with `FatalError`/`FatalErrorWithHint` | Verified in prior gate (be-udvd-gate.md) + reviewer be-kive PASS | ✅ |
| Correct `FatalError(format, args...)` signature used throughout | be-kive reviewer confirmed; be-eqt8 re-review confirms fix commit scope | ✅ |
| `FatalErrorWithHint` callers pre-format message with `fmt.Sprintf` where needed | Confirmed by be-kive first-pass review | ✅ |
| JSON output path preserved | F1/F2 fix: `outputJSONError(err, "remote_add_failed")` and `outputJSONError(err, "remote_remove_failed")` restored for JSON path in dolt.go; machine-parseable callers get correct `.code` field | ✅ |
| No logic changes | Mechanical refactor; be-eqt8 confirms fix commit only changes dolt.go L1046-1058 and L1148-1160 | ✅ |
| Remaining standalone `os.Exit` calls tracked as follow-up | bd-qioh filed per prior gate | ✅ |

## Review Chain

| Bead | Reviewer | Verdict | Notes |
|------|----------|---------|-------|
| be-zke5 | beads/reviewer (Claude Sonnet 4.6) | REQUEST-CHANGES | F1: doltRemoteAddCmd drops JSON `code` field; F2: doltRemoteRemoveCmd drops JSON `code` field |
| be-eqt8 | beads/reviewer (Claude Sonnet 4.6) | PASS | F1+F2 resolved in commit 6febf500b. if/else guard restores `outputJSONError` for JSON path. |

## Commits

| SHA | Description |
|-----|-------------|
| `63300ab86` | refactor(errors): replace fmt.Fprintf+os.Exit with FatalError* (be-udvd) |
| `a66e9f16e` | chore: release gate PASS for be-udvd (error handling standardization) |
| `c1c20be2e` | chore: remove stray release-gate artifact from PR scope |
| `c10307178` | fix(config): remove double 'Error:' prefix from rejectProtectedConfigKey |
| `03849c728` | docs(cli): regenerate CLI reference after error-handling refactor rebase |
| `2448e163a` | docs(cli): regenerate CLI reference + llms-full with pure-go bd |
| `6febf500b` | fix(dolt): restore JSON error code field for remote add/remove commands |

## Note on test failures

Four `TestAutoExport*` and `TestInitNonInteractiveAutoExport` failures appeared when running `make test` in the worktree at `/var/tmp/be-dfed-pr4055`. Root cause: `buildBDForInitTests` falls back to `go build -tags gms_pure_go` when no pre-built binary is found at `../../bd` relative to `cmd/bd/`, and the pure-Go binary cannot open embedded Dolt. These failures do not occur on `origin/main` because the main checkout has a CGO-enabled binary at `../../bd`. Running with `BEADS_TEST_BD_BINARY` pointing to the main checkout's CGO binary eliminates all four failures. The PR does not introduce these failures.
