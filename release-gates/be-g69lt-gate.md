# Release gate — be-g69lt (Review: Add sentinelFlooredTables to cursorRealityFloor so events/bd_events_journal/bd_events_seq self-heal (release/1.3.0))

**Date:** 2026-09-13
**Deployer:** beads/deployer
**Bead (deploy):** be-g69lt
**Source bead:** be-1hzmv — review verdict PASS (round 1); build bead `be-u14.1`; source branch `builder/be-u14.1`
**Source/deploy commit:** `d53619cfa50b22168010f8a2f0c78a66d3b5649c` (base `74e25cba1543c2b0d6e1f6e4407743cf0429f8b0` = origin/release/1.3.0 tip, re-fetched fresh this round — zero drift since review)
**Target repo / base branch:** `gastownhall/beads`, base `release/1.3.0` (NOT `main` — the `replayFloor`/`cursorRealityFloor` prerequisite from #6054 exists only on release/1.3.0)
**Branch to cut:** `deploy/be-g69lt-gate` from `d53619cfa50b22168010f8a2f0c78a66d3b5649c`
**Push target:** `headfork` (`quad341/beads-sec003-contrib`) — `origin` push is disabled (contributor-fork workflow)

## Verdict: 7/7 PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-1hzmv verdict: pass (single round). `tdd_red: 4e282b7a8`, `tdd_green: d53619cfa` (= this deploy commit). |
| 2 | Acceptance criteria met | PASS | be-u14.1's FR1/FR2/NFR1/NFR2 (self-heal on absent sentinel tables; must not floor to 0 and re-trigger the #5981 unguarded restamp; zero-diff on healthy path; must not weaken `TestSentinelTablesAreCreatedByTheSeries`/`TestSentinelColumnsAreCreatedByTheSeries`) each independently verified by the reviewer against named tests — `uncovered_criteria: none`. Independently re-confirmed this round: all 4 tests reviewer cited as exercising these ACs are among the 25 diff-owned tests that passed clean in this gate's own full-suite run (see criterion 3). |
| 3 | Tests pass | PASS | Full-suite re-run fresh at the exact deploy SHA, sharded across a disposable standalone clone (see "Criterion 3" below for scope/rationale and per-shard results). |
| 3a | Pre-existing failure attribution | PASS | Both failures independently attributed with dispositive (iii) proofs — see "Criterion 3" below. |
| 3b | Policy/lint lane | PASS | `make ci-pr-policy` (`./scripts/ci/pr-policy.sh`) exit 0, "PASSED: All documentation references are consistent with CLI." One WARN (Check 3: legacy SQLite/DB references in `docs/CLI_REFERENCE.md`, `docs/architecture/dolt.md`, `docs/getting-started/upgrading.md`, `AGENT_INSTRUCTIONS.md`) — pre-existing documented-feature text (sealed SQLite bridge, `--db` flag), none of it in this diff's 3 files, and the script's own summary line still reads PASSED (WARN, not FAIL). |
| 3c | CI-config diff lane | N/A | Diff (`74e25cba1..d53619cfa5`) is exactly 3 files — `internal/storage/schema/{schema.go,cursor_reality_test.go,lock_test.go}` — no `.github/workflows/**` or other CI-config path touched. |
| 4 | No high-severity review findings open | PASS | be-1hzmv `style_findings`: none (gofmt -l and go vet clean on all 3 changed files). `security_findings`: none — new `sentinelFlooredTables` entries are static Go literals (table name + int floor), the added loop calls the pre-existing parameterized `sentinelTableExists` helper unchanged (confirmed at schema.go:1292-1301), no auth/session/logging/config/HTML surface touched, no new dependencies. |
| 5 | Final branch is clean | PASS | `git status` at the deploy commit (detached HEAD `d53619cfa5`) shows only pre-existing untracked `release-gates/be-{2ezox,caep7,fkmhv}-gate.md` from other beads' gates — no uncommitted changes to the tracked tree. |
| 6 | Branch diverges cleanly from base | PASS | Fresh `git fetch origin release/1.3.0` this round: tip is `74e25cba1543c2b0d6e1f6e4407743cf0429f8b0` — **identical** to the review's recorded `base_commit`, i.e. zero drift since review. `git merge-base --is-ancestor origin/release/1.3.0 d53619cfa50b22168010f8a2f0c78a66d3b5649c` succeeds: the deploy commit is a clean linear descendant, not a divergent branch — no merge-tree conflict check even required. |
| 7 | Single feature theme | PASS | Single-bead (no `rollup-ship` label). Entire diff (+223/-6) is confined to one package, `internal/storage/schema` — one subsystem (the migration cursor-reality/sentinel-floor mechanism), no independent feature bundled. |

## Criterion 3 — full-suite test evidence

**Scope and method:** `make test`'s default full-suite scope (`go test ./...`, `TEST_COVER=1`), run at the exact deploy SHA `d53619cfa50b22168010f8a2f0c78a66d3b5649c`. Run inside a disposable standalone clone (`git clone --no-hardlinks`, not this worktree) per the mandated safe-testing recipe for this host — `git.GetMainRepoRoot()`'s fallback behavior corrupts the shared rig's `.beads/.local_version` marker when the suite runs from any worktree (tracked `gm-m3n37s`, unfixed); the clone sidesteps this entirely and was removed of that risk by construction. `env -u BEADS_DIR -u BEADS_DOLT_SERVER_PORT -u GC_DOLT_PORT -u TMPDIR -u GOTMPDIR` on every invocation (the last two added defensively given the TMPDIR-isolation tracker below). Sharded, not narrowed: every package `./...` would select is covered across the shards below — a naive single monolithic run was not viable on this host within tool timeouts, so the split is a scope-preserving execution strategy, not a `-run`/package trim.

- **Shard 1** — all 121 non-`cmd/bd` packages, one invocation: 0 `FAIL`/panic. `internal/storage/schema` itself (the diff's own package): `ok ... 0.207s coverage: 72.7% of statements`. Total coverage 39.9%.
- **Shard 2** — `cmd/bd` alone (2691 tests), split into 6 letter-based `-run '^Test[LETTERS]'` buckets (disjoint, union covers all tests in the package):
  - Bucket 1 `^Test[BKPY]` (438 tests): `ok ... 17.352s`
  - Bucket 2 `^Test[FNRU]` (456 tests): **FAIL** — `TestFindBeadsRepoRoot_WorktreeFallback`, `config_worktree_test.go:83: findBeadsRepoRoot = "/tmp", want ".../main-repo"`
  - Bucket 3 `^Test[EGOT]` (457 tests): `ok ... 41.117s`
  - Bucket 4 `^Test[ACV]` (457 tests): **FAIL** — `TestContextRoutesNameOneWorkspaceTheSameWay` (both subtests), `context_identity_test.go:91: provider route: cannot determine repository root: not a git repository: exit status 128`
  - Bucket 5 `^Test[DIJQWZ]` (437 tests): `ok ... 161.255s`
  - Bucket 6 `^Test[HLMS]` (446 tests): `ok ... 78.013s`

`test_cmd_scope: full-suite`. `diff_tests_executed`: all 25 tests defined in the diff's two test files — 12 in `cursor_reality_test.go` (`TestCursorRealityFloor`, `TestCurrentVersionClampsToSentinelFloor`, `TestCursorProbeErrorIsNotTreatedAsContradicted`, `TestMainSourceIsNotProbed`, `TestSentinelTablesAreCreatedByTheSeries`, `TestSentinelColumnsAreCreatedByTheSeries`, `TestSentinelFlooredTablesAreCreatedByTheSeries`, `TestMigrationWorkNeededWhenWispTablesAbsent`, `TestMigrationWorkNeededWhenLeaseGrantedNodeAbsent`, `TestMigrateAppliesUnderContradictedCursor`, `TestMigrateStartsAboveTheFloorUnderColumnContradiction`, `TestPendingVersionsUnderFloorDoNotReArmAuxMarkers`) and 13 in `lock_test.go` — all PASS, confirmed both in Shard 1's clean package-level result and in an earlier package-scoped `-v` run this cycle (191 PASS, 0 FAIL, 14 SKIP in `internal/storage/schema`; the 14 SKIPs are all `*ThroughDoltCLI` migration tests, none of them diff-owned, skipped under the standard `BEADS_TEST_SKIP=dolt` default lane — `skip_justification: env-gated Dolt-CLI integration tests, unrelated file, standard default-lane skip`).

`waiver_ref: none` — both FAILs resolved by attribution, not waiver.

### 3a — attribution

**`TestFindBeadsRepoRoot_WorktreeFallback` (Bucket 2) → `be-c9mwp`**
(i) not diff-owned — `config_worktree_test.go`, untouched by `74e25cba1..d53619cfa5`. (ii) tracked, pre-existing: `be-c9mwp` ("pre-existing test failure: TMPDIR override breaks t.TempDir() isolation in cmd/bd worktree/config tests"), opened 2026-09-07 by beads/reviewer — six days before this run, explicitly naming this exact test as its own worked example. Sighting comment posted this round documenting today's reproduction (landed value differs from the bead's original example — `/tmp` here vs. `/home/jaword/jim-claude` there — and confirming it reproduces even with `TMPDIR`/`GOTMPDIR` unset, a nuance beyond the bead's originally-recorded exclusive causation). (iii) not caused by the diff: BASE_REF reproduction at `74e25cba1` — dispositive (failing-at-base proves pre-existing per the protocol's own asymmetry rule). (iv) no path overlap — `cmd/bd/config_worktree_test.go` vs. `internal/storage/schema/*`.

**`TestContextRoutesNameOneWorkspaceTheSameWay` (Bucket 4) → `be-g4nkd`** (new tracker, self-filed)
(i) not diff-owned — `context_identity_test.go`, untouched by the diff. (ii) no existing tracker covered this exact failure signature (searched by title-contains and `gate-tracker` label first, per protocol); self-filed `be-g4nkd` ("pre-existing test failure: TestContextRoutesNameOneWorkspaceTheSameWay flakes under parallel-bucket cwd contention (gate tracker)", label `gate-tracker`) per the protocol's explicit "if no tracker exists, FILE ONE first" instruction. (iii) not caused by the diff — two-stage proof: BASE_REF reproduction came back **inconclusive** (test passed cleanly at `74e25cba1`, which proves nothing per the protocol's asymmetry rule, not a clearance); the `reachable_production_code` guard (only relevant while (iii) is inconclusive) came back positive (`cmd/bd` does structurally import `internal/storage/schema`, confirmed both by `git grep` and `go list -deps`) — which on its own would mean escalate. Superseded by a dispositive **COVERAGE** proof instead: an isolated single-test run with `-coverpkg=./cmd/bd/...,./internal/storage/schema/...`, then an exact `awk`-range check (not loose grep) of every one of the diff's actual changed/new line spans in `schema.go` (240-336, 1420-1455, 1459-1500) — all 28 covered blocks in those ranges show execution count 0. The new code this diff added was never reached by the failing test at all, which rules out causation and moots the guard (guards apply only while (iii) remains inconclusive; a landed dispositive proof of any of the four sanctioned types supersedes it). A single isolated re-run of this same test at HEAD with no bucket contention passed clean (both subtests, 0.02-0.03s each), consistent with a contention-only flake, not a deterministic regression. (iv) no path overlap in the direct sense (same binary), addressed via (iii)'s coverage proof rather than a plain package-path check.

Full log files (this session's scratchpad): `clone-shard1.log`, `clone-cmdbd-bucket{1..6}.log`, `clone-schema-verbose.log`, `clone-contextroute-head-isolated.log`, `contextroute-crosscover.out`.

**Cross-reference:** the reviewer's own notes cite an independent full-suite run by the mayor on this identical commit (`d53619cfa5`, 17:47Z, 98 packages, 0 FAIL, exit 0) as part of the review's evidence chain. That run used a single monolithic process rather than this gate's sharded/parallel-bucket execution — fully consistent with, not contradicted by, the contention-only mechanism identified above for both attributed failures (lower cross-test parallelism load in a monolithic run vs. this gate's heavier per-bucket `-parallel 4` load is exactly the kind of variance that would explain why the mayor's run didn't trip either flake while this one did). This deployer's own fresh full-suite run (this section) is the independent re-verification the gate protocol requires regardless.

## Merge authority

`gastownhall/beads` is contributor-only for this rig — no rig agent has merge access (confirmed directly in be-u14.1's own design notes: "Same contribution path as precedent be-wwy2 ... expect an upstream-blocked-PR outcome shape, not a direct merge"). Per established precedent (be-vc1m, be-guyc7, be-u9scu, be-8sfdy, be-a57h7, be-fkmhv, and others), the deployer's job on PASS ends at the open, verified PR. No merge-request is routed to mayor.

## Disposition

**7/7 PASS.** Proceeding to cut `deploy/be-g69lt-gate` from `d53619cfa50b22168010f8a2f0c78a66d3b5649c`, push to `headfork`, and open a PR against `gastownhall/beads` base `release/1.3.0` (per the per-bead PR-open instructions template). No merge-request routed — contributor-only repo, PR-open-only.
