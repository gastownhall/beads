# Release gate — be-3ht (SearchIssueSummaries narrow projection)

- **Bead:** be-3ht (review bead for build be-nu4.3.2 / ADR be-nu4 §4.D3 + addendum be-nu4.3.1)
- **Commits shipped:**
  - `ca547f31` — cherry-pick of `5023e0e1` (D3 base, `perf(storage): add SearchIssueSummaries…`)
  - `b52764a9` — cherry-pick of `1915d30d` (D3 fix, `fix(test): D3 fixture SkipPrefixValidation per reviewer (be-3ht)`)
- **Branch:** `release/be-nu4.3-stack` (stacks on `release/be-eqw` / PR #3453)
- **PR:** #3458
- **Evaluated:** 2026-04-24 by beads/deployer

## Scope note

The release branch `release/be-nu4.3-stack` stacks D3 on top of D2
(`release/be-eqw`), because `SearchIssueSummaries` label hydration calls
`GetLabelsForIssuesInTx` with D2's wisp set helper. Merge ordering:
#3453 (D2) must land before #3458 (D3).

One additional commit rides on this branch, outside this bead's scope:

- `5df7b257` — `test(cli): unit tests for sortSummaries + useSummary selector (be-2kl)`

That commit is a test-only follow-up tracked separately as be-2kl. It
does not affect D3's acceptance criteria and does not change D3's
production-code surface; the gate below is evaluated against the D3
commits alone, with the be-2kl tests co-riding on PR #3458.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | First-pass review verdict `PASS` in be-3ht notes on commit `1915d30d` (re-review after earlier REQUEST-CHANGES on `5023e0e1` was resolved via fixture `SkipPrefixValidation` flip). Gemini second-pass currently disabled per project policy. |
| 2 | Acceptance criteria met | **PASS** | Render parity (compact + `--agent`) byte-for-byte at 1K rows including pinned perm + pinned wisp — PASS (`TestSummaryRenderParity`). ID parity per `IssueFilter` dimension at 1K rows — PASS all 15 subtests (reviewer log, commit `1915d30d`). Pinned-fixture-present guard — PASS. `SearchIssues` unchanged. `BenchmarkSearchIssueSummaries_1K` executes end-to-end (539 ms/op reviewer-side, 284 ms/op builder-side — well inside FR-4 budget of ≤2s p95 at 49K with projection narrowing). `IssueSummary` shape matches addendum be-nu4.3.1 (`Pinned` yes, `Metadata` no). No correlated EXISTS / recursive CTEs. |
| 3 | Tests pass | **PASS** | See "Tests run on release branch" below. |
| 4 | No high-severity review findings open | **PASS** | 0 HIGH findings. The two earlier blockers (fixture prefix-validation on both the parity seed and the bench seed) were closed by commit `1915d30d` and confirmed PASS on re-review. |
| 5 | Final branch is clean | **PASS** | `git status` on `release/be-nu4.3-stack` shows nothing except worktree-scaffolding untracked paths (`.gc/`, `.gitkeep`) that are never staged. |
| 6 | Branch diverges cleanly from main | **PASS (stacked)** | Branch stacks on `release/be-eqw` (PR #3453 D2). Cherry-picks of D3's two commits applied with zero conflicts. Merge ordering: #3453 → #3458. Once D2 lands on `origin/main`, this branch becomes a clean diff against main. |

## Tests run on release branch

| Test | Result | Notes |
|------|--------|-------|
| `go build ./...` | PASS | Go 1.26.2 via `GOTOOLCHAIN=auto`. |
| `go vet ./...` | clean | No output. |
| `TestSummaryRenderParity` (container-free, the designer-audit §3 hard gate) | PASS 0.959s | Byte-for-byte equality of `formatIssueCompact` vs `formatSummaryCompact` AND `formatAgentIssue` vs `formatSummaryAgent` at 1K fixture rows including one pinned permanent + one pinned wisp. |
| `TestFormatIssueCompact`, `TestFormatAgentIssue`, `TestSortSummaries` | PASS 0.620s | No regression in compact/agent render paths or new `sortSummaries` helper. |
| `TestSearchIssueSummaries_IDParity` (reviewer, container-dependent) | PASS all 15 subtests | Per-IssueFilter-dimension at 1K rows, including `pinned_only`, `ephemeral_true/false`, `label_all/any`, `title_contains`. Deployer did not re-run container tests; relying on reviewer's 18.38s PASS at commit `1915d30d`. |
| `TestSearchIssueSummaries_PinnedFixturePresent` (reviewer, container-dependent) | PASS 17.50s | Fixture-shape guard, confirmed clean after fixture fix. |

## Out of scope (per bead, intentionally deferred)

- `--long` migration to summaries — stays on `SearchIssues` per
  addendum be-nu4.3.1; one-line comment at the `--long` call site in
  `cmd/bd/list.go:1077` names the addendum.
- Other list-shaped consumers (`bd graph`, `bd agent`, `bd ready`) —
  P3 follow-ups per bead.
- `Metadata` on `IssueSummary` — intentionally excluded per addendum;
  would defeat D3's perf win.

## Verdict

**PASS** — commit gate markdown to `release/be-nu4.3-stack`; push to
`fork` (origin is locked for quad341 per `release/be-eqw` precedent);
PR #3458 is already open against `gastownhall/beads:main` stacking
on #3453.
