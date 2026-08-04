# Release gate — be-8qyl4 (predicate-form search-counts filtering fix)

**Date:** 2026-08-04
**Deployer:** beads/deployer
**Bead (deploy):** be-8qyl4 — Review: Fix: predicate-form search aggregates the whole side table — unfiltered wisp_labels join costs a fixed ~1.6s (reviewed PASS)
**Build bead:** be-dlt6f — closed
**Review bead:** be-k2dzk — closed, review verdict PASS
**Source commit:** `4f0890fb8264c18a0589db78571105d05efc53f9` (provenance branch `builder/be-dlt6f` — provenance only, never a push target)
**Branch:** `deploy/be-8qyl4-gate` (isolated, cut fresh at the reviewed SHA)
**Base:** `origin/main` @ `0621dd6cef` ("Let httpapi.Config take the issue roles as a database source (#5337)")
**Merge-base:** `0a3ff99f34` ("Add issueops.Claimer and route the HTTP claim through it (#5334)")
**Merge-tree simulation:** `git merge-tree --write-tree origin/main 4f0890fb8` → tree `fdb73a0a7e`, exit 0, **zero conflicts**

## Verdict: PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-k2dzk closed by beads/reviewer, `verdict: pass`, close_reason "pass". style_findings: none (gofmt -l clean on all 3 changed files, go vet clean). security_findings: none (blocker/major/minor) — full 9-category OWASP-lens walk, each explicitly justified. |
| 2 | Acceptance criteria met | PASS | be-k2dzk records `uncovered_criteria: none` — all 5 "done-when" bullets addressed (3 directly verified, 2 covered via justified proxy). Independently re-verified against the diff below, not taken at face value. |
| 3 | Tests pass | PASS | Documented CI-equivalent (`./scripts/test.sh -v`) run across all 7 packages that reference `SearchCountsSQL`: **1116 PASS / 0 FAIL / 925 SKIP**, exit 0. Went further than the reviewer: found and exercised the `BEADS_TEST_EMBEDDED_DOLT=1` opt-in, obtaining genuine behavioral (not just SQL-shape) verification of all 6 changed aggregate subqueries — 17/17 PASS. See "Tests run" below. |
| 4 | No high-severity findings open | PASS | be-k2dzk: zero blocker/major/minor security findings, zero style findings. |
| 5 | Final branch is clean | PASS | `git status` on `deploy/be-8qyl4-gate` at `4f0890fb8`: "nothing to commit, working tree clean." |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main 4f0890fb8` succeeds, single merged tree `fdb73a0a7e`, no conflicts — re-verified against the current `origin/main` tip (freshly fetched), not a stale snapshot. |
| 7 | Single feature theme | PASS | `git diff --stat 0a3ff99f34 4f0890fb8` (true merge-base to reviewed SHA): 3 files, 143 insertions(+), 25 deletions(-) — `internal/storage/sqlbuild/counts.go` (implementation), `internal/storage/sqlbuild/sqlbuild_test.go` + `internal/storage/domain/db/issue_search_counts_test.go` (tests). One theme throughout: bind all 6 aggregate subqueries in the predicate-form counts query to the same filtered id set the driver already computes. |

## Acceptance check (be-k2dzk "done-when", per be-8qyl4/be-k2dzk notes)

1. **Bounds all 6 aggregate subqueries to the filtered id set.**
   Verified directly in `internal/storage/sqlbuild/counts.go`: a new `filtered_ids` CTE feeds every subquery's WHERE clause (labels, dep-count, reverse-dep-count, comment-count, parent-count, plus the driver itself), replacing per-subquery full-table scans. **PASS.**
2. **SQL-shape test (`TestSearchCountsSQLShape`).**
   Ran directly: PASS, real execution (pure string-shape assertion, no live-DB dependency). **PASS.**
3. **Parity test (predicate-form vs. by-IDs-form produce identical counts).**
   The new sub-test in `internal/storage/domain/db/issue_search_counts_test.go` needs a live Dolt container; it SKIPs cleanly in this sandbox (`BEADS_TEST_SKIP=dolt`) — confirmed independently, not taken on the builder's or reviewer's word (see "Tests run" below). A stronger substitute was obtained instead: the cross-backend `search-counts-stats` conformance audit (`backend/conformance/audit_search-counts-stats.go`), which asserts the same counts/parent/labels contract against the embedded-Dolt oracle, PASSES for real (17/17) once `BEADS_TEST_EMBEDDED_DOLT=1` is set — behavioral proof this deployer obtained that neither builder nor reviewer reached. **PASS.**
4. **`repro.sh` <1s on hq (live production timing).**
   Not executable from any sandbox (no live hq access) — same limitation builder and reviewer both disclosed. Mechanism verified instead: every aggregate subquery now scans `filtered_ids` (the driver's already-narrowed set) rather than the full side table, the exact mechanism the original 2.644s→0.014s estimate was computed from. Live timing is a deploy-time check. **Proxy-verified, non-blocking** (matches reviewer's disposition).
5. **`gc status` renders per-agent health correctly (downstream, cross-repo symptom).**
   Out of reach of beads-repo code review by construction — cross-repo (gascity, not beads), requires this fix actually deployed to hq. This is what routing the merge-request to mayor exists to close out. **Proxy-verified, non-blocking** (matches reviewer's disposition).

## Tests run on release branch

| Command | Result | Notes |
|---|---|---|
| `./scripts/test.sh -v ./internal/storage/sqlbuild/... ./internal/storage/domain/db/... ./internal/storage/issueops/... ./internal/storage/dolt/... ./internal/storage/embeddeddolt/... ./internal/storage/uow/... ./backend/...` | **1116 PASS / 0 FAIL / 925 SKIP**, exit 0 | Every consumer of `SearchCountsSQL` (grepped repo-wide) plus the conformance-audit-hosting packages. Per-package: sqlbuild 37/0/0, domain/db 2/0/1, issueops 383/0/0, dolt 531/0/805, embeddeddolt 38/0/94, uow 123/0/25, backend 2/0/0. |
| `BEADS_TEST_EMBEDDED_DOLT=1 ./scripts/test.sh -v -run 'TestConformance/Audit/search-counts-stats' ./internal/storage/embeddeddolt/...` | **17 PASS / 0 FAIL**, exit 0 | Real behavioral verification of `SearchIssuesWithCounts`, `WispMergeSearchCount`, and 15 other cross-backend audit cases — against the embedded-Dolt oracle, the exact aggregate paths this fix changed. |
| `BEADS_TEST_EMBEDDED_DOLT=1 TEST_COVER=1 ./scripts/test.sh -v ./internal/storage/embeddeddolt/...` (full package, opt-in enabled) | **441 PASS / 0 FAIL / 0 SKIP**, exit 0, 55.1% coverage | Beyond-scope sanity check: confirms the opt-in doesn't surface any other regression in the package this fix's strongest evidence came from. Not required for this gate; recorded for the next deployer who touches this package. |
| SKIP audit (925 total, all 7 packages) | All environmental | Every `--- SKIP:` line traced to one of two documented sandbox gates: `BEADS_TEST_SKIP=dolt` (no live Dolt server) or the `BEADS_TEST_EMBEDDED_DOLT=1` opt-in (unset by default, exercised separately above). 708 distinct top-level test names affected, spread across the whole storage-backend test surface — not concentrated on this diff's code paths. Matches the pre-existing sandbox limitation already on file in deployer memory; not something this diff introduced or made worse. |

## Findings from review (no action required)

be-k2dzk (reviewer): zero blocker/major/minor security findings (full 9-category OWASP-lens walk, each explicitly justified n/a or verified-safe — notably confirmed `whereSQL` is still interpolated exactly once in the new CTE, preserving the existing `//nolint:gosec // G201` reviewed pattern). Zero style findings (gofmt -l clean, go vet clean).

## Push target

`origin` (`gastownhall/beads`) push is disabled (placeholder URL) — upstream is fetch-only. `prhead` (`quad341/beads-sec003-contrib`) is the currently-active push convention — used by this session's immediately-prior deploy (be-ulyj3 / PR #5329, still open) and the deploys before it — confirmed via `git push --dry-run`.

## Verdict

**PASS** — push `deploy/be-8qyl4-gate` to `prhead`, open PR against `gastownhall/beads:main`. Merge decision routed to mayor (deployer does not merge).
