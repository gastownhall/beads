# Release gate — be-l9q (`bd list --skip-labels` AD-02)

**Date:** 2026-04-27
**Deployer:** beads/deployer (deployer-1, second pass after builder rebase)
**Bead (review):** be-l9q — Review: bd list --skip-labels (AD-02)
**Feature bead:** be-a5z (closed)
**Builder commit (post-rebase):** `5e6d84b4` (was `bbfa50df` pre-rebase)
**Source branch:** `be-vzu-rebase-fix` (builder worktree, rebased onto current `origin/main`)
**Final branch:** `release/be-l9q` @ `c4b7e0ac` (cherry-pick of `5e6d84b4` onto `origin/main`)
**Base:** `origin/main` @ `f4c46d91` ("/go.{mod,sum}: bump dolt driver (#3435)")

## Verdict: PASS

## What this ships

`bd list --skip-labels` — a hydration toggle for the list command that:

- Skips the `GetLabelsForIssuesInTx` SQL JOIN at the search-layer when set,
  with a defense-in-depth gate at the `cmd/bd` post-query step too.
- Emits `labels: []` in JSON via a `skipLabelsIssueView` wrapper that
  overrides the `omitempty` on `Issue.Labels`, so machine consumers can
  always parse the field.
- Surfaces the suppression in human output: footer in pretty/tree and
  buffered/compact paths (gated by `!isQuiet()`); `--long` swaps the
  Labels block to `Labels: (suppressed by --skip-labels)`.
- Refuses combination with the six existing label-filter flags with
  exit code 2 and a Wireframe-5 error message.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | reviewer-1 PASS + reviewer-2 (concur) PASS in be-l9q notes. |
| 2 | Acceptance criteria met | PASS | All 5 ACs walked + Wireframes 1-6 covered in reviewer-1's matrix. |
| 3 | Tests pass | PASS | Targeted suite all green; broader sweep failures are pre-existing on `origin/main` (see below). |
| 4 | No HIGH-severity review findings open | PASS | No HIGH findings — one LOW advisory (storage-gate test is behavioral not query-counter) is non-blocking. |
| 5 | Final branch is clean | PASS | `git status` clean after gate commit; only untracked items unrelated to this bead. |
| 6 | Branch diverges cleanly from main | PASS | `git cherry-pick 5e6d84b4` onto `origin/main` applied without conflict. All 7 touched files were byte-identical on `origin/main` and `5e6d84b4^`, so the rebased commit lands without drift. |

## Test evidence (criterion 3)

Run from `release/be-l9q` @ `c4b7e0ac`:

- `go vet -tags gms_pure_go ./cmd/bd/... ./internal/storage/issueops/... ./internal/types/...`: clean (exit 0).
- Targeted: `go test -tags gms_pure_go -run 'TestFormatIssueLong|TestSkipLabels|TestFormatSkipLabelsConflictError' ./cmd/bd/`: PASS (1.081s). Includes:
  - `TestSkipLabelsConflicts` (8 subs)
  - `TestSkipLabelsIssueView_AlwaysEmitsLabelsArray`
  - `TestFormatSkipLabelsConflictError`
  - `TestFormatIssueLong*` (open/closed/with assignee/with labels/with metadata variants)
- Broader sweep: `go test -tags gms_pure_go -count=1 ./cmd/bd/ ./internal/storage/issueops/ ./internal/types/`:
  - `internal/storage/issueops`: PASS
  - `internal/types`: PASS
  - `cmd/bd`: 2 failures, both **pre-existing on `origin/main`**:
    - `TestWhereCommand_ReadsPrefixFromEmbeddedStore` — verified FAIL on bare `origin/main` (`f4c46d91`) checkout.
    - `TestResolveWhereBeadsDir_UsesInitializedDBPath` — passes in isolation; fails only when run alongside the above due to shared test state. Same behavior on bare `origin/main`.
  - Both failures are in `bd where` test infrastructure and have no code path through `cmd/bd/list.go`, `internal/storage/issueops/search.go`, or any file touched by `5e6d84b4`. Builder and reviewer-2 both flagged this `cmd/bd` test-suite environmental flakiness on parent commits in their notes.

## Cherry-pick mechanics (criterion 6)

The first deployer pass FAILed criterion 6 because the original `bbfa50df`
conflicted with upstream `bb6b8f22` (#3481, "Fix ready/list UX regressions")
in `cmd/bd/list_format.go`. The builder rebased `be-vzu-rebase-fix` onto
current `origin/main` (`f4c46d91`), resolved the conflict in the
`formatIssueLong` Description-block-vs-`labelsSkipped` interleaving (per the
suggested form in the prior FAIL note), and produced `5e6d84b4` as the new
be-a5z commit.

The deployer cherry-picked just `5e6d84b4` (not the entire 6-commit branch)
because the review of be-l9q covers only the be-a5z change. Pre-flight check
confirmed every file touched by `5e6d84b4` is byte-identical between
`origin/main` and `5e6d84b4^` (the rebased be-a5z parent in the builder
chain), so cherry-picking the single commit produces the same tree it would
on the rebased branch. Cherry-pick applied without conflict; `git status`
clean.

| File | `origin/main` blob | `5e6d84b4^` blob |
|---|---|---|
| `cmd/bd/list.go` | `54c277a1` | `54c277a1` |
| `cmd/bd/list_format.go` | `e723a973` | `e723a973` |
| `cmd/bd/list_skip_labels_test.go` | (new) | (new) |
| `cmd/bd/list_test.go` | `e4640f29` | `e4640f29` |
| `cmd/bd/show_format_metadata_test.go` | `cc988f92` | `cc988f92` |
| `internal/storage/issueops/search.go` | `b9d297ca` | `b9d297ca` |
| `internal/types/types.go` | `45f902bf` | `45f902bf` |
