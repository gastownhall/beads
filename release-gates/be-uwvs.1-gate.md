# Release gate — be-uwvs.1 (Lite SELECT shape storage-layer foundation)

- **Build bead:** be-uwvs.1 (closed) — Lite SELECT shape: storage-layer foundation
- **Review bead:** be-mbcn — Verdict PASS (Opus 4.7 reviewer, single-pass; gemini second-pass currently disabled)
- **Commit shipped:** `6001dd4e8` (one commit, +453/-2 over `origin/main` da73b7511)
- **Branch:** `feat/be-uwvs.1-lite-select-foundation` on `fork` (`quad341/beads`)
- **Evaluated:** 2026-05-12 by beads/deployer

## Scope

Storage-layer foundation for `IssueFilter.Lite`. Zero behavior diff at the
default (`Lite=false`). Five files: `internal/types/types.go` (adds
`Issue.IsLitePartial`, `IssueFilter.Lite`), `internal/storage/issueops/scan.go`
(adds `IssueSelectColumnsLite`, `HeavyDropList`, `ScanIssueLiteFrom`),
`internal/storage/issueops/search.go` (`searchTableInTx` switch on
`filter.Lite`), `internal/storage/issueops/scan_test.go` (new — structural
parity test + happy-path tests), `docs/EXTENDING.md` (new — caller contract).

CLI wiring (be-uwvs.2), `WorkFilter.Lite` mirror (be-uwvs.3), per-backend
correctness tests (be-uwvs.4 validator), filter-spy gates (be-uwvs.5
validator), and benchmarks are deferred to follow-up beads per the design.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | beads/reviewer recorded `Review verdict: PASS` on be-mbcn 2026-05-12 11:03. Findings F1 (cosmetic missing godoc-comment) and F2 (positional-scan risk inherent to the existing codebase) are info-only, no action. |
| 2 | Acceptance criteria met | **PASS** | All six acceptance items in the be-uwvs.1 bead checked against code (see "Acceptance" below). |
| 3 | Tests pass | **PASS** | See "Tests run on release branch" below. |
| 4 | No high-severity review findings open | **PASS** | Zero HIGH findings. F1/F2 are info-level, both explicitly marked "no action required". |
| 5 | Final branch is clean | **PASS** | `git status` on the feature branch shows only the worktree-scaffolding untracked paths (`.gc/`, `.gitkeep`) which are never staged. |
| 6 | Branch diverges cleanly from main | **PASS** | `git merge-base --is-ancestor origin/main HEAD` confirms HEAD descends from `origin/main` (single commit on top of da73b7511; no rebase needed). |

## Acceptance (per be-uwvs.1)

| Criterion | Status | Evidence |
|---|---|---|
| Five new symbols with godoc (`Issue.IsLitePartial`, `IssueFilter.Lite`, `IssueSelectColumnsLite`, `HeavyDropList`, `ScanIssueLiteFrom`) | ✓ | All present and documented. |
| `searchTableInTx` switches on `filter.Lite` | ✓ | `internal/storage/issueops/search.go` switch in place; lite path opt-in only. |
| `TestIssueSelectColumns_LitePlusHeavyEqualsFull` is structural + actionable | ✓ | Asserts `cols(IssueSelectColumns) == cols(IssueSelectColumnsLite) ∪ HeavyDropList`; failure message names the actionable classification step. |
| Happy-path tests for both scan helpers (Lite: heavy fields blank + `IsLitePartial=true`; Full: heavy fields populated + `IsLitePartial=false`) | ✓ | `TestScanIssueLiteFrom_LeavesHeavyFieldsBlank`, `TestScanIssueFrom_PopulatesHeavyFields` PASS. |
| EXTENDING.md documents caller contract | ✓ | New file with the Lite-fetch caller contract. |
| No CLI changes; `Lite` defaults `false` everywhere | ✓ | Grep confirms one switch site and one usage site; both new; default-false at every existing call site. |

## Tests run on release branch

| Test | Result | Notes |
|------|--------|-------|
| `go test -tags gms_pure_go ./internal/storage/issueops/ ./internal/types/` | PASS | Targeted change packages. |
| `go test -tags gms_pure_go ./internal/storage/embeddeddolt/ ./internal/storage/schema/ ./internal/storage/versioncontrolops/` | PASS | Downstream consumers of `SearchIssuesInTx`. |
| `go test -tags gms_pure_go -run 'TestIssueSelectColumns_LitePlusHeavyEqualsFull\|TestScanIssueLiteFrom\|TestScanIssueFrom' -v ./internal/storage/issueops/` | PASS (3/3) | Structural parity + both happy-path tests, named. |
| `go vet -tags gms_pure_go ./internal/storage/issueops/ ./internal/types/` | clean | No output. |
| `gofmt -l` on the four changed `.go` files | clean | No output. |

The reviewer's pre-existing rig-isolation failure
(`internal/storage/dolt/TestApplyConfigDefaults_*` env-leak from
`GC_DOLT_PORT`) is documented as unrelated; the fix lands separately under
be-lql / be-ic2 on the postgres-backend branch. Not a blocker here — those
tests do not exercise the changed code paths.

## Findings from review (no action)

- **F1 (info):** `ScanIssueLiteFrom` omits the `// Custom metadata field
  (GH#1406)` comment present in `ScanIssueFrom`. Cosmetic.
- **F2 (info):** Positional `Scan(&dest...)` argument order in
  `ScanIssueLiteFrom` must stay in lockstep with `IssueSelectColumnsLite`.
  Inherent to positional scans across the codebase, not introduced here.
  End-to-end fidelity is caught by validator bead be-uwvs.4.

## Push target

`origin` (`gastownhall/beads`) denies push to `quad341` (deployer's GitHub
identity); `fork` (`quad341/beads`) accepts. PR opens cross-repo against
`gastownhall/beads:main` with head `quad341:feat/be-uwvs.1-lite-select-foundation`.

## Verdict

**PASS** — push the gate-file commit to `fork`, open PR.
