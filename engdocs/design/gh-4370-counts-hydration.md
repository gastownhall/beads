# GH#4370: select-then-hydrate on the counts/ready scan paths — design record

Issue [gastownhall/beads#4370](https://github.com/gastownhall/beads/issues/4370):
`bd list`-class scans seizes at ~100k issues (2-minute client timeouts) under
concurrent write churn, while point reads and `bd stats` stayed fast. This
note records what shipped, why it is safe, and how to reason about the paths
it touched when they are next changed.

## What changed

The counts projections (`bd list --json`, ready work with counts, piped
listings) used to run one "counts mega-query" per table family in **predicate
form**: a driver over the whole matching set, LEFT JOINed to aggregates that
each computed over a whole side table, with ORDER BY and LIMIT applied after
all the joins. Three costs dominated at ~100k rows:

1. Every aggregate (labels, dep/comment counts, parent, dependency JSON)
   materialized over its whole side table, once per family query.
2. The reverse-blocker subquery joins on `COALESCE(depends_on_issue_id,
   depends_on_wisp_id, depends_on_external)` — an expression no supported
   planner (Dolt server or the pure-Go analyzer) can seek — so its
   materialization was re-scanned once per driver row.
3. An unbounded (`--limit 0`, piped, or `--all`) request ran that whole shape
   over the complete corpus.

Every counts seam now answers in two steps:

- **Select the page of IDs** with the narrow id projection the plain search
  seam already runs (label-driven JOINs, residual filters, the page bound, the
  Go-side `--sort id` sort-before-bound).
- **Hydrate only those IDs** through the by-IDs form of the same mega-query,
  which constrains every aggregate's input to the page and chunks it at
  `sqlbuild.QueryBatchSize` (200) so the by-IDs placeholder binding stays
  within per-statement limits (at most 2,400 bindings per batch).

The by-IDs form's reverse-blocker was also restated as three disjoint
`UNION ALL` legs per dependency table (issue target; wisp target with the
issue target NULL; external target with both NULL), each constraining a real,
indexed column — `idx_dep_issue_target`/`idx_dep_type_issue` and the 0047 wisp
twins — instead of the unseekable COALESCE key. The legs derive their guards
from the COALESCE precedence itself, not from `ck_dep_one_target`, so their
result set is identical even if that CHECK ever relaxes. The predicate form
remains in `sqlbuild.SearchCountsSQL` as the parity oracle that the tests
compare against.

Work-paged seams that now share one shape:

- `issueops.SearchIssuesWithCountsInTx` (direct store, embedded store),
- `issueops.GetReadyWorkWithCountsInTx`, both bounded and unbounded pages,
- the proxied non-union branches of `domain/db_searchIssuesAndWispsWithCounts`
  (SkipWisps, and the empty/missing wisps probe routes).

The proxied union and ready paths already selected IDs first; they are
unchanged.

## What deliberately did NOT change

No CLI flag, JSON envelope, or schema migration. The limit and paging
contracts are exactly as before, and all remain the right knobs for bounding
what an agent reads:

- `bd list` piped output is **unlimited by design** (GH#4094); an explicit
  `--limit 0` and `--all` mean unlimited; a configured `list.limit` applies
  when piped; a terminal gets 50, an agent at a terminal 20. This work changed
  what the query UNDER those knobs costs, never what the knobs hand back — the
  aim was to make the unbounded paths not blow up, not to take them away.
- `--max-rows` / `BEADS_MAX_ROWS` remain the opt-in defensive cap: a refusal
  (`*ErrTooManyRows`), not a truncation, counted on the rows the query
  TOUCHED (limit + offset), with the refusal carrying the caller's
  attribution.
- Keyset paging (`--after-created`/`--after-id` and the served redirect
  cursor) is untouched; it remains the portable paging page.

Downstream retention policy (bounding closed-churn accumulation itself), new
indexes, and cursor/envelope additions for scripted consumers remain open and
out of scope, exactly as the issue tracker records them.

## Behavioral notes for future changes

- **Statement budgets.** An unbounded family read that matches rows now costs
  ID-select + hydration (two statements) where the predicate mega-query cost
  one; a family whose page is empty stops after the ID select. `bd ready`'s
  budget pins live in `internal/storage/embeddeddolt/ready_total_test.go`.
- **Broken-plane detection.** The hydration reads labels/comments/dependencies
  only when the page is non-empty, so a table that a legs' query no longer
  names is first seen when rows cross it. The missing-table guards
  (issueops/counts_missing_table_test.go and the embedded twin) encode the
  current reach per entry point.
- **Nothing about counts changed.** Per-row counts are functions of the whole
  dependency graph restricted to that row, so constraining the aggregates'
  input to the page cannot change what a surviving row counts. The parity
  tests in `internal/storage/dolt/search_counts_parity_test.go`,
  `search_counts_select_oracle_test.go` and
  `search_counts_byids_plan_test.go` are the pins to extend when touching
  these shapes, and the SQL EXPLAIN assertion to keep indexed is
  `TestSearchCountsByIDsPlanNeverScansADependencyTable`.

## Measurement guidance (from the issue's open questions)

This change removes the class of cost that dominated the reported incident;
whether it fully explains any given timeout still wants a before/after phase
measurement under concurrent writes. The fixture to build is the disposable
~100k-issue corpus from the issue report: compare bounded/unbounded list,
search-with-counts and ready runs, baseline vs. fixed, with `EXPLAIN` for the
plan shapes. A dominant residual scan after this change is the evidence that
would justify revisiting a composite index (see
`internal/storage/dolt/keyset_search_test.go` for the recorded 2.2.0
plan-neutrality measurement that argues against adding one today).
