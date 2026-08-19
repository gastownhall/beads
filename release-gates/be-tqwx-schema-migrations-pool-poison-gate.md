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
