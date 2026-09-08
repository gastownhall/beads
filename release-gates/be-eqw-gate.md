# Release gate — be-eqw (WispIDSetInTx amortized wisp routing)

- **Bead:** be-eqw (review bead for build be-nu4.2.1 / ADR be-nu4 §4.D2)
- **Commit shipped:** 12ab3647 (cherry-pick of 61cfc45c from gc-builder-e35c0415a93c)
- **Branch:** `release/be-eqw` off `origin/main`
- **Evaluated:** 2026-04-23 by beads/deployer

## Scope note

The builder worktree `gc-builder-e35c0415a93c` carries two commits on top of
`origin/main`:

- `61cfc45c` — D2 build (be-nu4.2.1, reviewed PASS as be-eqw) — **shipped**
- `5023e0e1` — D3 build (be-nu4.3.2, review be-3ht still open) — **excluded**

This PR cuts a clean branch off `origin/main` with only the D2 commit, so the
un-reviewed D3 change does not ride along.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | reviewer-1 recorded `Verdict: PASS` in be-eqw notes (single-pass; gemini second-pass currently disabled). Findings F1 (medium scope-completeness), F2 (low benchmark-numbers) are advisory/follow-up, not blockers. |
| 2 | Acceptance criteria met | **PASS** | Reviewer trace table: hot-path callers make one wisp-id query per invocation ✓; mixed-ID routing hard gate ✓; `IsActiveWispInTx` retained for single-ID paths ✓; benchmark code at 1K/10K/50K with ≥25% wisp share ✓; no correlated EXISTS / recursive CTEs / UNION ALL ✓. Full-audit gap tracked as explicit follow-up (see §F1 below). |
| 3 | Tests pass | **PASS** | See "Tests run on release branch" below. |
| 4 | No high-severity review findings open | **PASS** | 0 HIGH findings. F1=medium, F2=low. |
| 5 | Final branch is clean | **PASS** | `git status` on `release/be-eqw` shows nothing except worktree-scaffolding untracked paths (`.gc/`, `.gitkeep`) that are never staged. |
| 6 | Branch diverges cleanly from main | **PASS** | Branch cut fresh from `origin/main` via `git checkout -B release/be-eqw origin/main`; `git cherry-pick 61cfc45c` applied with zero conflicts. |

## Tests run on release branch

| Test | Result | Notes |
|------|--------|-------|
| `go build ./...` | PASS | Go 1.26.2 via `GOTOOLCHAIN=auto`. |
| `go vet ./...` | clean | No output. |
| `gofmt -l` on 6 changed files | clean | No output. |
| `TestPartitionByWispSet` (pure-fn, authoritative gate per design bead) | PASS 0.006s | 6 subcases: all_permanent, all_wisps, mixed, empty_input, nil_wisp_set_treats_all_as_permanent, explicit-id_wisp_routes_as_wisp. |
| `TestWispIDSetInTx_HardGate` + `TestWispIDSetInTx_Empty` (Dolt testcontainer, `TESTCONTAINERS_RYUK_DISABLED=true`) | PASS 2.94s | Container flakiness builder reported did not reproduce on deployer pass; matches reviewer's observation. |
| `go test ./internal/storage/issueops/... ./internal/types/... ./internal/ui/...` | PASS | All container-free packages clean. |

## Findings tracked from review

**F1 (medium, scope-completeness — advisory):** Reviewer identified three
additional N-per-ID `IsActiveWispInTx` hot loops the builder did not flag
(`DeleteIssuesInTx`, `GetCommentCountsInTx`, `GetCommentsForIssuesInTx`) on
top of the two already flagged (`GetDependencyRecordsForIssuesInTx`,
`GetBlockingInfoForIssuesInTx`). Design bead be-nu4.2.1 scoped work to the
two named sites, so the build is correct as-delivered; a consolidated
follow-up bead covering all five remaining sites must exist before the
be-nu4 epic closes. Not a release blocker.

**F2 (low, benchmark numbers not captured — advisory):** Benchmark code
ships and compiles; runtime 1K/10K/50K numbers were not captured because
the Dolt server / testcontainer environment is flaky in both the builder
and reviewer sandboxes (Docker 29.4 + ryuk 0.13.0). Release-gate decision:
defer numeric capture to a clean CI environment; code-level deliverable
(the benchmarks exist) is satisfied per ADR §11.2.

## Verdict

**PASS** — push to `fork` (origin is locked for quad341; `fork =
quad341/beads`), open PR against `gastownhall/beads:main`.
