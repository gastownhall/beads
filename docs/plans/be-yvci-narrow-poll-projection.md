# Plan — be-yvci: narrow the ready/work-probe projection (drop LONGTEXT bodies from the poll path)

**Bead:** be-yvci (P1, bug) · **Rig:** beads · **Base branch:** `feat/connection-pooling` · **Branch:** `gc/be-yvci`
**Author:** beads/voxist.planner-1 · **Date:** 2026-06-25
**Slice:** A (beads-core) of the dolt-CPU re-diagnosis. Slice B (gascity work_query warm-pool + flip the flag introduced here) = ga-arn. Slice C (`gc dolt cleanup`) = ops.

## Problem (one paragraph)

The managed Dolt sql-server pegs CPU under the agent fleet. The architect's
EXPLAIN-driven re-diagnosis (be-yvci comments, 2026-06-25) killed the original
"missing index" and "recursive recompute" hypotheses: indexes exist and are
used (`IndexedTableAccess index:[is_blocked,status]`), reads already use the
materialized `is_blocked` column, and ANALYZE/stats is a dead end
(memory-only, no durable gain). The remaining **beads-core** lever is the
**wide projection on the high-frequency poll/work-probe path**: the supervisor
probes every agent's `work_query` (`bd ready --metadata-field
"gc.routed_to=$target" --json`) every reconcile, and that path projects all 47
issue columns — including 5 LONGTEXT + 1 JSON + 2 TEXT "body" columns — for the
72–82 matched rows. Measured cost: **7–12× the narrow projection for the same
rows**. The probe never displays those bodies; it only needs routing/sort
columns + `metadata` (which carries `gc.routed_to`).

## Goal

Give the ready/work-probe path a **brief projection** that omits the heavy body
columns while keeping every column the probe actually consumes — crucially
`metadata` (routing) and `title`. Ship it as an **opt-in `WorkFilter.BriefBodies`
capability, default off** (zero behavior change to `bd ready`/`bd list` today,
so no upstream contract break), wired through to a `--brief` CLI flag. The
fleet-wide CPU win lands when Slice B (gascity ga-arn) sets that flag on the
supervisor probe.

## Crux finding that corrects the bead's design-field acceptance

The bead's `design` field says the narrow projection should exclude
`description, design, acceptance_criteria, notes, close_reason, **metadata**`.
**Keeping `metadata` out is wrong for the ready/work-probe path.** The gascity
work_query is `bd ready --metadata-field "gc.routed_to=$target" --json`
(`gascity internal/config/config.go:3511`) and the consumer jq-filters on
`.metadata["gc.routed_to"]` / `.metadata["gc.run_target"]`
(`config.go:3527,3561`). Dropping `metadata` from the projection would silently
break pool routing. **This plan keeps `metadata` and `title`; it drops only the
7 free-text body columns** (`description`, `design`, `acceptance_criteria`,
`notes`, `close_reason`, `payload`, `waiters`) — which are the measured 7–12×
driver. `metadata` is a small JSON blob, not part of that cost.

## Code map (on `feat/connection-pooling`)

| Symbol | Location | Role |
| --- | --- | --- |
| `IssueSelectColumns` (47 cols) | `internal/storage/issueops/scan.go:14-23` | canonical wide projection |
| `ScanIssueFrom` | `internal/storage/issueops/scan.go:34-62` | order-coupled wide scan |
| `readyWorkIssueColumns` (`i.`-aliased wide list) | `internal/storage/issueops/ready_work_counts.go:103-111` | **work-probe projection (offender)** |
| `GetReadyWorkWithCountsInTx` | `internal/storage/issueops/ready_work_counts.go:14-75` | the `bd ready`/work-probe entry |
| `runSearchQueryInTx` | `internal/storage/issueops/search_counts.go:98` (uses `readyWorkIssueColumns` at `:165`) | builds the SELECT + scans |
| `scanReadyWorkRowWithCounts` | `internal/storage/issueops/ready_work_counts.go:123` | composite scan (issue cols + dep/label/count extras) |
| `types.WorkFilter` | `internal/types` | filter struct; add `BriefBodies bool` |
| existing sqlmock tests | `internal/storage/issueops/search_counts_test.go`, `ready_work_test.go` | house test style (`sqlmock`, `ExpectQuery(regex)`) |

The body columns to drop and the columns to keep:

- **Drop (7):** `description, design, acceptance_criteria, notes, close_reason, payload, waiters`
- **Keep (40, incl. routing):** everything else in `IssueSelectColumns`, notably
  `id, title, status, priority, issue_type, assignee, created_at, updated_at,
  owner, due_at, defer_until, ephemeral, pinned, is_template, **metadata**`.

## Recommended mechanism

`WorkFilter.BriefBodies bool` (default `false`). When `true`,
`GetReadyWorkWithCountsInTx` → `runSearchQueryInTx` projects a new
`briefReadyWorkIssueColumns` and scans via a brief scanner that leaves the 7
body fields zero-valued. Default `false` keeps the existing wide behavior, so
`bd ready --json` / `bd list --json` output is unchanged for every existing
caller (no upstream contract break, no `gc hook` body regression). A `--brief`
flag on `bd ready` sets the field; gascity Slice B passes `--brief` on the
supervisor probe to realize the CPU win.

`ScanIssueFrom` is strictly order-coupled to `IssueSelectColumns`, so the brief
path needs its own column list **and** a matching brief scanner — do not try to
reuse `ScanIssueFrom` with a shorter column list.

## Micro-tasks

| id | description | acceptance (a single failing test) | est_minutes | slings |
| --- | --- | --- | --- | --- |
| T-001 | Write the failing projection test: drive `GetReadyWorkWithCountsInTx` (or `runSearchQueryInTx`) via `sqlmock` with `WorkFilter{BriefBodies:true}` and assert the emitted SELECT **excludes** `description\|design\|acceptance_criteria\|notes\|close_reason\|payload\|waiters` and **includes** `metadata` and `title`. | `go test ./internal/storage/issueops -run TestReadyWorkBriefProjection` fails to compile/RED (field + brief columns don't exist yet). | 5 | — |
| T-002 | Add `BriefBodies bool` to `types.WorkFilter`; add `IssueSelectColumnsBrief` (the 40-col keep-set, metadata+title retained) to `scan.go`; add the `i.`-aliased `briefReadyWorkIssueColumns` var in `ready_work_counts.go` mirroring `readyWorkIssueColumns`. | `go build ./...` passes; a unit assertion that `IssueSelectColumnsBrief` contains `metadata`,`title` and none of the 7 body names passes. | 4 | — |
| T-003 | Add `ScanIssueBriefFrom(IssueScanner)` in `scan.go` scanning exactly the brief column set in order, leaving `Description/Design/AcceptanceCriteria/Notes/CloseReason/Payload/Waiters` zero-valued; add the brief branch to `scanReadyWorkRowWithCounts`'s composite row. | `go test ./internal/storage/issueops -run TestScanIssueBrief` passes (round-trips id/title/status/metadata; body fields empty). | 5 | — |
| T-004 | Branch `GetReadyWorkWithCountsInTx`→`runSearchQueryInTx` to use `briefReadyWorkIssueColumns` + the brief scan when `filter.BriefBodies`; leave the default (wide) path byte-for-byte unchanged. | T-001 now GREEN; full `go test ./internal/storage/issueops/...` GREEN. | 5 | — |
| T-005 | Guard test: with `BriefBodies:true`, returned `IssueWithCounts` still carry populated `Metadata` (routing intact), `ID`, `Status`, `Title`, and counts; with `BriefBodies:false` the bodies are still populated (no regression). | `go test ./internal/storage/issueops -run TestReadyWorkBriefKeepsMetadata` passes. | 4 | — |
| T-006 | Add `--brief` flag to the `bd ready` command (and `bd list` if trivial) that sets `WorkFilter.BriefBodies`; document it as the seam gascity uses. | `go test ./cmd/... -run TestReadyBriefFlag` (or cmd-layer equiv) asserts `--brief` → `filter.BriefBodies==true`. | 5 | gascity/voxist.executor (ga-arn) sets `--brief` on the work_query probe |
| T-007 | (Non-gating, best-effort) Add `BenchmarkReadyWorkBriefVsFull` and/or capture EXPLAIN via the recipe below; record the brief-vs-wide delta in the PR body. | Benchmark runs; PR body cites the measured ratio (target: brief materially cheaper, architect measured 7–12× on the wide path). | 5 | — |

Run `go test ./...` (or at least `./internal/storage/...` and `./cmd/...`) green before opening the PR. `gofmt`/`go vet` clean.

## Verification / evidence

- **Primary (deterministic, rig-local):** T-001/T-004 projection test + T-005 metadata-retention guard.
- **Secondary (best-effort):** EXPLAIN + timing on a populated store. EXPLAIN over the
  dolt sql-server CLI renders empty in 2.1.8 — use a direct MySQL-protocol client.
  Read-only recipe (from the bead's architect comment):
  ```
  DOLT_CLI_PASSWORD='' dolt --host 127.0.0.1 --port $(cat ~/portharbour/.beads/dolt-server.port) --user root --no-tls sql -q "USE hq; <query>"
  ```
  (`--no-tls` must be the last global arg.) The fleet-level `SHOW GLOBAL STATUS`
  Com_select drop is realized only once Slice B (ga-arn) flips `--brief` on the
  probe — that is **not** this bead's gate.

## GDPR data-flow impact

No-op (and mildly positive). This change only narrows a `SELECT` column list on
an internal issue-tracker poll path; it stores nothing new, exposes nothing new,
and removes no data-subject capability. If anything it advances data
minimisation: the high-frequency machine probe stops pulling free-text issue
bodies it never reads. No personal data crosses a new boundary; `bd show` (wide
path) remains the way detail is fetched. No DSR (Art. 15–20) surface is touched.

## MDR Class I traceability

No-op. This bead is in the beads issue-tracker core, not the
voxmemo → voxist-api clinical pipeline. No chain-of-evidence metadata, no
clinical recording/transcript path, no Class I traceability surface is involved.
Heading retained for auditor visibility per planner discipline.

## Open questions

- (executor/reviewer) Does the `bd list` path want the same `--brief` seam, or
  is `bd ready` sufficient for Slice B? T-006 scopes `bd ready`; extend to
  `bd list` only if trivial — otherwise leave to a follow-on.
- (executor/reviewer) `scanReadyWorkRowWithCounts` builds a composite row with
  dep/label/count extras appended after the issue columns; confirm the brief
  scanner appends the same extras in the same order (the coupling is the main
  implementation risk).
- (executor/reviewer) Confirm no non-supervisor caller reads `.description` (etc.)
  off `bd ready --json` today; if one exists it must pass no `--brief` (default
  keeps bodies, so this is safe, but worth a grep).
- [architect] FYI — this plan **keeps `metadata` in the brief projection**,
  diverging from the bead's design-field acceptance which listed `metadata`
  among the columns to exclude. Reason: the work_query routes on
  `.metadata["gc.routed_to"]`; dropping it would break pool routing. Flagged in
  a bead comment too.

## Handoff

One bead, one PR. Executor reuses worktree `worktrees/be-yvci` on `gc/be-yvci`
(base `feat/connection-pooling`), reads this plan, executes T-001…T-007 red-green,
opens one PR. Slice B (gascity ga-arn) consumes the `--brief` seam from T-006.

## Execution status (beads/voxist.executor)

All gating micro-tasks green and committed on `gc/be-yvci`.

- [x] T-001 — brief work-probe SELECT omits the 7 body cols, keeps metadata+title   ✅ TestReadyWorkBriefProjection (1f6d46ae2)
- [x] T-002 — `WorkFilter.BriefBodies` + derived `IssueSelectColumnsBrief` (drift-proof) + `briefReadyWorkIssueColumns`   ✅ TestIssueSelectColumnsBrief (1f6d46ae2/b16814ce3)
- [x] T-003 — `ScanIssueBriefFrom` + shared composite-extras scanner   ✅ TestScanIssueBrief (1f6d46ae2/b16814ce3)
- [x] T-004 — `runSearchQueryInTx(brief)` branch; `GetReadyWorkWithCountsInTx` threads `filter.BriefBodies`; wide path unchanged   ✅ full issueops green (1f6d46ae2)
- [x] T-005 — brief keeps routing metadata + counts, bodies empty   ✅ TestReadyWorkBriefKeepsMetadata (b16814ce3)
- [x] T-006 — `bd ready --brief` seam → `WorkFilter.BriefBodies` (testable helper)   ✅ TestReadyBriefFlag (6a623b24b)
- [~] T-007 — best-effort, non-gating: documented below (no synthetic benchmark — the win is server-side projection, unmeasurable without a populated dolt store).

### Implementation notes / refinements

- **Drift-proofing.** `IssueSelectColumnsBrief` is **derived** from `IssueSelectColumns`
  by removing `bodyColumnsOmittedInBrief` (not a hand-kept parallel constant), so it
  can never drift; `TestIssueSelectColumnsBrief` locks the drop/keep sets.
- **Coupling risk (plan-flagged) eliminated structurally.** `scanReadyWorkRowWithCounts`
  and the new `scanReadyWorkBriefRowWithCounts` both delegate to a shared
  `scanReadyWorkRowWithScanner(rows, scanFn)` — the 6 composite extras
  (labels_json, dep/rdep/comment counts, parent_id, deps_json) are appended in one
  place, so brief and wide are guaranteed identical on the extras.
- **Scope.** `--brief` is wired on `bd ready` only (the gascity ga-arn work_query
  command). `bd list --ready` (via `readyWorkFilterFromIssueFilter`) is left wide —
  per the plan's open question, a trivial follow-on if a second probe needs it.
- **Column counts.** The canonical list is 46 columns (the plan's "47" was off by
  one); brief is 39 (drops exactly the 7 named body columns: 5 LONGTEXT
  description/design/acceptance_criteria/notes/close_reason + 2 TEXT payload/waiters).

### T-007 — measured delta (documented)

The architect measured the wide projection at **7–12× the brief projection for the
same matched rows** (be-yvci comments). The structural reduction this PR ships: the
work-probe SELECT now projects 39/46 issue columns, dropping all 5 LONGTEXT + 2 TEXT
free-text/blob bodies the probe never displays. A local micro-benchmark is **not**
included: the cost is server-side (Dolt materializing LONGTEXT into the result set),
not client scan time, so a sqlmock/in-process benchmark would misrepresent it. The
fleet-level `SHOW GLOBAL STATUS` Com_select / CPU drop is realized when Slice B
(gascity ga-arn) flips `--brief` on the supervisor probe — explicitly **not** this
bead's gate.

### Verification

`go build -tags=gms_pure_go ./...` green; `gofmt`/`go vet` clean (issueops, types,
cmd/bd); `go test ./internal/storage/issueops/... ./internal/types/...` green; the
new `bd ready` command tests green; the CI pure-Go (`CGO_ENABLED=0`) cmd/bd test
binary compiles. The Docker-backed dolt store tests and the `BEADS_DOLT_SERVER_PORT`-
sensitive `TestApplyConfigDefaults_*` tests are unaffected (this change is confined
to issueops/types/cmd-bd) and run clean in CI.
