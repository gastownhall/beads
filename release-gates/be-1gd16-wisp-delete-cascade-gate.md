# Release gate — wisp delete cascade to auxiliary tables (be-1gd16)

- **Original bead (CLOSED):** be-zdqyl — Fix: wisp delete never cascades to
  `wisp_labels`/`wisp_events` — 3.03M orphan rows on the city store
- **Round-2 build bead (CLOSED):** be-wnuyt — added repeated-cycle test
  coverage requested by round-1 review
- **Review bead (CLOSED):** be-u4s3y — Verdict **PASS** (round 2, responding
  to round-1 request-changes) on commit `fe76a21ea5c375c6a5790aa501f214a4e5d33b63`
- **Deploy bead:** be-1gd16
- **Commit shipped:** `fe76a21ea5c375c6a5790aa501f214a4e5d33b63` (deploy
  source SHA recorded in be-1gd16), 3 commits over `origin/main` @
  `d801ec43dc9b46ffc960eb2793933bbe725ddde4` (2 files, +298), merge-base
  is `origin/main`'s exact current tip
- **Source branch:** `builder/be-zdqyl` (provenance only, not a push target)
- **Deploy branch:** `deploy/be-1gd16-gate`, cut from the commit above
- **Evaluated:** 2026-08-04 by beads/deployer

## Scope

Fixes a data-integrity bug in `internal/storage/dolt/wisps.go`: both wisp
delete paths (`deleteWisp`, `deleteWispBatchTx`) deleted the `wisps` row and
its dependency edges but never the wisp's rows in the four auxiliary tables
(`wisp_labels`, `wisp_events`, `wisp_comments`, `wisp_child_counters`),
leaking ~3.03M orphan rows on the live `hq` city store (91.7% of
`wisp_labels`, 89.2% of `wisp_events`). Some stores enforce this via FK `ON
DELETE CASCADE` and some do not, so the delete paths cannot rely on the
database to do it for them.

Fix: a new shared helper `deleteWispAuxRowsInTx`, called from both delete
paths inside their existing transaction, deletes matching rows from all four
tables (three keyed on `issue_id`, `wisp_child_counters` keyed on
`parent_id`). Table/column identifiers are a fixed literal slice, not user
input; row-scoping ids go through the existing parameterized `?`
placeholder helper — same `//nolint:gosec // G201` pattern already used
elsewhere in this file.

**Explicitly out of scope (documented in be-zdqyl, not a gate concern):**
backfilling the existing 3.03M orphan rows on the live `hq` store — that is
a live-prod data operation belonging to mayor/operator authority, to be
filed separately. This bead is the code fix that stops the leak from
growing further.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-u4s3y recorded `verdict: pass` (round 2, 2026-08-04T23:25Z), responding to round-1's (be-wnuyt's predecessor review) request-changes. Single-pass (Claude); gemini second-pass currently disabled. |
| 2 | Acceptance criteria met | **PASS** | Independently re-checked all 3 be-zdqyl done-when items against the diff myself (not just the reviewer's restatement) — see "Acceptance" below. |
| 3 | Tests pass | **PASS** | See "Tests run on release branch" below — re-run independently by the deployer, not just trusted from the review. |
| 4 | No high-severity review findings open | **PASS** | Zero HIGH findings. be-u4s3y's style_findings and security_findings both conclude "no blockers or majors, PASS." Independently spot-checked `gofmt -l`, `go vet`, `go build` myself — all clean. |
| 5 | Final branch is clean | **PASS** | `git status` on `deploy/be-1gd16-gate` shows "nothing to commit, working tree clean." |
| 6 | Branch diverges cleanly from main | **PASS** | `git merge-base fe76a21ea5c375c6a5790aa501f214a4e5d33b63 origin/main` = `d801ec43dc9b46ffc960eb2793933bbe725ddde4`, which is `origin/main`'s exact current HEAD — the reviewed commit is a direct 3-commit fast-forward descendant of current `origin/main`. Zero conflict risk; no self-rebase needed. |
| 7 | Single feature theme | **PASS** | Diff touches exactly 2 files, both in `internal/storage/dolt/` (`wisps.go` + its integration test) — one subsystem, one bug, purely additive (+298/-0). |

## Acceptance (per be-zdqyl done-when, independently re-verified against the diff)

| Criterion | Status | Evidence |
|---|---|---|
| Both delete paths remove rows from all four wisp side-tables in-transaction | ✓ | `deleteWispAuxRowsInTx` called from both `deleteWisp` (wisps.go:349) and `deleteWispBatchTx` (wisps.go:424), inside the same `*sql.Tx` as the existing wisp-row delete, before `RecomputeIsBlockedInTx`. Table set: `wisp_labels`, `wisp_events`, `wisp_comments` (on `issue_id`), `wisp_child_counters` (on `parent_id`) — matches `internal/storage/schema/cli_migrations.go:300-315`. |
| A test creates a wisp with labels + events + comments, deletes it, and asserts zero residual rows in every side-table (single and batch paths) | ✓ | `TestWispDeleteCascade_CleansUpAuxiliaryTables`, subtests `single_delete` and `batch_delete`, both PASS. |
| Orphan counts on a fresh store stay at 0 after a reap cycle | ✓ | `TestWispDeleteCascade_RepeatedCyclesLeaveNoOrphans` — 5 create-seed-delete cycles asserting store-wide total aux-row counts are 0 after every cycle. PASS. Added in round 2 in direct response to round-1 review's request-changes on this exact criterion. |
| One shared helper used by both paths so the table set cannot drift | ✓ | `deleteWispAuxRowsInTx` is the single call site for both `deleteWisp` and `deleteWispBatchTx`. |

## Tests run on release branch

Ran independently by the deployer on `deploy/be-1gd16-gate` (not copied from
the review) — native `dolt` v2.2.1 present on `PATH`, so
`BEADS_TEST_SKIP=dolt` is not force-set and the diff's own standalone
Dolt-server-backed tests are not skipped.

| Test | Result | Notes |
|------|--------|-------|
| `go build ./...` | success | clean build. |
| `gofmt -l internal/storage/dolt/wisps.go internal/storage/dolt/wisp_delete_cascade_integration_test.go` | clean | no output, no formatting diffs. |
| `go vet -tags=integration,gms_pure_go ./internal/storage/dolt/...` | clean | no findings. |
| `go test -tags=integration,gms_pure_go -race -timeout=15m -v ./internal/storage/dolt/...` | **575 PASS / 0 FAIL / 951 SKIP** (ok, 69.128s) | Exact match to be-u4s3y's independently-run counts (575/0/951). |
| `TestWispDeleteCascade_CleansUpAuxiliaryTables` (diff-owned) | **PASS** (12.75s) | single_delete + batch_delete subtests both ran for real. |
| `TestWispDeleteCascade_RepeatedCyclesLeaveNoOrphans` (diff-owned) | **PASS** (11.42s) | 5 cycles, ran for real. |

`diff_tests_executed`: `TestWispDeleteCascade_CleansUpAuxiliaryTables` PASS,
`TestWispDeleteCascade_RepeatedCyclesLeaveNoOrphans` PASS. Both use their own
standalone `dolt` sql-server (`doltserver.Start` on the native binary), so
they are not subject to the container-runtime gate below.

`skip_justification`: all 951 skips trace to
`internal/storage/dolt/testmain_test.go:60`
(`EnsureDoltContainerForTestMain` — no container runtime in this sandbox);
every test keyed to that shared package-level fixture self-skips. This is a
pre-existing sandbox gap, not introduced by this diff, and does not touch
either diff-owned test (which start their own server independent of that
fixture). `waiver_ref`: none needed — no diff-owned test was skipped.

## Findings from review (no action required)

From be-u4s3y (round 2): no blockers or majors in either style or security
review.

- Injection (OWASP A03): `deleteWispAuxRowsInTx` builds SQL via
  `fmt.Sprintf` for table/column names, but both come only from the fixed
  `wispAuxCascadeTables` literal — not attacker- or caller-controlled. Row
  ids go through parameterized `?` placeholders. No injection vector.
  Independently confirmed by reading the diff.
- Data integrity / TOCTOU: all four aux-table deletes plus the wisps-row
  delete plus `RecomputeIsBlockedInTx` execute inside the same transaction —
  atomic, no window where a wisp is gone but aux rows survive.

## Push target

`origin` (`gastownhall/beads`) has push deliberately disabled
(`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`); `fork`
(`quad341/beads`) accepts. PR opens cross-repo against
`gastownhall/beads:main` with head `quad341:deploy/be-1gd16-gate`.

`gastownhall/beads` is upstream-only for this deployer — we are
contributors, not maintainers. Per standing policy, job ends at PR-open; no
merge-request is routed, merge belongs to the upstream maintainers.

## Verdict

**PASS (7/7)** — push the gate-file commit to `fork`, open PR.
