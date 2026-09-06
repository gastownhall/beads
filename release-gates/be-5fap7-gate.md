# Release gate — setupTwoProjectStores shares one testTimeout across two cold store opens (be-gvnsq / be-5fap7)

- **Builder bead (CLOSED):** be-gvnsq — `setupTwoProjectStores` opened store A
  and store B under one shared `context.WithTimeout`, so store A's startup
  could consume store B's entire budget under load (100% of prior failures
  landed on store B, per be-1nl2h's investigation). Fix gives each store its
  own fresh `testTimeout`-bounded context via a new `storeOpenContext()`
  helper.
- **Deploy bead:** be-y0fsc
- **Review bead:** be-5fap7 — verdict **PASS**, recorded on commit
  `f23a315631db7f6d36f82b4cbafeb280c025fea1`
- **Commits:** `c2e896373b53ac5e5116c1daef1468735e429735` (labelled "TDD red"
  — **this label was wrong**; the pre-fix failure was a *compile* error because
  `storeOpenContext` did not yet exist, not a behavioural red. See the review
  addendum below.) then
  `f23a315631db7f6d36f82b4cbafeb280c025fea1` (TDD green — fix), 1 file over
  `origin/main` (base `c0d8da42de5fd15c95adac85e342ba4a121da0fb`)
- **Branch:** `builder/be-gvnsq` → deploy branch cut fresh as
  `deploy/be-5fap7-gate` from the exact reviewed commit SHA
- **Evaluated:** 2026-09-03 by beads/deployer

## Scope

`internal/storage/dolt/cross_project_test.go` — the **only** file this diff
touches (confirmed via `git diff c0d8da42d..f23a31563 --name-only`, 1 file,
+71/-7). Refactors `setupTwoProjectStores` so store A and store B each get an
independent, freshly-budgeted context (`storeOpenContext()`, new helper);
store A's context is cancelled before store B's open begins. Added diff-owned unit test
`TestStoreOpenContext_FreshBudgetPerCall` (pure, `time.Sleep`-based, ~0.5s,
does not call `New()` or touch a real Dolt store) — **that test has since been
deleted as vacuous; see the addendum.** No production code changed — this is
test-infrastructure only.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|---|---|---|
| 1 | Review PASS present | **PASS** | be-5fap7: `status: closed`, close reason `pass`, `verdict: pass`. Its `deploy_bead: be-y0fsc` / `deploy_commit` match this gate's bead and SHA exactly. |
| 2 | Acceptance criteria met | **PASS** | Reviewer independently walked all 5 be-gvnsq Done-when items against the diff itself (context lifecycle/cancellation, no escape of the helper-local context, gofmt/vet clean, load behavior via 3 independently-arrived-at convergent measurements, `testTimeout` value left untouched / scope discipline vs. the companion PR #5999). Each item backed by specific inspection, not asserted. |
| 3 | Tests pass | **PASS**, with attributed pre-existing failures | See "Test evidence" and "Pre-existing-failure attribution" below. |
| 3a | Pre-existing-failure attribution | Satisfied | 3 independent, predating root conditions cover every non-diff-owned failure observed. See below. |
| 3b | Policy lane (`make ci-pr-policy`) | **PASS**, attributed | Failed on `.githooks/commit-msg` BEGIN/END BEADS INTEGRATION marker check only — see attribution below. |
| 3c | CI-config lane | **n/a** | Diff contains no CI-config changes (no `.github/workflows/**`, no CI job/matrix/timeout/required-check edits). |
| 4 | No unresolved HIGH findings | **PASS** | be-5fap7: `style_findings: none`, `security_findings: none` (full OWASP walk, all N/A — diff is pure `context.WithTimeout` budgeting logic, no prod code, no new deps), `spec_findings: none` (blocker/major/minor). |
| 5 | Clean working tree | **PASS** | `git status --short` on `deploy/be-5fap7-gate` at this SHA: clean, no output. |
| 6 | Clean divergence from `origin/main` | **PASS** | `git rev-list --left-right --count origin/main...HEAD` → `0` behind, `2` ahead (the red+green pair). No rebase needed. |

## Test evidence

**Command:** `make test` (`TEST_COVER=1 ./scripts/test.sh`, full-suite —
`test_cmd_scope: full-suite`). `TIMEOUT=25m`, `GO_TEST_PKG_PARALLEL=4`,
`GO_TEST_PARALLEL=4` (repo defaults). Run on `deploy/be-5fap7-gate` at
`f23a315631db7f6d36f82b4cbafeb280c025fea1`, `TMPDIR`/`GOTMPDIR=~/.gotmp` per
this host's disk-quota policy. Duration: ~2000s wall.

| Metric | Count |
|---|---|
| Packages `ok` | 90 |
| Packages `FAIL` | 7 |
| Packages `[no test files]` | 1 |
| Individual `--- FAIL` lines (whole suite) | 62 |

**Diff-owned test (`TestStoreOpenContext_FreshBudgetPerCall`):** the primary
`make test` run used no `-v`, so a passing test prints nothing — its
PASS/FAIL could not be read by name from that log alone (zero mentions,
confirmed via `grep`). Per test-evidence-integrity's by-name resolution
requirement, independently re-ran isolated and verbose:

```
go test -tags gms_pure_go -run '^TestStoreOpenContext_FreshBudgetPerCall$' \
    -v ./internal/storage/dolt/... -timeout 5m
```

```
--- PASS: TestStoreOpenContext_FreshBudgetPerCall (0.50s)
ok  	github.com/steveyegge/beads/internal/storage/dolt	11.360s
```

**Result: PASS**, independently confirmed. (Run queued ~7 min behind this
host's shared go-shim build semaphore — `go-shim: 4/4 build slots busy` — a
host-load artifact of the run itself, not of the test.)

### The 7 FAIL packages, by root condition

All 62 individual `--- FAIL` lines plus `internal/storage/dolt`'s own
package-level timeout panic (0 individual `--- FAIL` lines — a hang, not a
per-test failure) are accounted for below. **None are diff-owned**; the diff
touches exactly one file, `internal/storage/dolt/cross_project_test.go`,
which is a `_test.go` file — not importable by any other package by Go's own
compilation model, and not referenced by `internal/storage/dolt/dolt_test.go`
either (confirmed via `grep` — zero hits for `setupTwoProjectStores` or
`storeOpenContext`, the diff's only two touched symbols, in that file).

**Condition A — host-wide Dolt/build contention.** Tracked: **be-52t59**
(open, `gate-tracker`, filed same-day, already used on be-34u9a for a
structurally identical pattern). Live corroboration this run: 28-47
concurrent `go test`/`go build`/`dolt sql-server` processes observed via
`ps aux` throughout the run, from multiple unrelated agent accounts/worktrees
on this shared host; the go-shim's own machine-wide 4-slot build semaphore
was observed saturated live (`4/4 build slots busy`) during this gate's own
independent test-verification run.

- `internal/storage/dolt` (package FAIL, 1509.120s): `TestNewDoltStore`
  (running 1m13s) → `panic: test timed out after 25m0s`. Baseline for this
  suite under `-p 4` is ~870s; this run took ~1.7x that. Also the review
  bead's own pre-registered `known_pre_existing_failure` for this package
  (`TestCrossProject_*` / "context deadline exceeded" under host load,
  tracking **be-696w**, filed 2026-08-19) — not directly triggered this run
  (`TestCrossProject_*` ran and passed silently, zero FAIL lines for any of
  them), but corroborates that this package is independently known to be
  load-sensitive, predating and unrelated to this diff.
- `cmd/bd` (package FAIL, 1506.689s): `TestInitDoltMetadata` (running
  12m22s) → the same 25m timeout panic; 52 further `--- FAIL` lines earlier
  in the same run (see Condition B for the config/repo-root-flavored subset
  of these). The Dolt/server-flavored subset — e.g.
  `TestDoltRemoteAddPersistsSyncRemoteToSharedWorktreeConfig`,
  `TestInitForceRefusesWhenRemoteHasDoltData`,
  `TestInitFromJSONLRefusesWhenRemoteHasDoltData`,
  `TestInit_WithBEADS_DIR_DoltBackend` — match be-52t59's own already-cited
  cmd/bd test list by name.
- `cmd/bd/doctor` (package FAIL, 44.218s): all 4 failures —
  `TestRunDoltHealthChecks_DoltBackendNoServer`,
  `TestCheckFreshClone_ServerModeUnreachable`,
  `TestCheckRepoFingerprint_UsesTargetRepoOutsideCWD` (9.41s),
  `TestCheckTestPollution_NoTestIssues_EmptyDB` — every one of these 4 is
  explicitly named in be-52t59's own prior investigation of this exact
  package under this exact condition.
- `internal/tracker` (package FAIL, 71.001s):
  `TestEnginePullWithIssueIDsSelectiveByBeadID` (10.04s) —
  `StartTestBranch: DOLT_BRANCH(...) failed: invalid connection`. A dropped
  Dolt server connection under load; same mechanism family as the above
  (not previously named in be-52t59, but the identical symptom class — a
  live Dolt connection failing under this run's confirmed 28-47-process
  contention — extends it directly).

**Condition B — ambient `~/.beads` leaks into tests that assume a clean
workspace.** Tracked: **be-9ogs6** (open, predates this run — first
attributed during review of be-6iglh, reproduced 2026-08-30 via a controlled
A/B: diff tip vs. a fresh scratch worktree pinned to `origin/main` with
*zero* code differences — 62 byte-for-byte-identical failing test names on
both sides). Root cause: beads' own repo-root/config walk-up logic finds and
uses the real ambient `~/.beads` / `~/jim-claude/.beads` directories on this
host instead of staying inside each test's isolated temp sandbox — proven
independent of any diff content.

- `internal/beads` (0.960s):
  `TestFindAllDatabases_Unit/no_databases_returns_empty_slice` — named
  verbatim in be-9ogs6.
- `internal/config` (0.292s):
  `TestSetYamlConfig_WorktreeFallbackUsesMainRepoConfig`,
  `TestFindConfigYAMLPath_WorktreeFallbackUsesMainRepoConfig`,
  `TestFindProjectBeadsDir_NonGitTreeWithoutConfig` — same mechanism
  (worktree/repo-root config walk-up resolving to the real ambient
  `/home/jaword/jim-claude/.beads`).
- `internal/formula` (0.117s):
  `TestDefaultSearchPaths_FallsBackToCwdFormulaDirWithoutBeadsProject` —
  named verbatim in be-9ogs6 (resolves to real ambient
  `/home/jaword/.beads/formulas`).
- `cmd/bd`'s remaining config/repo-root-flavored failures (the balance of
  its 52) — e.g. `TestCollectMetadataEntries`, `TestCollectViperEntries`,
  `TestResolvedConfigRepoRoot`, `TestFindBeadsRepoRoot_WorktreeFallback`,
  `TestIssueIDCompletion_UsesWorktreeFallbackWhenStoreNil`,
  `TestBackupDir_NoWorkspaceReturnsActiveWorkspaceError` (this last one also
  named verbatim in be-9ogs6) — same ambient-walk-up mechanism, same
  package family be-9ogs6 already documents cmd/bd as part of.

Note: be-9ogs6 carries label `source:actual-reviewer`, not `gate-tracker`.
Its content substantively satisfies clause 2 regardless (predates this run,
names these exact tests, proven diff-independent via a controlled zero-diff
reproduction) — flagged here for a human reviewer rather than silently
resolved, since it is already known to have been cited successfully once
before (be-6iglh).

**Why the mechanism proof holds regardless of the A/B split above:** every
one of the 62 `--- FAIL` lines and the one package-level timeout panic sits
in a package this diff cannot structurally reach (`cmd/bd`, `cmd/bd/doctor`,
`internal/beads`, `internal/config`, `internal/formula`, `internal/tracker`),
or — for `internal/storage/dolt` itself — in a file with zero references to
either symbol the diff touches. The A/B grouping above is offered for
readability and correct tracker citation, not because causation hinges on
getting every single one of the 62 sorted into the right bucket.

**Condition C — `.githooks/commit-msg` BEADS INTEGRATION marker false
positive (criterion 3b).** Tracked: **be-a0dxu** (open, `gate-tracker`,
predates this run). `make ci-pr-policy`'s version-consistency check expects
`BEGIN/END BEADS INTEGRATION` markers inside `.githooks/commit-msg`, but
that file is a git-ignored, per-session local shim (not a tracked repo
file) — the check fails identically regardless of diff content. Matches a
standing personal memory independently.

## Findings from review (no action required)

From be-5fap7: no HIGH or MEDIUM findings — zero blockers/majors/minors
across style, security, and spec findings, consistent with a single-file,
test-infrastructure-only diff.

## Verdict

**PASS.** All 6 criteria (with 3a/3b/3c) clear. Every observed test failure
in the full-suite run is confined to packages/files this diff cannot
structurally affect, and is covered by predating tracked conditions
(be-52t59, be-9ogs6, be-696w, be-a0dxu). The diff's own test
(`TestStoreOpenContext_FreshBudgetPerCall`) independently re-verified PASS
in isolation. Proceeding to push `deploy/be-5fap7-gate` and open a PR against
`gastownhall/beads:main`. Per this rig's contributor-only carve-out for
`gastownhall/beads` (established precedent: be-34u9a), the deployer's job
ends at the verified opened PR — no merge, no merge-request routed to
mayor/mpr, a for-visibility-only mail to mayor instead.

---

## Addendum — review response (2026-09-06, PR #6256)

`bee-ghosttrack` filed CHANGES_REQUESTED at `61d1d51e7` on 2026-09-04 with two blocking findings. Both are accepted; both were correct.

### Finding 1 — the test did not exercise the property it claimed

Upheld in full. `TestStoreOpenContext_FreshBudgetPerCall` asserted only that two `context.WithTimeout(context.Background(), testTimeout)` calls return different deadlines — a property of the standard library. It never named `setupTwoProjectStores`, `New` or `DoltStore`, so no change to `setupTwoProjectStores` — a complete revert included — could have turned it red.

The reviewer's characterisation of this gate record is also correct and is corrected above: the "TDD red" at `c2e896373` was a **compile** failure (`storeOpenContext` undefined before the fix commit), not a behavioural one. A compile error is not evidence that a test guards a behaviour.

The test has been deleted rather than replaced. The property — "a cold open must not inherit a budget already spent by a previous open" — is not observable from a unit test without an injectable clock or injectable load, and writing a second test that *looks* like it checks this would repeat the original error. What replaces it is structural: every two-open site now routes through a single helper, so the shared-deadline shape has one place to regress rather than fourteen.

### Finding 2 — one of five sites fixed

Upheld, and the count was higher than reported. The review listed four unfixed sibling sites; enumerating every `New(ctx, …)` in the package found **seven**, all with two cold opens under one `testTimeout`:

| Site | Test |
|---|---|
| `cross_project_test.go:307,320` | `TestCrossProject_PortCollision_SameDatabase` *(reported)* |
| `cross_project_test.go:812,829` | `…_IdentityCheck_ExistingDatabase_ForeignRejected` *(reported)* |
| `cross_project_test.go:856,866` | `…_IdentityCheck_ExistingDatabase_MatchingSucceeds` *(reported)* |
| `create_guard_test.go:273,291` | `TestCreateGuard_ExistingDB_WithData` *(reported)* |
| `cross_project_test.go:903,914` | `…_IdentityCheck_SoftSkip_NoLocalMetadata` |
| `cross_project_test.go:938,945` | `…_IdentityCheck_SoftSkip_EmptyDBProjectID` |
| `cross_project_test.go:976,996` | `…_IdentityCheck_Gateway_SkipsVerification` |

Extended rather than deferred, since the change is mechanical and the review's own challenge — "if they never flaked, the diagnosis needs more support than the gate gives it" — is answered better by removing the shape everywhere than by arguing about it.

### Findings 3 and 4 — duplicate helper, unconditional sleep

Both resolved by the same deletion. `storeOpenContext()` was byte-identical to `testContext(t)` (`dolt_test.go:59-62`) minus `t.Helper()`, and the 500 ms `time.Sleep` existed only to exercise it. Both are gone.

The one helper now added, `openStoreWithOwnBudget(t, cfg)` (`dolt_test.go`), is not a re-run of finding 3: it does not duplicate `testContext`, it *composes* it with the `New` call and the cancel, which is the invariant all fourteen call sites need. Cancelling the open context when the open returns is safe for exactly the reason the review already established under "Checked and not a finding" — the context bounds the open, not the returned store.

`testTimeout`'s own doc comment said "some tests set up two stores under one context"; that is no longer true and has been corrected.

### Net effect

The diff is now **smaller** than the version reviewed: `-90/+44` across three files, against `+78/-7` in one before.

### Verification

Real `dolthub/dolt-sql-server:2.2.0` container, `TESTCONTAINERS_RYUK_DISABLED=true`, ambient `BEADS_DOLT_SERVER_PORT` unset so the suite cannot silently target the shared city server instead of the container:

```
go test ./internal/storage/dolt/ -run TestCrossProject -count=1 -v
  11/11 PASS, 0 FAIL, 0 SKIP   (193.4s)
go test ./internal/storage/dolt/ -run TestCreateGuard -count=1 -v
  9/9  PASS, 0 FAIL, 0 SKIP    (35.3s)
```

Zero SKIPs is load-bearing here: when the container cannot start, these suites skip rather than fail, and a skipped run is indistinguishable from a passing one in the summary line.

`gofmt -l internal/storage/dolt/` clean; `go vet ./internal/storage/dolt/...` clean.
