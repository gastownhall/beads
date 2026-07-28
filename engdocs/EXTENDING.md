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
