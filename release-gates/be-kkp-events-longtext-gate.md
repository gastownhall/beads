# Release gate: be-kkp — widen events/wisp_events value columns to LONGTEXT

**Verdict: PASS.**

Branch: `release/be-kkp-events-longtext` (stacks on `release/be-nx7-d4v2-indexes`, PR #3662)
Base on origin: `origin/main` @ `772a65688`
Stack base: `fork/release/be-nx7-d4v2-indexes` @ `de25f8ef3`
HEAD: `c0cd96b1`

## Stacking

be-kkp's commit adds migration `0034_widen_event_value_columns`. Adding any new migration on top of `origin/main` reproduces the pre-existing brittle assertion in `internal/storage/schema/schema_test.go:48-49` (`TestMigration0032ToleratesMissingAppliedAtColumn` hardcoded `applied == 1`). The fix for that brittleness is `684fb88` on PR #3662 (be-nx7), which derives `wantApplied = LatestVersion() - 31`. This branch therefore stacks: be-nx7 first, then be-kkp on top — matching the precedent set by `release-gates/be-1n9-pq5-purge-gate.md`. Until PR #3662 lands, the GitHub PR view will show 5 commits ahead of `origin/main`; once it lands, the view collapses to the single be-kkp commit.

Empirical confirmation of the stacking dependency: cherry-picking `1a159fb6` directly onto `origin/main` produces `applied migrations = 2, want 1` — failed in this rig prior to writing this gate. Stacking on `de25f8ef3` produces `ok` for the same test.

## Commit (be-kkp portion only)

| # | SHA on `release/be-kkp-events-longtext` | Source on `quad341/beads:rebase/be-nx7-be-1n9-stack` | Subject |
|---|----------------------------------------|------------------------------------------------------|---------|
| 1 | `c0cd96b1` | `1a159fb6` | fix(schema): be-kkp widen events/wisp_events value columns to LONGTEXT |

Cherry-pick is clean (3 new files: migration up/down + test; 1 modified file: `ignored_tables.go`).

## Criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | PASS | Reviewer-1 (`beads/reviewer-1`, 2026-05-03) PASS verdict on be-bof, no blockers, no requested changes. Gemini second-pass disabled per current policy. |
| 2 | Acceptance criteria met | PASS | All four columns widened: `events.{old_value,new_value}` and `wisp_events.{old_value,new_value}` migrate from TEXT (max 65535 B) to LONGTEXT (4 GiB). Down migration narrows back symmetrically. `ignored_tables.go` registers `{version: 34, filter: "wisp_events"}` so per-test branches pick up the wisp_events ALTERs via `CreateIgnoredTables`. Reporter's "wisp_events.body" misquote was scope-corrected (no `body` column exists in the schema; see `RecordFullEventInTable` at `internal/storage/issueops/helpers.go:231`). |
| 3 | Tests pass | PASS | Targeted: `BEADS_TEST_EXTERNAL_DOLT_PORT=33999 go test -run TestMigration0034 -count=1 -v ./internal/storage/dolt/` → all 3 tests PASS in 4s (column types, 200 KiB round-trip, down/up reversibility). Schema package: `go test -count=1 ./internal/storage/schema/` → PASS. Regression check: dolt-package short tests on the stacked branch produce 14 failures (the `TestApplyConfigDefaults_*`, `TestCreateGuard_*`, `TestUpdateIssueIDUpdatesWispTables`, `TestPullWithAutoResolve_*` rig-env-leakage failures); all 14 are pre-existing on the `de25f8ef3` stack base (which itself shows 16 failures — be-kkp eliminates none of consequence and adds none). No new failures vs the stack base. |
| 4 | No high-severity review findings open | PASS | Zero blocking findings. All four reviewer table entries are `info` severity. |
| 5 | Final branch is clean | PASS | `git status` clean (untracked `.gc/`, `.gitkeep` are rig artifacts outside the tree). |
| 6 | Branch diverges cleanly from main | PASS | Cherry-pick of `1a159fb6` onto `de25f8ef3` is clean (no conflicts). Once PR #3662 lands, GitHub will fast-forward this PR's diff to a single be-kkp commit against `origin/main` with no conflicts. |

## Test environment

- Scratch host Dolt sql-server: `dolt 1.86.6`, port 33999, data-dir `/var/tmp/be-kkp-scratch/dolt-data`.
- `BEADS_TEST_EXTERNAL_DOLT_PORT=33999`, `BEADS_TEST_EXTERNAL_DOLT_DATA_DIR=/var/tmp/be-kkp-scratch/dolt-data`.
- `go vet` and `go build` on the stacked branch: clean.

## Push target

`PUSH_REMOTE=fork` (origin = `gastownhall/beads` is upstream-not-pushable for this rig user; `git push --dry-run origin HEAD` returns 403). Cross-repo PR head: `quad341:release/be-kkp-events-longtext`.

## Open follow-ups (out of scope for this gate)

The four pre-existing test failures cited in be-kkp's notes (`TestApplyConfigDefaults_ProductionFallback`, `TestUpdateIssueIDUpdatesWispTables`, `TestGetAllEventsSince_UnionBothTables`, `TestPullWithAutoResolve_BranchTrackingFallback`) are not currently tracked in any open bead. Reviewer-1 flagged these as worth filing follow-ups for visibility — outside this gate's remit.
