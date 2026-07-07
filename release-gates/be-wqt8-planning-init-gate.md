# Release Gate: be-wqt8 — fork auto-config planning store init fix

**Bead:** be-chhm (deploy bead for be-mx1y / be-wqt8)  
**Branch:** fix/be-wqt8-planning-init  
**Cherry-picked from:** feat/be-3w6-be-0c8-nopush-dolt  
**Commit:** 3cb77b014 (cherry-pick of 1d9c835a2)  
**Date:** 2026-06-02  

## Gate Result: PASS

| # | Criterion | Status | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-mx1y: reviewer PASS verdict in bead notes. All 3 acceptance criteria verified by reviewer. |
| 2 | Acceptance criteria met | PASS | See below |
| 3 | Tests pass | PASS | `make test` exit 0 on fresh branch off origin/main. Coverage 37.3%. |
| 4 | No high-severity review findings open | PASS | Reviewer posted 5 findings, all PASS. Zero HIGH or MEDIUM severity findings. |
| 5 | Final branch is clean | PASS | `git status` clean, working tree clean, nothing to commit. |
| 6 | Branch diverges cleanly from main | PASS | Cherry-picked cleanly onto origin/main (fbcee6c62). No conflicts. |
| 7 | Single feature theme | PASS | Single commit touching `cmd/bd/init_contributor.go` and `cmd/bd/init_embedded_test.go`. One subsystem (fork auto-config init path). |

## Acceptance Criteria

1. **Planning Dolt schema initialized during fork auto-config** → SATISFIED  
   `init_contributor.go:331-337`: `newDoltStoreFromConfig` called on `planningBeadsDir` immediately after creation. CGO builds initialize schema; no-CGO builds skip silently (non-fatal).

2. **YAML-configured contributor setup covered by idempotency guard** → SATISFIED  
   `init_contributor.go:295-303`: new check on `config.GetValueSource("routing.contributor")` catches `SourceConfigFile` and `SourceEnvVar` — correctly broader than DB-only check.

3. **Regression test exercises the fix** → SATISFIED  
   `init_embedded_test.go:369-373`: asserts `embeddeddolt` dir exists after `bd init` on fork, proving `initSchema` ran. Gated `//go:build cgo`. No-CGO builds compile clean.

## Test Notes

- `make test` passes clean on a fresh worktree off `origin/main` with this commit cherry-picked.  
- The builder worktree's `../../bd` binary (v1.0.4, no-CGO, built May 20) caused unrelated test failures when tests ran there — these are NOT regressions from this commit. Verified by running the same tests in a temp worktree at both `31e416872` (parent) and `1d9c835a2` (this commit) without the stale binary, both pass.

## Deploy Notes

The source branch `feat/be-3w6-be-0c8-nopush-dolt` fork remote has diverged from the local branch (non-fast-forward; builder rebase not pushed to fork). The commit deploys cleanly as a standalone cherry-pick because it is independent of the no-push feature in that branch. PR #4212 continues to track the no-push feature separately.
