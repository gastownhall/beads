# Release gate — Incremental auto-export via `dolt_diff` (be-hka), round 4

- **Builder bead (CLOSED):** be-hka — incremental `bd export --auto` using
  `dolt_diff()` to patch only changed issues instead of rewriting the full
  JSONL file on every export.
- **Deploy bead:** be-y1jo (round 2 of deploy; supersedes be-uoat, which
  FAILed on criterion 3 — see `release-gates/be-uoat-dolt-diff-export-gate.md`)
- **Review beads:**
  - be-fgd — round 1, findings led to round-2 rework (dolt test-store hang)
  - be-wu03 — round 2, verdict **PASS**, commit `d8f6563393a3f` (this is the
    commit be-uoat evaluated and FAILed on criterion 3 only)
  - be-8wnq — round 3, verdict **REQUEST CHANGES**, commit `f35368477` — fixed
    be-uoat's flake at the root (see below) but introduced a genuine,
    deterministic test-logic defect in the new plumbing-proof test
  - be-unlq — round 4, verdict **PASS**, commit `fc5caadcf63371f6b992f32d23ef4d8c2a71e5ee`
- **Commit:** `fc5caadcf63371f6b992f32d23ef4d8c2a71e5ee` — 5 commits over
  `origin/main` (`5e94e0cc5`, `561c45a03`, `9c4f2e798`, `f35368477`,
  `fc5caadcf`), 7 files
- **Branch:** `builder/be-hka` (provenance only); deploy branch
  `deploy/be-y1jo-gate` cut from `fc5caadcf` and pushed to `headfork`
  (`quad341/beads-sec003-contrib`, confirmed GitHub fork-parent of
  `gastownhall/beads`) — per be-eh6 precedent and this feature's own round-2
  gate note, `fork` (`quad341/beads`) is the pre-migration URL and is avoided
  to sidestep the rename-redirect ambiguity.
- **Evaluated:** 2026-08-15 by beads/deployer

## Scope

Same feature as be-uoat's round-2 gate: incremental auto-export via
`dolt_diff()`, patching only changed issue lines instead of rewriting the
whole JSONL file on every export. This round makes **zero** further changes
to feature/production logic beyond what be-uoat already gated — the
production files (`cmd/bd/export_auto.go`, `internal/storage/diff_store.go`,
`internal/storage/dolt/versioned.go`) are unchanged since round 2. Rounds 3
and 4 are both test-infrastructure-only, addressing be-uoat's criterion-3 FAIL:

- **Round 3** (`9c4f2e798` red, `f35368477` green): root-caused be-uoat's
  flake to the pool's `defaultPoolReadTimeout = 10s` colliding with the
  5001-row bulk seed in `TestTryIncrementalExport_ThresholdExceededFallsBack`
  under host contention — exactly as diagnosed in be-uoat's gate. Fixed by
  adding `newTestStoreSharedBranchWithReadTimeout` /
  `newTestStoreWithPrefixAndReadTimeout` (siblings of the existing
  unparameterized helpers) that thread a caller-supplied
  `dolt.Config.PoolReadTimeout` through both the shared-branch fast path and
  the per-test-DB fallback. The flaky test now opts into
  `bulkSeedPoolReadTimeout = 5 * time.Minute`
  (`cmd/bd/export_auto_test.go:1425`) via
  `setupIncrementalExportTestWithReadTimeout` — this is the exact
  evidence-calibrated-long-timeout fix be-uoat's gate recommended, arrived at
  independently. Existing callers keep the 10s default; only this one
  bulk-seed path opts in. Round-3 review (be-8wnq) independently soaked the
  fixed test 3/3 PASS (413.24s/257.68s/316.48s, package-scoped,
  `BEADS_TEST_ENV_RUN_DOLT=1`, `-timeout 40m`) but REQUEST-CHANGES'd on a
  separate, genuine defect: the new plumbing-proof test
  (`TestNewTestStoreWithReadTimeout_AppliesConfiguredTimeout`) wrapped its
  expected-to-fail case in `t.Run` and checked the bool return — but a failed
  Go subtest unconditionally propagates to the parent/package result
  regardless of that bool, so the test could never report an overall PASS
  even when the timeout plumbing was working correctly. Deterministic, not a
  flake.
- **Round 4** (`fc5caadcf`, refs be-8wnq): fixes the round-3 defect by adding
  `tryNewTestStoreWithReadTimeout`, which returns the error directly instead
  of calling `t.Fatal` inside a subtest; the test now asserts on that error
  value with no subtest wrapper for the expected-to-fail branch. Round-4
  review (be-unlq) confirmed this live against a real Dolt container (PASS,
  0.09s, not a self-skip) plus a full default-mode `./cmd/bd/...` run (5/5
  packages, 0 FAIL, 246.3s).

Diff scope, confirmed directly via `git diff --name-only origin/main...fc5caadcf`
(7 files, identical set to be-uoat's already-approved scope):

- `cmd/bd/export_auto.go` — feature logic (unchanged since round 2)
- `internal/storage/diff_store.go` — feature logic (unchanged since round 2)
- `internal/storage/dolt/versioned.go` — feature logic (unchanged since round 2)
- `cmd/bd/export_auto_test.go` — feature tests (round 3+4: PoolReadTimeout plumbing)
- `cmd/bd/test_helpers_test.go` — shared test infra (round 3+4: PoolReadTimeout plumbing)
- `cmd/bd/test_dolt_server_cgo_test.go` — shared test infra (round 2, carried forward)
- `internal/testutil/testdoltserver.go` — shared test infra (round 2, carried forward)

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-unlq records `verdict: pass` for round 4 on commit `fc5caadcf`, full style/security/spec write-up, zero findings. |
| 2 | Acceptance criteria met | **PASS** | be-hka's 6 ACs re-checked directly: branch off clean `origin/main` containing only the feature's net diff + strictly-required infra files (confirmed via `git diff --stat`); no unrelated file touched; `c0b942088` and all other shared-branch commits absent from the 5-commit ancestry (confirmed via `git log --oneline origin/main..fc5caadcf`); `go build`/`go vet` clean (independently re-run, see below); `export_auto_test.go` suite passes (independently re-run, see below); submitted via normal handoff each round (be-fgd → be-wu03 → be-8wnq → be-unlq chain, all verified claimable). |
| 3 | Tests pass | **PASS** | All 8 diff-owned tests green on independent re-run, including 3 fresh real-container runs of the previously-flaky test with zero failures. See "Tests run" below. |
| 3b | Policy/lint lane | **PASS** (documented exception) | `make ci-pr-policy` technically FAILs, root-caused and independently confirmed to a pre-existing gap unrelated to this diff. See below. |
| 4 | No unresolved HIGH findings | **PASS** | Zero HIGH or MEDIUM findings across all 4 review rounds. Round 3's REQUEST CHANGES was a deterministic test-logic defect (not a HIGH-severity finding), fixed in round 4. One informational, non-blocking, carried-forward item from round 1/2 (`isSafeCommitRef` lacks a dedicated direct unit test). |
| 5 | Clean working tree | **PASS** | `git status --short` on `fc5caadcf` shows nothing staged/unstaged; only the 3 pre-existing, unrelated untracked scratch files already present in this worktree (`release-gates/be-hi97-no-workspace-tests-gate.md`, `release-gates/be-uoat-dolt-diff-export-gate.md`, `scripts/rebase-resolve-lib.sh`). |
| 6 | Clean divergence from `origin/main` | **PASS** | `git merge-base --is-ancestor origin/main fc5caadcf` succeeds — `fc5caadcf` is a clean fast-forward-able descendant, 5 commits ahead, 0 behind. No rebase needed. |
| 7 | Single feature theme | **PASS** | Same 7-file scope as be-uoat's already-approved round-2 gate; all files serve the one incremental-export feature or the test infra its fix strictly requires. |

### Ancestry-scope check: documented exception on one commit

`assert_deploy_ancestry_scope origin/main fc5caadcf be-fgd be-uoat be-8wnq`
returned rc=21, flagging exactly one commit as citing none of the accepted
bead ids:

```
5e94e0cc5 perf(export): incremental auto-export via dolt_diff
```

The `.claude/**` denylist check (check 1) passed cleanly — no output, no
refusal. Only the stray-commit check (check 2) fired, and only on this one
commit.

This is a known, structurally-expected false positive, not a be-27c-shaped
contamination, for a specific reason: `5e94e0cc5` **predates be-hka
entirely** — it is the original feature commit (authored 2026-04-19) that
already existed, unreviewed, on the long-lived shared branch
`gc-builder-e35c0415a93c` before be-27c's scope audit (2026-08-15) discovered
it and filed be-hka specifically to extract it onto a clean branch (see
be-hka's own description). No bead existed to cite at authorship time, and no
legitimate bead id substring-matches an empty reference — passing one to
force a match would be exactly the "blanket --force flag" behavior the
function's own docstring warns against, so none was passed.

Independently verified this is benign rather than taking the extraction
narrative on faith: `git show --stat --format='' 5e94e0cc5` shows exactly 4
files (`cmd/bd/export_auto.go`, `cmd/bd/export_auto_test.go`,
`internal/storage/diff_store.go`, `internal/storage/dolt/versioned.go`) — a
strict subset of the already-approved 7-file cumulative scope, zero overlap
with `.claude/**`, zero unrelated files. Proceeding past this flag with that
evidence recorded, per the deployer's judgment-call authority for exactly
this kind of mechanical-check edge case.

## Tests run on release branch (independent re-verification)

Static checks, independently re-run on `fc5caadcf` rather than trusted from
the reviewer's report:

| Check | Result |
|---|---|
| `go build ./...` | clean, rc=0 |
| `go vet ./...` | clean, rc=0 |
| `gofmt -l` on the 7 diff files | clean, 0 files listed |

Diff-owned tests, run with real podman/Dolt containers (`-tags=cgo`,
`DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock`,
`TESTCONTAINERS_RYUK_DISABLED=true`), matching the methodology established
across all prior rounds of this feature:

| Test | Result | Duration |
|---|---|---|
| `TestOrderedIssueLines_PreservesInsertionOrderAndReplacesInPlace` | PASS | 0.00s |
| `TestLoadExistingIssueLines_ParsesIssuesSkipsMemories` | PASS | 0.00s |
| `TestLoadExistingIssueLines_MissingFileReturnsEmpty` | PASS | 0.00s |
| `TestChangedIssueIDs_DetectsUpsertsAndRemovals` | PASS | 0.53s |
| `TestTryIncrementalExport_PatchesChangedIssuesAndDropsRemoved` | PASS | 0.48s |
| `TestTryIncrementalExport_DropsIssueWhenFlippedToTemplate` | PASS | 0.34s |
| `TestTryIncrementalExport_FallsBackWhenFileMissing` | PASS | 0.33s |
| `TestNewTestStoreWithReadTimeout_AppliesConfiguredTimeout` (new, round 4) | PASS | 0.19s |
| `TestTryIncrementalExport_ThresholdExceededFallsBack` — run 1/3 | PASS | 43.95s |
| `TestTryIncrementalExport_ThresholdExceededFallsBack` — run 2/3 | PASS | 45.45s |
| `TestTryIncrementalExport_ThresholdExceededFallsBack` — run 3/3 | PASS | 43.86s |

8/8 diff-owned tests green. `TestTryIncrementalExport_ThresholdExceededFallsBack`
— the exact test that FAILed 2/3 in be-uoat's round-2 gate under the 10s
default timeout — was independently re-run 3 times against a real container
specifically because of that history. All 3 passed, tight and consistent
(43.9–45.5s), nowhere near either the old 10s collision point or the new 5m
ceiling. No failure occurred, so `vmstat` sampling was not needed. Combined
with round-3 review's independent 3/3 (413.24s/257.68s/316.48s), that is 6/6
real-container passes post-fix with zero failures — the
`bulkSeedPoolReadTimeout=5m` fix holds under repeated, independent
real-container verification. be-uoat's criterion-3 FAIL is resolved.

### Policy/lint lane (criterion 3b)

`make ci-pr-policy` → FAIL (rc=2), root cause: `.githooks/commit-msg` is
missing `BEGIN/END BEADS INTEGRATION` markers, tripping the version-
consistency sub-step. Independently verified, not taken on trust:

```
git diff --name-only origin/main...fc5caadcf -- .githooks/commit-msg   # empty — untouched by this diff
git diff origin/main -- .githooks/commit-msg                            # empty — byte-identical to origin/main
grep -n "BEADS INTEGRATION" .githooks/commit-msg                        # no hits — gap is real
```

`.githooks/commit-msg` is not one of the 7 diff files and is byte-identical
to `origin/main`, so this is a pre-existing gap on baseline, not something
this diff introduces or could fix within its own scope. `bd search` for
"githooks commit-msg" and "ci-pr-policy" returned no existing tracking bead.
Same disposition class as the `cmd/bd` / `internal/remotecache` failures
triaged non-blocking in be-uoat's gate: zero file overlap with the diff,
independently root-caused rather than waved through. Filing a small P3
tracking bead for this gap separately (not blocking this deploy).

## Findings from reviews (no action required)

Zero HIGH or MEDIUM findings across all 4 rounds. One informational,
non-blocking, carried-forward item from round 1/2 — `isSafeCommitRef`
(`internal/storage/dolt/versioned.go:244`) is correct but still lacks a
dedicated direct unit test. Round 3's REQUEST CHANGES (test-logic defect in
the new plumbing-proof test, not a HIGH finding) is resolved in round 4 —
see "Scope" above.

## Verdict

**PASS** — all 7 criteria pass (3b passes with a documented, independently-
verified pre-existing-and-unrelated exception; the ancestry-scope check's
single stray-commit flag is a documented, independently-verified false
positive specific to this extraction-workflow shape). Cutting isolated
deploy branch `deploy/be-y1jo-gate` from `fc5caadcf63371f6b992f32d23ef4d8c2a71e5ee`,
pushing to `headfork`, and opening a PR against `gastownhall/beads:main`.

**gastownhall/beads merge-authority carve-out:** this is a contributor-only
repository (`origin` push disabled; upstream is fetch-only). Per deployer
protocol, the job ends at the open PR — no merge-request routed to
mayor/mpr, no deploy-clearance status posted. Merge belongs to upstream
maintainers.
