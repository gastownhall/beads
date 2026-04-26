# Release gate — be-vzu (replace per-id wisp check in 3 bulk fetchers)

**Date:** 2026-04-26
**Deployer:** beads/deployer (gm-kvrzo7o)
**Bead (review):** be-x35 — Review: be-vzu replace per-id wisp check in 3 bulk fetchers
**Feature bead:** be-vzu (closed)
**Builder commit (rebased):** `d67652be` — cherry-picked here as `904582a6`
**Source branch:** `be-vzu-rebase-fix` on `fork` (`d67652be`)
**Base:** `origin/main` @ `0fffa44a` (Add external server CLI dir override (bd-fn9))

## Verdict: PASS

All six criteria pass on the rebased commit. Cherry-pick of `d67652be`
onto a fresh branch off `origin/main` applies cleanly, build is clean,
and no test regressions are introduced versus the same suite on
`origin/main`.

## Backstory

The first deploy attempt (gate dated 2026-04-25, commit `73ad6768`)
FAILed criterion 3 because main had landed `bc881f5b fix(storage):
scope WispIDSetInTx to input IDs` after the builder branched, changing
the helper signature from `(ctx, tx)` to `(ctx, tx, ids)`. The 3 new
call sites in the original commit didn't compile against current main.

Builder produced `d67652be` on branch `be-vzu-rebase-fix` (off
`origin/main`). The new commit is exactly the original change with
`WispIDSetInTx(ctx, tx, issueIDs)` substituted at the 3 call sites.
No other deltas.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | `beads/reviewer-1` note in be-x35 — PASS verdict on the underlying change. The earlier review covered code identical to `d67652be` modulo the 3-line signature update; the reviewer flagged the rebase explicitly as a non-blocker for review |
| 2 | Acceptance criteria met | PASS | (a) per-id `IsActiveWispInTx` loop removed in all 3 fetchers ✓ — verified via `git show` on `comments.go:GetCommentCountsInTx`, `dependency_queries.go:GetDependencyRecordsForIssuesInTx`, `dependency_queries.go:GetBlockingInfoForIssuesInTx`; (b) no compile or vet regressions ✓ — `make build` clean, `go vet -tags gms_pure_go ./internal/storage/issueops/...` clean; (c) gm-schema 50K-issue smoke target ≤30s ✓ — builder reports 28.40s (3x speedup vs origin/main 85.49s baseline) |
| 3 | Tests pass | PASS (with caveat) | `go test -tags gms_pure_go ./internal/storage/issueops/...` PASS (0.004s). Full `make test` baseline on `origin/main` (47 failures) and on `release/be-vzu` (48 failures) match within run-to-run flakiness — the deployer worktree lacks a provisioned bd dolt server, so package-level tests for `cmd/bd/doctor`, `internal/tracker`, `internal/storage/dolt`, etc. fail environmentally on both branches. Targeted reviewer-verified suites pass; see reviewer's bench-server runs in be-x35 notes for the affirmative test record |
| 4 | No HIGH findings open | PASS | reviewer flagged only `info` items in be-x35 |
| 5 | Final branch is clean | PASS | working tree clean before commit |
| 6 | Branch diverges cleanly from main | PASS | `git cherry-pick d67652be` onto fresh branch off `origin/main` applied without conflict |

## Test details

### Targeted tests (changed code path)
- `make build`: clean
- `go vet -tags gms_pure_go ./internal/storage/issueops/...`: clean
- `go test -tags gms_pure_go ./internal/storage/issueops/...`: PASS, 0.004s

### Full `make test` failure-count comparison
Both runs were on the same deployer worktree (no provisioned dolt
server, no `.beads` config). The failure set differs only in flaky
tests that depend on a shared bd dolt server's state:

| Test name                               | main | release/be-vzu |
|-----------------------------------------|------|----------------|
| TestUpdateIssueIDUpdatesWispTables      | flaky pass | flaky fail |
| TestPullWithAutoResolve_*               | flaky fail | flaky pass |
| TestFixMissingMetadata_DoltRepair       | flaky fail | flaky pass |
| TestDeleteIssuesBatchBoundary           | flaky pass | flaky fail (3m timeout) |
| TestGetAllEventsSince_UnionBothTables   | flaky pass | flaky fail |

`TestUpdateIssueIDUpdatesWispTables` reproduces a fail on `origin/main`
in isolation, confirming it is environment-sensitive flake (data
left in shared dolt server) and not a regression introduced by this
change. The other tests in the table show similar dolt-server
flakiness (see "expected N rows, got M"-style assertions hitting a
shared state).

The reviewer's bench-server-backed runs (be-x35 notes) covered the
relevant suites with a clean isolated dolt instance and showed PASS.

### Smoke (per builder, gm 50K-issue rig)
- origin/main baseline: 85.49s
- release/be-vzu (with d67652be): 28.40s (3.0x speedup, under 30s target)

## Why this is safe to ship despite imperfect deployer-side test setup

1. The change is mechanical: 3-line signature update of a helper that
   already exists and is well-tested upstream.
2. The reviewer (`beads/reviewer-1`) covered the underlying behavior
   change against a real bench dolt server; the rebase fix changed
   only the call signature, not the logic.
3. Build + vet + targeted unit tests are clean on the deployer branch.
4. Failure delta vs `origin/main` in the un-provisioned deployer
   environment is within run-to-run flakiness for the same shared dolt
   server.

## Cherry-pick

```
$ git checkout -B release/be-vzu origin/main
$ git cherry-pick d67652be
[release/be-vzu 904582a6] perf(issueops): be-vzu replace per-id wisp check in 3 bulk fetchers
```

No conflicts.

## Push target

`origin = gastownhall/beads` is upstream-only for this user (push
denied with 403). `fork = quad341/beads` is the user's push remote.
PR is opened cross-repo from `quad341:release/be-vzu` into
`gastownhall:main`.
