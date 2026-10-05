# Extending bd

This file documents contracts that callers of the storage API must honor.
It is not user-facing; it is for code that embeds bd or talks to the
storage layer directly.

## Lite SELECT shape — `IssueFilter.Lite`

`store.SearchIssues(ctx, query, filter)` accepts an `IssueFilter` value.
When `filter.Lite == true`, the storage layer issues a narrower SELECT
that omits these heavy TEXT columns:

- `description`
- `design`
- `acceptance_criteria`
- `notes`
- `waiters`
- `payload`

### Contract for callers

Code that calls `store.SearchIssues` with `IssueFilter.Lite == true`:

- **MUST NOT** read `Description`, `Design`, `AcceptanceCriteria`,
  `Notes`, `Payload`, or `Waiters` from any returned `*types.Issue`.
  These fields are zero-valued after a lite scan; they did not come from
  the row. Reading them yields no signal.
- **MAY** read every other field — identity, status, priority,
  timestamps, labels, dependencies, metadata, etc. Lite preserves them.
- **MUST** detect lite-fetched records via `issue.IsLitePartial` if
  branching behavior on hydration depth is required. The field is
  internal-only (`json:"-"`) — it never crosses the wire.

To recover the full body for a specific issue after a lite listing,
call `store.GetIssue(ctx, id)` — `GetIssue` always returns the full row.

### Default behavior

`IssueFilter.Lite` defaults to `false`. Every existing call site that
does not opt in retains today's behavior: heavy columns are fully
hydrated, and `Issue.IsLitePartial` is `false`.

### Where the contract is enforced

- Column lists: `internal/storage/issueops/scan.go`
  (`IssueSelectColumns`, `IssueSelectColumnsLite`, `HeavyDropList`).
- Scan helpers: `ScanIssueFrom` (full) and `ScanIssueLiteFrom` (lite,
  sets `IsLitePartial`).
- SELECT dispatch: `internal/storage/issueops/search.go` — `SearchIssuesInTx`
  selects `issueProjection` or `issueLiteProjection` on `filter.Lite`; both
  are `searchProjection[*types.Issue]` literals sharing the wisp-merge and
  hydration machinery in `searchTableInTxT`.
- Schema-parity guard:
  `internal/storage/issueops/scan_test.go::TestIssueSelectColumns_LitePlusHeavyEqualsFull`
  fails CI if a future column is added to `IssueSelectColumns` without
  being classified into `IssueSelectColumnsLite` or `HeavyDropList`.

### Backend coverage

`filter.Lite` is currently honored only by the issueops-backed stores
(Dolt, embedded Dolt) via the dispatch above. The proxied-server
(`internal/storage/domain/db`) path — `issueSQLRepositoryImpl.searchTable`
/ `fetchIssuesByIDs` — does not check `filter.Lite` yet: it always issues
the full `issueSelectColumns` SELECT and returns fully-hydrated issues
with `IsLitePartial == false`. This is correct-but-unoptimized (no lite
callers exist yet, so the difference is invisible today); wiring
`filter.Lite` through the domain/db stack is deferred to the CLI-wiring
follow-up (be-uwvs.2+), not part of this foundation.

## Summary projection — `store.SearchIssueSummaries`

`SearchIssueSummaries` is a third projection alongside the full and lite
shapes above. It returns `[]*types.IssueSummary` rather than
`[]*types.Issue`, selecting only `IssueSummaryColumns` — narrower than
`IssueSelectColumnsLite`, because it drops the routing/claim scalars
(`metadata`, `row_lock`, the `leases.*` overlay) a list-shaped render never
reads, not just the heavy TEXT bodies.

### Contract for callers

- **MAY** read every field `types.IssueSummary` declares. All of them come
  from the row, so there is no `IsLitePartial` equivalent and no partial
  state to detect — a field the type does not have is a compile error, not
  a silent zero value. That is the difference from Lite: Lite returns a
  full-shaped `*types.Issue` with some fields hollowed out, the summary
  projection returns a narrower type.
- **MUST** call `store.SearchIssues` instead when dependencies are needed.
  `types.IssueSummary` has no `Dependencies` field, so
  `IssueFilter.IncludeDependencies` is a silent no-op on this path.
  `SkipLabels` is honored exactly as it is on `SearchIssues`.
- **MAY** rely on wisps behaving identically. The issues+wisps merge in
  `searchInTx` runs for any filter that does not set `SkipWisps`, and
  `types.IssueSummary` carries the four wisp-plane markers (`Ephemeral`,
  `NoHistory`, `WispType`, `StorageClass`), so a merged wisp row serializes
  the same through either projection.
- **MAY** rely on identical ordering. `SortBy`/`SortDesc` render the same
  SQL `ORDER BY`, and the post-merge re-sort uses `sqlbuild.LessSummary`,
  which shares one comparator body with `sqlbuild.Less`.

### Where the contract is enforced

- Column list and scanner: `internal/storage/issueops/scan.go`
  (`IssueSummaryColumns`, `ScanIssueSummaryFrom`).
- SELECT dispatch: `summaryProjection` in
  `internal/storage/issueops/search.go` — a
  `searchProjection[*types.IssueSummary]` literal on the same
  `searchTableInTxT` core as the other two, so WHERE clauses, wisp merge,
  dedup, trim and `MaxRows` cannot drift between them.
- Column/scanner guards:
  `scan_test.go::TestIssueSummaryColumnsMatchScanner` (count agreement) and
  `TestIssueSummaryColumnsAreRealIssueColumns` (every column exists on the
  issues/wisps tables). Note `IssueSummaryColumns` is deliberately **not** a
  subsequence of `IssueSelectColumns` in order, so it has no analogue of
  `TestIssueSelectColumnsLite_IsFullMinusHeavyInOrder`.
- Wire-shape guards: `internal/types/issue_summary_test.go` pins
  `IssueSummary`'s JSON tags against `Issue`'s field by field, including the
  wisp markers.
- End-to-end parity: `internal/storage/dolt/search_parity_test.go` compares
  `SearchIssues` against `SearchIssueSummaries` on a real database, across
  filters and sort keys, and field by field for durable and wisp rows.

### Backend coverage

No in-tree caller reads `SearchIssueSummaries` yet — this is the storage
foundation, and the `bd list` wiring that spends it is tracked separately.
The proxied-server (`internal/storage/domain/db`) path does not implement
the narrow projection either, for the same reason `filter.Lite` is not
wired through it yet (above).
