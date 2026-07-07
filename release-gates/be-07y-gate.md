# Release gate — be-07y (be-035 close authority guard)

- **Bead:** be-07y (review bead for fix of be-035, P1 silent data loss)
- **Commit shipped:** 3dd4375c (cherry-pick of 7a925e304 from `users/jaword/postgres-backend`)
- **Branch:** `release/be-07y-gate` off `origin/main`
- **Evaluated:** 2026-05-05 by beads/deployer

## Scope note

The builder committed `7a925e304` on top of `users/jaword/postgres-backend`, a
branch that carries ~10 unrelated postgres-backend commits explicitly parked as
LOCAL-ONLY (be-7yt, be-b0h, be-ry0, be-2oq, be-xz4, be-y8g, be-6fk.x, …).
Shipping the builder's branch directly would drag those parked commits into
the PR. To keep the unit-of-review tight, this gate cuts a clean branch off
`origin/main` and cherry-picks only `7a925e304`. Resulting commit `3dd4375c`
introduces a delta byte-identical to the reviewed commit (verified via
`diff <(git show 7a925e304 -- <files>) <(git show HEAD -- <files>)` — only
commit/blob SHAs differ; patch hunks are identical).

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | beads/reviewer recorded `PASS` verdict in be-07y notes (single-pass; gemini second-pass disabled). Three non-blocking findings (LOW-1 filed as be-po9, LOW-2 observability, INFO-1 scope choice). |
| 2 | Acceptance criteria met | **PASS** | All four be-035 criteria covered by the test set. (1) Authority-mismatch reproducer in `TestEmbeddedClose/close_assignee_mismatch_refuses_without_force` asserts non-zero exit, stderr names actor/assignee, bead remains open. (2) Concurrent two-actor scenario covered by claim/close pair in same sub-test. (3) Refused close exits non-zero (`os.Exit(1)` at `cmd/bd/close.go:277` when `closedCount==0`). (4) Clear stderr message format `cannot close %s: assignee is %q, actor is %q; reclaim or use --force to override`. |
| 3 | Tests pass | **PASS** | See "Tests run on release branch" below. Three flaky failures observed under full-suite parallelism are pre-existing baseline (reproduce on plain `origin/main`); not regressions. |
| 4 | No high-severity review findings open | **PASS** | 0 HIGH findings. LOW-1 → follow-up be-po9 (P2). LOW-2 + INFO-1 advisory. |
| 5 | Final branch is clean | **PASS** | `git status` on `release/be-07y-gate` shows nothing except worktree-scaffolding untracked paths (`.gc/`, `.gitkeep`). |
| 6 | Branch diverges cleanly from main | **PASS** | Branch cut fresh from `origin/main` via `git checkout -B release/be-07y-gate origin/main`; `git cherry-pick 7a925e304` applied with auto-merge on 4 files (context-only; verified delta is byte-identical to the original commit). |

## Tests run on release branch

| Test | Result | Notes |
|------|--------|-------|
| `TestAssigneeMatches` (`internal/validation/`, plain go test) | PASS 0.003s | All 6 sub-tests: nil_issue, unassigned, matching, mismatched_no_force, mismatched_with_force, empty_actor. |
| `TestValidateIssueClosable` (`cmd/bd/`, plain go test) | PASS 0.159s | Validator chain wiring with new `actor` parameter. |
| `TestEmbeddedClose` (`cmd/bd/`, `BEADS_TEST_EMBEDDED_DOLT=1`, `-tags 'cgo dolt_only'`) | PASS 65.05s | All 35 sub-tests, including the 4 new ones: `close_assignee_mismatch_refuses_without_force`, `close_assignee_mismatch_with_force`, `close_same_actor_succeeds`, `close_unassigned_bead_succeeds`. |
| `TestEmbeddedCloseConcurrent` (same tags) | PASS 65.57s | 50 issues × 10 concurrent workers; 50 in DB. |
| `go vet -tags gms_pure_go ./internal/validation/... ./cmd/bd/` | clean | No output. |
| `golangci-lint run --new-from-rev=origin/main ./...` | clean | 0 new issues. |
| `make test` (full suite) | 3 flaky failures, all baseline | `TestPullWithAutoResolve_BranchTrackingFallback` (nil DB panic in cleanup, full-suite contention only — passes in isolation), `TestResolvePartialID_Wisp/wisp_prefix_with_hash` (passes in isolation), `TestApplyConfigDefaults_ProductionFallback` (port-resolution flake — reproduces on plain `origin/main` with no cherry-pick applied). None touch close.go, validation/issue.go, or any path in the be-07y delta. |

## Findings tracked from review

**LOW-1 (defense-in-depth — follow-up filed):** `bd update --status=closed`
bypasses the new `AssigneeMatches` guard because it routes through
`issueStore.UpdateIssue` directly without `validateIssueClosable`. Same
silent-data-loss class as be-035 via a different command surface. Builder
scoped fix to `bd close` per the be-035 ticket; reviewer filed **be-po9**
(P2 bug, `discovered-from be-07y`) for the parallel surface. Not a release
blocker.

**LOW-2 (observability — advisory):** Refused authority attempts log to
stderr but produce no `audit.LogFieldChange` entry. Optional improvement
for automation-heavy environments. Not in be-035 acceptance, not a
blocker.

**INFO-1 (scope choice — accepted):** be-035 mentioned "owner/admin
override"; the fix substitutes `--force` for that override. The narrowing
is sensible given bd's actual auth model (no role-based authorization;
`Owner` is a free-form CV-chain field). `--force` is the established
override pattern (NotPinned, gate-satisfaction, blocker bypass). Builder
flagged the substitution explicitly; reviewer accepted.

## Verdict

**PASS** — push to `fork` (origin is locked for quad341; `fork =
quad341/beads`), open PR against `gastownhall/beads:main`.
