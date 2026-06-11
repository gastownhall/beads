# Release Gate: be-mk4w — routed-write OpenWritable + architecture alignment (be-w9fg, be-jb55)

**Date:** 2026-06-10
**Bead:** be-mk4w
**Commit:** f1ff1fbc5b0ff70d280f8d97daa688e096ef1d27
**Branch:** quad341:fix/routed-write-readonly-regression
**Deployer:** beads/deployer

---

## Gate Verdict: PASS

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | **PASS** | Reviewer verdict PASS (2026-06-10) in be-4wox notes by beads/reviewer. Full spec compliance verified for both be-w9fg (Layer 1) and be-jb55 (Layer 2). |
| 2 | Acceptance criteria met | **PASS** | be-w9fg: RoutedSchemaBehindError struct/Error()/helper exact match; checkBackwardDrift COALESCE(MAX(version),0) with v==0 nil sentinel and v<LatestVersion error; OpenWritable readOnly=false, skips gate/MigrateUp, runs CheckForwardDrift+checkBackwardDrift. be-jb55: newWritableNoMigrateStoreFromConfig (cgo+nocgo); resolveViaPrefixRouting forWrite removed; openRoutedReadStore alias kept for test compat; all callers updated (close.go, list.go, ready.go, routed.go). |
| 3 | Tests pass | **PASS** | `go build ./...` clean. `go vet ./...` clean. All 8 required tests PASS: TestEmbeddedOpenWritable_SkipsGateAndMigrations, TestEmbeddedOpenWritable_AllowsWriteTransactions, TestEmbeddedOpenWritable_RejectsSchemaAheadDB, TestEmbeddedOpenWritable_RejectsSchemaBehindNonZeroDB, TestEmbeddedOpenWritable_FreshDBSkipsBackwardDriftCheck, TestEmbeddedOpenReadOnly_SkipsGateAndMigrations, TestEmbeddedUpdateRoutedStoreCommitsTargetHead, TestEmbeddedRoutedSiblingWritesCommitTargetHead (comment/note/reopen). |
| 4 | No high-severity findings open | **PASS** | Reviewer observations: (1) openRoutedReadStore forwarding alias unexported with no remaining callers — compiles harmlessly, acceptable; (2) FreshDB test documents Dolt sql.ErrNoRows behavior — workaround correct and explained. Both non-blocking. Zero HIGH findings. |
| 5 | Final branch is clean | **PASS** | `git status` clean. Untracked .gc/, .gitkeep, bd-no-dolt, bench/ are build/rig artifacts, not staged. No uncommitted changes. |
| 6 | Branch diverges cleanly from main | **PASS** | `git merge-tree origin/main HEAD` exits 0. origin/main at 001c6c258532c9ecb69a3a9c128fe4edaf9b5aa3. No merge conflicts. |
| 7 | Single feature theme | **PASS** | All 3 commits touch the embeddeddolt/routing layer exclusively: routed-write writable target store, OpenWritable alignment + routing factory rename, OpenWritable unit tests. One logical feature: ensuring routed writes have a proper writable store path with schema drift guards. |

---

## Review Summary

- **First-pass reviewer:** beads/reviewer — PASS (2026-06-10)
- **Second-pass (gemini):** not required (single-pass policy)
- **Reviewer findings:** Non-blocking observations only (forwarding alias, FreshDB test doc). Zero HIGH findings.

## Commit Set

| Commit | Message |
|--------|---------|
| 89b3c4686 | fix(embeddeddolt): give routed writes a writable target store (be-cibr) |
| 1296758cd | fix(embeddeddolt): align OpenWritable + routing factories to architect design (be-w9fg, be-jb55) |
| f1ff1fbc5 | test(embeddeddolt): add 5 OpenWritable unit tests required by be-w9fg spec |

## Files Changed (vs origin/main)

- `cmd/bd/close.go` — caller updated to newWritableNoMigrateStoreFromConfig
- `cmd/bd/list.go` — caller updated
- `cmd/bd/ready.go` — caller updated
- `cmd/bd/routed.go` — resolveViaPrefixRouting forWrite bool removed
- `cmd/bd/routing_read.go` — openRoutedReadStore → openRoutedStore; forwarding alias retained
- `cmd/bd/store_factory.go` — newWritableNoMigrateStoreFromConfig (cgo path)
- `cmd/bd/store_factory_nocgo.go` — newWritableNoMigrateStoreFromConfig (no-cgo path)
- `internal/storage/embeddeddolt/open_writable_test.go` — 5 new OpenWritable unit tests
- `internal/storage/embeddeddolt/store.go` — RoutedSchemaBehindError, checkBackwardDrift, OpenWritable implementation
