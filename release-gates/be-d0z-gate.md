# Release gate — be-d0z (CountIssues / CountIssuesGroupedBy)

- **Bead:** be-d0z (review bead for build be-nu4.1.1 / ADR be-nu4 §4.D1)
- **Commit shipped:** `b84ebda0` — cherry-pick of `c80bdfe0` (builder branch `gc-builder-e35c0415a93c`)
- **Branch:** `release/be-d0z` (stacks on `release/be-nu4.3-stack` — PR #3458 — which stacks on `release/be-eqw` — PR #3453)
- **Evaluated:** 2026-04-24 by beads/deployer

## Scope note

The D1 commit's interface additions (`CountIssues`,
`CountIssuesGroupedBy` on `storage.Storage`) are positioned in
`internal/storage/storage.go` immediately after `SearchIssueSummaries`.
That places a positional dependency on D3 being already applied in the
cherry-pick base — not a semantic dependency (the Count methods don't
call into `SearchIssueSummaries`), just where the diff's context lines
need to land. A cherry-pick onto `release/be-eqw` (D2 only) fails with
a storage.go context conflict; onto `release/be-nu4.3-stack` (D2+D3)
it applies cleanly.

Choice: stack this PR on #3458. Merge ordering: #3453 → #3458 →
this PR. Each of the three has independently passing gate criteria;
this PR carries only the D1 diff on top of #3458's HEAD.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | First-pass review verdict `PASS` recorded in be-d0z notes on commit `c80bdfe0`. Gemini second-pass currently disabled per project policy. |
| 2 | Acceptance criteria met | **PASS** | Filter-parity at 1K rows across every `IssueFilter` dimension (16 subtests) — PASS per reviewer. Grouped-by parity across `status / priority / issue_type / assignee / label` × 3 filters (15 subtests) — PASS. Allowlist-rejects with named-field error (6 subtests) — PASS. Wisp-admission mirrors `SearchIssuesInTx` (Ephemeral=true/nil/false). Label grouping two-phase via `GetLabelsForIssuesInTx` with the D2 wisp set. `renderGroupKeys` preserves count sums on display-string collisions; byte-for-byte display parity with pre-D1 `bd count` output retained. `BenchmarkCountIssues_1K` 1.83ms / `BenchmarkCountIssuesGroupedBy_status_1K` 4.36ms — well inside FR-1 (≤250ms at 49K) and FR-2 (≤500ms at 49K). |
| 3 | Tests pass | **PASS** | See "Tests run on release branch" below. |
| 4 | No high-severity review findings open | **PASS** | 0 HIGH findings. Reviewer's note called out three `fmt.Sprintf` sites in `countTableInTx`, `groupByColumnSingleTableInTx`, `filteredIDsInTx` and verified they conform to the "tables hardcoded, columns from allowlist, args via `?`" contract — no new injection surface. |
| 5 | Final branch is clean | **PASS** | `git status` on `release/be-d0z` shows nothing except worktree-scaffolding untracked paths (`.gc/`, `.gitkeep`) that are never staged. |
| 6 | Branch diverges cleanly from main | **PASS (stacked)** | Triple stack: `release/be-eqw` (D2, #3453) → `release/be-nu4.3-stack` (D3, #3458) → `release/be-d0z` (D1, this PR). D1 cherry-pick applied to the D3 head with zero conflicts. Once #3453 and #3458 land on `origin/main`, this branch becomes a clean diff against main. |

## Tests run on release branch

| Test | Result | Notes |
|------|--------|-------|
| `go build ./...` | PASS | Go 1.26.2 via `GOTOOLCHAIN=auto`. |
| `go vet ./...` | clean | No output. |
| `TestCountReferences / TestCountExistingIssues_* / TestCountMetadataKeys` (cmd/bd, non-container) | PASS 0.795s | No regression from the `type` → `issue_type` internal rename. |
| `TestCountIssues_FilterParity` (dolt, container-dependent) | PASS all 16 subtests | Per reviewer's 24.25s run with `TESTCONTAINERS_RYUK_DISABLED=true`. Deployer did not re-run container tests; relying on reviewer PASS at commit `c80bdfe0`. |
| `TestCountIssuesGroupedBy_Parity` | PASS all 15 subtests | Per reviewer's 25.04s run. |
| `TestCountIssuesGroupedBy_AllowlistRejects` | PASS all 6 subtests | Error message names every valid field value. |

## Out of scope (per bead, intentionally deferred)

- Caching / materialized views / denormalized counters (ADR §11.5,
  tracked as D5 follow-up be-nu4.5).
- `SearchIssues` changes (D3 ships the summary projection; D1 is
  count aggregation only).
- Flag or output-format changes on `bd count` (ADR §11.3).

## Verdict

**PASS** — commit gate markdown to `release/be-d0z`; push to `fork`
(origin is locked for quad341 per `release/be-eqw` / #3453 precedent);
open PR against `gastownhall/beads:main`, stacking on #3458.
