# Release gate — be-764ey (Implement Phase 2 dual-write history behind flag - gastownhall/beads#6135)

**Date:** 2026-09-07
**Deployer:** beads/deployer
**Bead (deploy):** be-764ey
**Source bead:** be-lr5hd — status closed (2026-09-07T16:53:32Z), verdict `pass` (round 2). Review went two rounds: round 1 requested changes — the branch base lacked Phase 0's head, an independently dispositive checklist miss regardless of the tests_green finding. Fixed by re-seating onto the merged local base `42838f700` (Phase 1 `c95247e61` + Phase 0 `9c4e7a8f1`) via be-v33pa. Round 2 (this commit): gofmt/vet clean, full OWASP-Top-10-style security walk (9 categories, all PASS), 8/8 diff-owned tests PASS (reviewer's own count), `uncovered_criteria: none`.
**Source commit:** `c249019dafcd6aed08f483980ffdcdea9bca806f`
  - Parent: `e2dc9d3ce` (test(issueops): red — re-seat + attribution_status, refs be-v33pa)
  - Branch history: `715eac381` (red, refs be-0uifx) → `1f71e6ed0` (green, refs be-0uifx) → `e2dc9d3ce` (red, refs be-v33pa) → `c249019da` (green, refs be-v33pa)
  - Stacked on: `42838f700` (merge commit, Phase 1 `c95247e61` + Phase 0 `9c4e7a8f1` into `merge-base/be-v33pa`) — i.e. this branch carries Phase 0 (#6147, be-hs42e.1) and Phase 1 (#6304, be-dy66n/be-hs42e.2) commits in addition to its own 4 Phase-2 commits, per mayor's explicit standing instruction to stack rather than wait for #6304/#6147 to merge first.
  - Base: `origin/main` @ `c0d8da42de5fd15c95adac85e342ba4a121da0fb`. Re-confirmed via fresh `git fetch origin main` immediately before writing this gate: `origin/main` tip and `merge-base(HEAD, origin/main)` are identical — origin/main has not moved since the branch was cut. `git rev-list --left-right --count origin/main...HEAD` → `0  17` (0 behind, 17 ahead).
**Branch:** `deploy/be-764ey-gate`
**Push target:** `headfork` (`quad341/beads-sec003-contrib`) — pushed and independently re-verified via the opened PR's `headRefOid` (see below), matching local `HEAD` exactly.
**PR:** [gastownhall/beads#6358](https://github.com/gastownhall/beads/pull/6358) — `quad341:deploy/be-764ey-gate` → `gastownhall:main`. Verified via `gh pr view 6358`: `state=OPEN`, `isDraft=false`, `mergeable=MERGEABLE`, `headRefOid=c249019dafcd6aed08f483980ffdcdea9bca806f` (exact match), `headRepositoryOwner=quad341` (our own account — not an external contributor, no human-hold triggered). **Supersedes** the pre-existing draft [#6354](https://github.com/gastownhall/beads/pull/6354) (opened at this identical commit for reading only) per mayor's explicit standing instruction recorded in be-764ey's notes; PR body states the supersession and reuses #6354's implemented/not-yet callouts verbatim.

## Verdict: 7/7 — PASS, no waivers

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review pass | PASS | be-lr5hd closed, verdict `pass` (round 2, after a round-1 base-ancestry fix via be-v33pa) |
| 2 | Acceptance criteria met | PASS | All 4 converge: issue #6135's write-path-regression-benchmark acceptance item is absent from design doc be-hs42e.3's FR-1..FR-10, explicitly disclosed in this PR's own "Not in this head yet" list, and the reviewer's own `uncovered_criteria: none` — a mayor/design-authorized deferral, not a gap. All diff-owned tests confirmed passing (see Criterion 3) |
| 3 | Full-suite tests | PASS | See Criterion 3 detail below — independent run, zero diff-owned or pre-existing failures; two pre-existing non-blocking lint/policy findings attributed to trackers |
| 3a | Pre-existing-failure attribution | PASS (N/A) | Zero test failures occurred in this run at all — no failure classes to attribute. (Contrast be-dy66n's gate, which had 4.) The known be-c9mwp TMPDIR-isolation condition also did not reproduce here |
| 3b | Policy/lint lane (`ci-pr-policy` + `ci-pr-lint`) | PASS (attributed) | 2 pre-existing, non-diff-owned findings attributed to trackers; see Criterion 3 detail |
| 3c | CI-config-diff live-run | N/A | Diff touches no `.github/` or `scripts/ci/` files (`git diff --name-only origin/main...HEAD` confirms) |
| 4 | Zero open HIGH | PASS | Reviewer's OWASP walk: 9 categories, all PASS. Only three beads link to this review chain at all — be-c9mwp (pre-existing, non-blocking, didn't reproduce), be-2l6n8 and be-xzg59 (self-filed this run, both confirmed pre-existing via clean-`origin/main` reproduction, both non-blocking). No HIGH-severity findings open against this diff |
| 5 | Clean git status | PASS | `git status --short` clean on `deploy/be-764ey-gate` at SHA `c249019da`, re-confirmed immediately before writing this gate |
| 6 | No merge conflicts with BASE_REF | PASS | `origin/main` unchanged since branch cut; merge-base == origin/main tip; no conflict possible |
| 7 | Single feature theme/ancestry scope | PASS | Phase-2-only diff (`42838f700...c249019da`): 29 files, all under `backend/conformance/` + `internal/storage/{dolt,domain,embeddeddolt,issueops,schema,uow}`, consistent dual-write theme. The additional Phase 0 + Phase 1 commits carried by this branch are an intentional, mayor-authorized stack, not scope creep (see diffstat below) |

**Diffstat — Phase 2 only** (`git diff --stat 42838f700...c249019da`, matches PR #6354's own stated figures exactly):
```
 backend/conformance/dualwrite_history_contract.go  | 344 +++++++++++++++++++++
 backend/conformance/role_bundle.go                 |   1 +
 backend/conformance/role_bundle_cases.go           |  18 ++
 internal/storage/dolt/dualwrite_history_contract_test.go        | 226 ++++++++++++++
 internal/storage/dolt/initschema_0067_versioned_beads_replay_test.go |   4 +-
 internal/storage/dolt/store.go                     |  29 +-
 internal/storage/domain/db/issue.go                |  15 +-
 internal/storage/embeddeddolt/dependency_editor_version_history_test.go | 113 +++++++
 internal/storage/embeddeddolt/dualwrite_history_contract_test.go        |  133 ++++++++
 internal/storage/embeddeddolt/store.go             |  13 +
 internal/storage/issueops/create.go                |   3 +
 internal/storage/issueops/dependency_editor.go     |  14 +
 internal/storage/issueops/journal_completeness_test.go |  20 +-
 internal/storage/issueops/row_lock_guard_test.go   |  14 +
 internal/storage/issueops/update.go                |   3 +
 internal/storage/issueops/version_history.go       | 147 +++++++++
 internal/storage/issueops/version_history_test.go  |  60 ++++
 internal/storage/schema/cli_migrations.go          |  14 +
 internal/storage/schema/migration_0067_versioned_beads_test.go  |  21 +-
 internal/storage/schema/migration_0068_add_attribution_status_test.go |  130 ++++++++
 internal/storage/schema/migrations/0068_add_attribution_status.down.sql |  24 ++
 internal/storage/schema/migrations/0068_add_attribution_status.up.sql   |  44 +++
 internal/storage/schema/schema_test.go             |   6 +
 internal/storage/storage.go                        |  10 +
 internal/storage/uow/dolt_sql_provider.go          |  29 +-
 internal/storage/uow/doltserver_tx.go              |  15 +
 internal/storage/uow/dualwrite_history_contract_test.go |  136 ++++++++
 internal/storage/uow/notifying.go                  |  10 +
 internal/storage/uow/notifying_parity_test.go      |  12 +-
 29 files changed, 1566 insertions(+), 42 deletions(-)
```

**Diffstat — full branch, incl. stacked Phase 0 + Phase 1** (`git diff --shortstat origin/main...HEAD`):
```
48 files changed, 5020 insertions(+), 39 deletions(-)
```

## Criterion 3 — full-suite test evidence + policy/lint lane + CI-config-diff (3c)

**test_cmd:** `go test -p 4 -parallel 4 -timeout 25m -covermode=atomic -coverprofile /tmp/beads.coverage.out ./...` (invoked via `TEST_COVER=1 ./scripts/test.sh`, i.e. `make test`)
**test_cmd_scope:** full-suite (`./...`, no `BEADS_TEST_SKIP`; `TMPDIR`/`GOTMPDIR` deliberately unset — see attribution note below)
**test_counts:** 97 packages `ok`, 0 `FAIL`, 0 `SKIP` (the one case-insensitive "skip" hit in the log is the harness's own empty `Skipping: ` preamble line, not a per-package result). Exit code 0. Total coverage 39.2%.

**diff_tests_executed** (12 diff-owned test files, mapped by name; all PASS by package-level entailment — every listed package reports `ok` with 0 failures in the full-suite run):

- `internal/storage/dolt/dualwrite_history_contract_test.go` (new) — `TestDualWriteContract`, `TestDualWriteContractFlagOff`, `TestDualWriteFixtureKitIsWired`, `TestDualWriteStampsTheCurrentStoreEpochOnEachVersionRow`
- `internal/storage/dolt/initschema_0067_versioned_beads_replay_test.go` (modified) — `TestMigration0067ReplaysIdempotentlyAsRawSQLThroughPR4107Harness` (extended)
- `internal/storage/embeddeddolt/dependency_editor_version_history_test.go` (new) — `TestEmbeddedDependencyEditorVersionsOnlyARealAddOrRemove`
- `internal/storage/embeddeddolt/dualwrite_history_contract_test.go` (new) — `TestDualWriteContract`, `TestDualWriteContractFlagOff`, `TestDualWriteFixtureKitIsWired`
- `internal/storage/issueops/journal_completeness_test.go` (modified, exemption tables extended) — `TestEveryMutationFunctionJournals`, `TestExemptMutationsDoNotJournal`, `TestEveryBeadMutatorJournalsOrIsExempt`
- `internal/storage/issueops/row_lock_guard_test.go` (modified, exemption table extended) — `TestAllIssueRowWritesStampRowLock`, `TestRowLockGuardHasTeeth`
- `internal/storage/issueops/version_history_test.go` (new) — `TestAttributionStatusForActor`, `TestAttributionStatusValuesMatchR14`
- `internal/storage/schema/migration_0067_versioned_beads_test.go` (modified) — `TestMigration0067AddsVersionedBeadsSchemaThroughDoltCLI` (extended)
- `internal/storage/schema/migration_0068_add_attribution_status_test.go` (new) — `TestLatestVersionIncludesMigration0068`, `TestMigration0068AddsAttributionStatus`, `TestMigration0068AddsAttributionStatusThroughDoltCLI`
- `internal/storage/schema/schema_test.go` (modified) — `TestAllMigrationsSQLUsesDirectDDLForKnownCLIIncompatibilities` (extended, two call sites)
- `internal/storage/uow/dualwrite_history_contract_test.go` (new) — `TestDualWriteContract`, `TestDualWriteContractFlagOff`, `TestDualWriteFixtureKitIsWired`
- `internal/storage/uow/notifying_parity_test.go` (modified) — `TestNotifyingProviderBuildsRolesOnItself` (extended)

The three-legged `TestDualWriteContract`/`TestDualWriteContractFlagOff`/`TestDualWriteFixtureKitIsWired` triple in `dolt`, `embeddeddolt` and `uow` is the `DualWriteFixture` conformance contract (design FR-10) run once per storage leg, not duplication.

**failure_attribution:**

1. `internal/storage/dolt`, `internal/storage/embeddeddolt`, `internal/storage/issueops`, `internal/storage/schema`, `internal/storage/uow` — all report `ok` in this run. The previously-tracked TMPDIR-isolation condition (`be-c9mwp`, affecting `cmd/bd`, `internal/beads`, `internal/config`, `internal/formula`) did **not** reproduce here: `TMPDIR`/`GOTMPDIR` were correctly left unset for this run (the `~/.gotmp` workaround is safe for build/vet but breaks `t.TempDir()` isolation in exactly those four packages — confirmed not needed and not applied). `be-c9mwp` remains open as a tracker for anyone who hits the condition under the `~/.gotmp` override; a sighting comment noting this run's clean result is being posted separately.
2. `ci-pr-lint` (`golangci-lint`): `G602: slice index out of range (gosec)` in `backend/conformance/importer_contract.go:390,392` and `relations_contract.go:672` → `be-2l6n8` | clause 3: independently reproduced identically on a clean `origin/main` checkout (same 3 locations; one location's line-shift attributed to new sibling files changing gosec's SSA analysis order, not risk). `.golangci.yml` has no G602 exclude entry yet. Confirmed not diff-owned. Non-blocking.
3. `ci-pr-policy`: check-versions.sh false positive on a local, gitignored commit-msg shim installed by `worktree-setup.sh` → `be-xzg59` | clause 3: independently reproduced on a clean `origin/main` checkout; would not reproduce on real GH Actions CI (the shim is local-only). Confirmed not diff-owned. Non-blocking.

**attribution_evidence:** be-2l6n8 and be-xzg59 were filed this same gate round with their full clause-3 proof (clean-`origin/main` reproduction) recorded directly in each bead's description at creation time; no further comment needed beyond what's already on file. be-c9mwp's sighting comment (this run's non-reproduction) is posted as a separate, non-blocking follow-up.

**ci_lane_run (3c):** N/A — diff touches no `.github/` or `scripts/ci/` paths.

**waiver_ref:** none — no waiver needed. Zero diff-owned or pre-existing test failures occurred; the two lint/policy findings are cleanly attributed to pre-existing, non-diff-owned conditions with clause-3 proof and zero clause-4 path overlap.

**uncovered_criteria:** none

Beyond the above, `gc beads-contributor pre-pr-check` ran clean: **0 blockers, 2 warnings** — (1) commit count 17 > 15 advisory (expected: mayor-authorized 3-phase stack, not a defect), (2) title/body cites be-2l6n8/be-764ey/be-c9mwp/be-xzg59 not present in commits/diff (expected: legitimate "Review and test state" citations, not commit-message trailers). All other 8 checks passed cleanly: no postgres tokens, no `.claude/**` paths, no agent-style self-review trailers, 0 commits behind, no duplicate among our own open PRs, repro-check N/A (not a bug-fix PR), 48 files ≤ 50 OK, 3 top-level dirs, no undefined build tags.

## Merge authority

This rig is a **contributor-only** participant in `gastownhall/beads` (upstream `origin` is fetch-only by design; all push/PR traffic goes through `headfork`/`prhead`, both `quad341/beads-sec003-contrib`). No rig agent — builder, reviewer, or deployer — holds merge rights on `gastownhall/beads`, and no rig agent ever runs `gh pr merge`. Per standing policy, the deployer's job for a contributor-only rig ends at a verified open, mergeable PR; this gate stops there. **Exception, per mayor's explicit request recorded in be-764ey's notes:** an informational mail naming the new PR number is sent to mayor regardless, specifically so mayor can close the superseded draft #6354 with a pointer and update CLAIMED.md (#6149) and #6135's status — this is visibility routing, not a merge-request.

This follows established precedent: be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3, be-vc1m (PR #5792), be-7q688 (PR #6003), be-6iglh/be-0l89e (PR #6082), be-c8kgv (PR #6221), be-1wwre (PR #6247), be-3vzut (PR #6262), be-kqg23 (PR #6271), be-dy66n (PR #6304).

## Disposition

**PASS, 7/7, no waivers.** PR [gastownhall/beads#6358](https://github.com/gastownhall/beads/pull/6358) opened, verified OPEN and MERGEABLE, authored by our own account (no external-contributor human-hold triggered). Supersedes draft #6354 at mayor's explicit standing instruction; PR body states the supersession and reuses #6354's implemented/not-yet-implemented callouts verbatim, with a freshly-written review-and-test-state section reflecting this gate's own independent evidence. Two pre-existing, non-diff-owned findings encountered during gate evaluation, both attributed to their exact tracker beads (be-2l6n8, be-xzg59) with clause-3 proof recorded at creation. Mailing mayor the PR number for visibility, per mayor's explicit request; no merge-request routed, per contributor-only merge-authority carve-out.
