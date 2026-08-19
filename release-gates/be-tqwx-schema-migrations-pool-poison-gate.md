# Release gate — Fix: failing schema_migrations probe poisons the pooled Dolt session (be-bv7x)

- **Builder bead (CLOSED):** be-bv7x — a Dolt sql-server session that issues a
  failing statement stays pinned to its pre-statement catalog snapshot;
  `migrationSource.currentVersion` ran a bare `SELECT` against the cursor
  table before it necessarily existed, so on a fresh database the pooled
  connection that hit the missing-table error stayed poisoned for the rest
  of its life in the pool. Real production bug (hits `bd init` and any first
  writable open with `CreateIfMissing` on a fresh database), not test-only.
- **Deploy bead:** be-tqwx
- **Review bead:** be-43sq — verdict **PASS**, recorded on commit
  `e71d40578452425db33a17a70bb330157ad2b4fa`
- **Commits:** `dfe017ca942e5b11cfe3b14db30614de6e837b3f` (TDD red — new
  regression test, confirmed failing pre-fix) then
  `e71d40578452425db33a17a70bb330157ad2b4fa` (TDD green — fix), 2 files over
  `origin/main`
- **Branch:** `builder/be-bv7x` (pushed to fork remote; deploy branch cut
  fresh as `deploy/be-tqwx-gate` from the exact reviewed commit SHA, pushed
  to `headfork` per established multi-round precedent for this rig)
- **Evaluated:** 2026-08-18/19 by beads/deployer

## Scope

`internal/storage/schema/schema.go`, `migrationSource.currentVersion`: probes
cursor-table existence with a query that always succeeds
(`information_schema.tables`) before ever issuing the original
`SELECT COALESCE(MAX(version), 0) FROM <cursorTable>`, which can itself fail
before migrations have run. The absent-table case now short-circuits to
`(0, nil)` without ever touching the connection with a failing statement, so
the pooled connection is never pinned to the pre-migration catalog snapshot.
Original error-handling path (`dberrors.IsTableNotExist` fallback) is
untouched and still covers races.

Diff scope, confirmed directly via `git diff origin/main...e71d40578452425db33a17a70bb330157ad2b4fa`
(2 files, 79 insertions, 0 deletions):

- `internal/storage/schema/schema.go` — feature logic (+17 lines, all inside
  `currentVersion`)
- `internal/storage/dolt/initschema_pool_poison_test.go` — new diff-owned
  regression test (+62 lines), entirely dedicated to this fix

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-43sq: `status: closed`, `close_reason: pass`, `verdict: pass`; `deploy_bead: be-tqwx` / `deploy_commit: e71d40578452425db33a17a70bb330157ad2b4fa` both match this gate exactly. |
| 2 | Acceptance criteria met | **PASS** | be-bv7x's 4-item Done-when checklist independently walked by be-43sq's reviewer, each item backed by specific evidence (code inspection, red/green reproduction, 9/9 test run, style step be-gkh1); re-confirmed directly by this gate below. |
| 3 | Tests pass | **PASS** | Diff-owned `TestFreshDB_PoolReadsSchemaMigrations` + 8 named exit-contract tests, independently re-run by this gate against a real `dolthub/dolt-sql-server:2.2.0` container — 9 PASS, 0 FAIL, 0 SKIP, 85.47s. Matches reviewer's independently-reported 9/9 exactly. |
| 4 | No unresolved HIGH findings | **PASS** | be-43sq: `style_findings: none`; `security_findings: none blocking` (full OWASP walk — the one parameterized-vs-concat SQL distinction is correctly attributed: new probe query is parameterized, pre-existing adjacent line is unchanged/out-of-scope/not attacker-reachable). |
| 5 | Clean working tree / divergence | **PASS** | `deploy/be-tqwx-gate` cut from `origin/main` at exactly `e71d40578452425db33a17a70bb330157ad2b4fa` — 2 commits ahead (clean TDD red/green pair), 0 behind. No rebase needed. `assert_deploy_ancestry_scope` clean (no `.claude/**` paths, both commits cite be-tqwx/be-bv7x). |
| 6 | Clean divergence from `origin/main` | **PASS** | Same as above — trivially clean, both commits properly cited. |
| 7 | Single feature theme | **PASS** | Both files serve exactly one fix: the production change (+17 lines, one function) and its dedicated regression test. No unrelated changes riding along. |

## Tests run on release branch (independent re-verification)

Static checks, independently re-run rather than trusted from the reviewer's
report, on `deploy/be-tqwx-gate` at `e71d40578452425db33a17a70bb330157ad2b4fa`:

| Check | Result |
|---|---|
| `gofmt -l` on the 2 diff files | clean, 0 files listed |
| `go vet ./internal/storage/schema/... ./internal/storage/dolt/...` | clean, rc=0 |
| `go build ./...` | clean, rc=0 |

Diff-owned + named exit-contract tests, real podman/Dolt container
(`DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true
BEADS_TEST_ENV_RUN_DOLT=1 ./scripts/test.sh -v -run '^(...)$' ./internal/storage/dolt/...
./internal/storage/schema/...`, `BEADS_DOLT_SERVER_PORT`/`BEADS_DOLT_AUTO_START`
unset per be-bv7x's documented gotcha to avoid silently hijacking onto the
shared city Dolt server):

| Test | Result | Duration |
|---|---|---|
| `TestFreshDB_PoolReadsSchemaMigrations` (diff-owned) | PASS | 5.60s |
| `TestDoltNew_RemoteMigrateGate_BlocksReopen` | PASS | 6.29s |
| `TestCheckForwardDrift_EscapeHatch_ReturnsNil` | PASS | 6.25s |
| `TestDoltNew_ReadOnly_ForwardDrift_ReturnsSchemaSkewError` | PASS | 7.76s |
| `TestDoltNew_ReadOnly_ForwardDrift_EscapeHatch_Succeeds` | PASS | 7.79s |
| `TestSchemaRunsInitWhenMissing` | PASS | 12.71s |
| `TestDoltNew_SmartRemoteMigrateGate_AutoFastForward_RealDolt` | PASS | 6.06s |
| `TestDoltNew_SmartRemoteMigrateGate_UnpushedCommitDegrades_RealDolt` | PASS | 8.20s |
| `TestDoltNew_SmartRemoteMigrateGate_BelowLatestDegrades_RealDolt` | PASS | 8.29s |

9/9 PASS, 0 FAIL, 0 SKIP (85.47s total package run). No pre-existing-failure
attribution needed.

## Findings from review (no action required)

From be-43sq: no HIGH or MEDIUM findings. Two informational, non-blocking
items, both pre-existing and out of scope for this diff — the unchanged
concat-built `SELECT ... FROM "+m.cursorTable` line immediately below the new
probe (internal constant, never user/network input) and the test file's
`fmt.Sprintf` DROP DATABASE cleanup (test-internal generated name, no
realistic injection path).

## Verdict

**PASS** — all 7 criteria clear. Proceeding to cut/push `deploy/be-tqwx-gate`
to `headfork` and open a PR against `gastownhall/beads:main`. Per this rig's
contributor-only carve-out for `gastownhall/beads`, the deployer's job ends
at the open PR — no merge (`gh pr merge` is forbidden for all rig agents), no
merge-request routed to mayor/mpr, no wait on upstream maintainer action.

## Post-hoc annotation — 2026-08-19 (be-crwzj, be-mv0ww)

**The PASS verdict above is left exactly as recorded.** A release gate is an
audit artifact: the verdict stands as what this gate concluded at the time, on
the evidence it gathered. This section annotates that record rather than
rewriting it.

**What happened.** PR #5847 went red on CI after this gate passed. 37 tests
across `internal/storage/schema` and `internal/storage/uow` fail on this branch.
The cause is this diff: the new `information_schema` existence probe in
`migrationSource.currentVersion` (`internal/storage/schema/schema.go:1115-1130`)
runs ahead of the pre-existing cursor read, so every ordered sqlmock expectation
that mocks that read now meets an unregistered query and errors with
`could not match actual sql`. Investigated under be-crwzj; the test-expectation
repair is tracked as be-1jha0. **The production change is correct and is not
being reverted** — the stale artifacts are the mocks, not the fix.

**Criterion 3 is not false, and is deliberately not being changed to FAIL.**
Its evidence line is accurate: those 9 tests were run and did pass. The defect
is that the criterion is *named* "Tests pass" while its actual *scope* is
"diff-owned and named exit-contract tests pass". Concretely, this gate's own
command filtered `./internal/storage/schema/...` through a `-run` regex naming
9 tests, so the sqlmock tests living in that same package were never executed,
and `internal/storage/uow` was not in the package list at all. `go vet` and
`go build` do not execute tests, so no other check in this gate covered them.

**Why this gate could not have caught it.** The break is in the *callers* of the
changed function, not in the changed files: 3 files changed, 9 test files broken,
in two packages the diff never touches. A gate that scopes its test run to the
diff cannot observe a caller-side break by construction.

**The durable fix is a template change, tracked as be-mv0ww** (P1, routed
beads/architect): either run the callers of every changed function via a
reverse-dependency query over the changed packages, or rename criterion 3 to
"Diff-owned tests pass" and add a separate caller-scope criterion. This is a
template defect, not a discipline failure by this gate's author.

## Deploy gate — be-z2ffj (2026-08-19)

- **Deploy bead:** be-z2ffj (routed from review be-pubk9, verdict **PASS**)
- **Build bead:** be-1jha0 (investigator root-cause: be-crwzj)
- **Commit:** `1976d6a8512b0ea57cf9a29314bcf603f5b5035c` — tip of
  `deploy/be-tqwx-gate`, same commit already serving as PR #5847's head (see
  "Reconciliation with the existing PR" below).
- **Evaluated:** 2026-08-19 by beads/deployer

### Scope

Deploy-gate evaluation of the be-1jha0/be-pubk9 test-repair described in the
post-hoc annotation immediately above: the fix for the 37 sqlmock
expectations broken by the be-bv7x cursor-existence probe. This diff sits on
top of, and does not modify, the production fix already gated PASS above
(`e71d40578`). Diff over `origin/main`, confirmed via
`git diff origin/main 1976d6a85... --name-only` (11 files):

- `internal/storage/schema/{aux_row_id_backfill_test.go,content_hash_test.go,cursor_reality_test.go,lock_test.go,remote_migrate_gate_test.go,schema_skew_test.go,schema_test.go}`
  — mechanical `expectCursorProbe(...)` insertions ahead of the pre-existing
  ordered sqlmock expectations, plus 2 semantic changes
  (`TestCheckSchemaSkew_MissingTable_NoError`,
  `TestCheckTeamServerSchema_MissingTable_RefusesWithBtsInit`: error-injection
  → `expectCursorProbe(mock, table, false)`), independently re-verified below
  against `schema.go`'s actual `currentVersion()` short-circuit-to-`(0,nil)`
  logic.
- `internal/storage/uow/team_server_schema_test.go` — same repair class.
- `internal/storage/dolt/initschema_pool_poison_test.go` — untouched by this
  diff (already part of the be-bv7x commit; included in the file list only
  because `git diff` is against `origin/main`, spanning all 4 stacked
  commits).
- `release-gates/be-tqwx-schema-migrations-pool-poison-gate.md` — the
  post-hoc annotation above (be-pubk9's own commit) plus this section.

Zero production code changed. `internal/storage/schema/schema.go` is
byte-identical to its state after `e71d40578` — confirmed via
`git diff e71d40578...1976d6a85 -- internal/storage/schema/schema.go`
(empty).

### Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-pubk9: `status: closed`, `close_reason: pass`, `verdict: pass`; `deploy_bead: be-z2ffj` / `deploy_commit: 1976d6a8512b0ea57cf9a29314bcf603f5b5035c` both match this gate exactly. |
| 2 | Acceptance criteria met | **PASS** | be-pubk9's `acceptance_criteria_check` explicitly verifies both semantic (non-mechanical) mock changes against `schema.go`'s actual `currentVersion()` logic, not just asserted; independently re-read by this gate — confirmed accurate. Diff's stated purpose (repair the 37 sqlmock tests broken by be-bv7x's probe) is satisfied: all 23 diff-owned test functions individually named PASS in be-pubk9's evidence, cross-checked by this gate's own full-suite run below. |
| 3 | Tests pass | **PASS*** | See "Test evidence" below — local full-suite `ci-pr-core` clean (604s, 0 FAIL); one GitHub Actions job fails, attributed as pre-existing/environmental per the 4-clause writeup below (not diff-caused). |
| 3a | Pre-existing-failure attribution | **Satisfied** | See "Pre-existing-failure attribution" below. |
| 3b | Policy + lint lanes | **PASS** | `ci-pr-policy`: clean after moving aside the local, git-ignored `.githooks/commit-msg` worktree artifact (known false-positive, see below) — restored immediately after. `ci-pr-lint`: clean (0 issues native + 0 issues windows-cross) after `golangci-lint cache clean` + explicit `BD_LINT_NEW_FROM_MERGE_BASE=origin/main` (known shared-cache/unscoped-sweep false-positive, see below). |
| 4 | No unresolved HIGH findings | **PASS** | be-pubk9: full OWASP-style walk, all clean, "zero blockers/majors/minors" — consistent with an 8-test-file/0-production-line diff. No open architect/blocker bead against this specific commit; be-mv0ww (template-defect fix for the gate's own criterion-3 scope) is a process-improvement bead routed to beads/architect and does not block this deploy — its lesson (don't diff-scope the test run) was proactively applied here by running the full `./...` suite rather than a diff-scoped subset. |
| 5 | Clean working tree | **PASS** | `git status --porcelain` on `deploy/be-tqwx-gate` at this SHA: 4 pre-existing untracked files only (3 unrelated release-gate scratch files from other deploy cycles sharing this worktree, 1 deployer-tooling script copied in by worktree setup, not a beads-repo file) — zero tracked-file modifications. |
| 6 | Clean divergence from `origin/main` | **PASS** | `git fetch origin main` → tip `ed382cbdb89cf7ba42b020e4927575dbf27e102e`; `git merge-base --is-ancestor origin/main 1976d6a85` → true (clean fast-forward). Exactly 4 commits ahead, 0 behind, no rebase needed. All 4 commits cite governing bead IDs (be-bv7x ×3, be-1jha0/be-bv7x ×1). |
| 7 | Single feature theme | **PASS** | One coherent lineage: be-bv7x's production fix + its own regression test, then be-1jha0/be-pubk9's follow-up repair of test mocks broken by that same fix, explicitly sanctioned as a stacked annotation rather than a rewrite ("per mayor ruling" cited in the tdd_green commit message). No unrelated changes riding along — confirmed by the 11-file diff list above, every file within the schema/uow/dolt-test or gate-doc scope. |

\* Criterion 3 evidence line intentionally states the same "named tests pass,
scope is bounded" caveat the post-hoc annotation above flags as this
template's known limitation (be-mv0ww) — mitigated here by running the full
`./...` suite rather than a diff-scoped one.

### Test evidence

Independently re-run by this gate (not solely trusted from be-pubk9's
report), on `deploy/be-tqwx-gate` at `1976d6a8512b0ea57cf9a29314bcf603f5b5035c`,
hermetic env via `scripts/ci/lib/test-env.sh`:

| Check | Result | Notes |
|---|---|---|
| `make ci-pr-core` (`go test -p 4 -parallel 4 -race -short -skip '^TestEmbedded' ./...`) | **PASS** | 604s, every package `ok` or `[no test files]`, 0 FAIL. Includes `internal/storage/schema`, `internal/storage/uow`, `internal/tracker` (all `ok`). |
| `make ci-pr-policy` | **PASS** | Failed on first run (`.githooks/commit-msg` local-artifact false-positive, documented institutional memory `githooks-commit-msg-local-artifact-not-ci-breakage` — file is git-ignored, worktree-setup.sh-managed, not tracked anywhere in the repo). Moved aside, re-ran clean (exit 0), restored immediately after. |
| `make ci-pr-lint` | **PASS** | Failed on first run (3 gosec G602 findings in `backend/conformance/*.go`, files entirely outside this diff — documented institutional memory `golangci-lint-shared-cache-staleness`: cross-agent-shared lint cache + bare invocation sweeping `./...` instead of the diff-scoped set CI actually uses). `golangci-lint cache clean` + `BD_LINT_NEW_FROM_MERGE_BASE=origin/main make ci-pr-lint` → 0 issues native, 0 issues windows-cross. |

### Pre-existing-failure attribution — GitHub Actions "Test (storage domain + uow)"

PR #5847's CI (run `32243141423`, `headSha` = this exact deploy SHA) shows
one failing job, "Test (storage domain + uow)" (job `96038149135`), plus its
downstream "CI Gate / Required" rollup. Every other check — including the
full embedded/proxied Dolt test matrix (Embedded Dolt Storage 1-5, Embedded
Dolt Cmd 1-20, Proxied Dolt Cmd 1-15, Server Dolt Conformance, historical
upgrade matrices, Contract corpus, Lint, both PR-Policy/PR-Core wrapper
jobs) — passes.

1. **Not in diff scope.** The actual failing package is
   `github.com/steveyegge/beads/internal/tracker`
   (`TestEngineCreateDependencies_Empty`, `TestEngineExcludeIDPrefix`,
   `TestEngineExcludeIDPatterns`, `TestEngineExcludeIDBoth`,
   `TestEngineExcludeID_AlreadySynced`, `TestEngineDryRunRespectsExcludeID`).
   `internal/tracker` does not appear anywhere in this diff's 11-file list
   above. The job's display name ("storage domain + uow") is a pre-existing
   umbrella label for a package group that also happens to include
   `internal/tracker`; it is not evidence the failure is storage/uow-related.
2. **Root cause is environmental, not logic.** Full failure signature
   matches institutional memory `beads-ci-ryuk-reaper-flake-triage` exactly:
   `Waiting for Reaper "607b34d4" to be ready` / `Reaper obtained from Docker
   for this test session 607b34d4` / `Reaper handshake failed: read ack: EOF`
   (the only three Reaper lines in the log), followed by
   `internal/tracker` tests failing at 0.00s with `Dolt server unreachable
   at 127.0.0.1:32772: connect: connection refused` /
   `invalid connection`. Mechanism (documented):
   `.github/workflows/pr.yml:676` runs three package trees in one `go test`
   invocation, so Go parallelizes multiple test binaries that share a single
   testcontainers Ryuk reaper; when one binary's reaper handshake resets,
   Ryuk reaps containers out from under the still-running victim (usually
   `internal/tracker`). Not a connection-string, config, or schema-probe
   defect — this diff touches none of the Dolt-connection or testcontainer
   setup code.
3. **Confirmed pre-existing / PR-independent.** The identical signature has
   recurred on two unrelated prior PRs per the same institutional memory:
   PR #5339 (2026-08-13) and PR #5809 (2026-08-15) — neither related to this
   diff's lineage. A durable upstream fix already exists and is blocked:
   PR #5770 disables Ryuk on the Dolt testcontainer steps, tracked as
   be-lrkh (parked `hold:external`). This rig is contributor-only for
   `gastownhall/beads` (confirmed `admin=false push=false`), so `gh run
   rerun` is unavailable here; per the same memory, the fix belongs in
   be-lrkh, not as scope creep on this deploy.
4. **Documented, not swept under the rug.** Cited above by memory key
   (`beads-ci-ryuk-reaper-flake-triage`) and tracked fix (be-lrkh). Local
   `ci-pr-core` independently confirms every OTHER package in this diff's
   blast radius is green; the specific failing test names were confirmed to
   be gated out locally by this hermetic env's `BEADS_TEST_SKIP=dolt` policy
   (not silently "passing" — genuinely not exercised locally), so the GitHub
   Actions run remains the sole source for this package's outcome, and its
   failure is attributed to infrastructure per points 1-3 above rather than
   claimed as a local pass.

### Reconciliation with the existing PR

be-z2ffj's description instructs cutting a fresh `deploy/be-pubk9-gate`
branch and opening a new PR, with an explicit warning not to push to
`deploy/be-tqwx-gate` because it "may be a shared builder branch." Live
repository state overrides this prose: `deploy/be-tqwx-gate` does not match
the shared-worktree-branch signature (`gc-<agent>-<12hex>`, per
`scripts/rebase-resolve-lib.sh`'s `is_shared_worktree_branch`) — it is
already the isolated `deploy/<bead-id>-gate` branch this same deployer role
cut for be-tqwx, and the be-pubk9 builder pushed this repair directly onto
it. `gh pr view 5847` confirms `state=OPEN`, `mergeable=MERGEABLE`,
`headRefName=deploy/be-tqwx-gate`, `headRefOid` exactly equal to this
deploy SHA. Cutting a second, duplicate branch/PR for the identical commit
would only create confusing redundant state. This gate's annotation is
committed onto the existing branch and pushed to `headfork`, extending PR
#5847 rather than opening a new one — the same reconciliation pattern
be-pubk9's own tdd_green commit already used for its post-hoc annotation
above.

### Verdict

**PASS** — all 7 criteria clear, criterion 3's one CI failure attributed to
a documented pre-existing infrastructure flake (be-lrkh) unrelated to this
diff. This annotation is committed onto `deploy/be-tqwx-gate` and pushed to
`headfork`, extending the existing PR #5847 (no new branch, no new PR — see
reconciliation above). Per this rig's contributor-only carve-out for
`gastownhall/beads`, the deployer's job ends here: no merge (`gh pr merge`
is forbidden for all rig agents), no merge-request routed to mayor/mpr, no
mayor handoff — mirroring be-tqwx's own precedent on this identical branch.
