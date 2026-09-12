# Release gate — uow tests leak a 200 MB bd binary per run in /tmp (be-3un7c)

- **Builder bead:** be-cd8oo — fix `buildBDBinary` in
  `internal/storage/uow/doltserver_provider_test.go` so its `MkdirTemp`
  scratch dir (holding a ~200MB compiled `bd` binary) does not outlive the
  package's test run.
- **Deploy bead:** be-3un7c
- **Review bead:** be-zf5a4 — verdict **PASS**, recorded on commit
  `c89fb55d53bf462682cdd4cc4267f05dd15783a0`
- **Commits:** `cfc837cd3b6db0b3bbb0c9ea0265b7b1675ac0c1` (tdd_red) then
  `c89fb55d53bf462682cdd4cc4267f05dd15783a0` (tdd_green), 2 commits over
  base `a690b0a8c4d1ddc4f0bd9bf767499625dd71bc96` (`origin/main`)
- **Source branch:** `builder/be-cd8oo` (provenance only, not a push target)
- **Isolated deploy branch:** `deploy/be-3un7c-gate` @
  `c89fb55d53bf462682cdd4cc4267f05dd15783a0`
- **Evaluated:** 2026-09-10 by beads/deployer

## Scope

Single file, `internal/storage/uow/doltserver_provider_test.go`
(+23/-1, confirmed via `git diff --stat origin/main..HEAD`): renames a
package var to `bdBinaryDir`, records `buildBDBinary`'s `MkdirTemp` scratch
dir into it, and adds a `TestMain` that removes that dir after `m.Run()`
and `os.Exit(1)`s with a diagnostic if `os.Stat` still finds it afterward.
Test-only change; no production code touched.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-zf5a4 records verdict PASS on commit `c89fb55d53bf462682cdd4cc4267f05dd15783a0` — exact match to this deploy's commit. |
| 2 | Acceptance criteria met | **PASS** | Sole AC: stop the per-run scratch-dir leak. Verified two ways: (a) `TestMain`'s own self-check did not trigger its FAIL/`os.Exit(1)` path — the package reported `ok`, not `FAIL`; (b) both tests that actually exercise `buildBDBinary` (`TestNewDoltServerUOWProvider_HappyPath`, `TestNewDoltServerUOWProvider_ConcurrentInstantiation`) pass by name, independently re-run below. |
| 3 | Tests pass (full-scope) | **PASS** | `make test` on the deploy branch: exit 0, **97 packages ok, 0 FAIL, 1 no-test** (`memoryops` — no test files; pre-existing, unrelated to this diff). `test_cmd_scope: full-suite`. `internal/storage/uow` (diff-owned) ran for real (not cached, not skipped): `ok 0.548s coverage: 35.7%`. See "Tests run" below for the by-name diff-owned re-confirmation and a note on an earlier misconfigured attempt. |
| 3a | Pre-existing-failure attribution | **N/A** | The authoritative full-suite run (below) has zero failures — nothing requires attribution. |
| 3b | Policy/lint lane | **PASS** | `scripts/ci/pr-policy.sh`'s checks re-run individually (doc-flags, doc-freshness, testing.Short boundaries, workapi frontend boundary, no `.beads/issues.jsonl` changes, `make api-check`): all exit 0. One pre-existing, non-blocking WARN (legacy SQLite references in `docs/CLI_REFERENCE.md`, `docs/architecture/dolt.md`, `docs/getting-started/upgrading.md`, `AGENT_INSTRUCTIONS.md` — none touched by this diff). `check-versions.sh`'s local `.githooks/commit-msg` marker failure is pre-existing and environment-local, not diff-owned, not caused by this diff, no path overlap — tracked by already-open **be-a0dxu** (filed 2026-08-30, predates this run). |
| 3c | CI-config diff needs its own lane | **N/A** | Diff touches only a `_test.go` file — no CI job/matrix/timeout/required-check changes. |
| 4 | No unresolved HIGH findings | **PASS** | be-zf5a4: `style_findings: none`, `security_findings: none` — full OWASP Top-10 walk; test-only diff, no injection/auth/secrets/config-change surface. |
| 5 | Clean working tree | **PASS** | `git status --porcelain` on `deploy/be-3un7c-gate` @ `c89fb55d5` is empty. |
| 6 | Clean divergence from `origin/main` | **PASS** | `origin/main` is still `a690b0a8c4d1ddc4f0bd9bf767499625dd71bc96` — unchanged since the reviewed commit's merge-base; `assert_deploy_ancestry_scope origin/main c89fb55d5... be-cd8oo be-3un7c` → rc=0 (re-run fresh immediately before push). |
| 7 | Single feature theme | **PASS** | One file, one purpose: the leak fix and its own self-verifying cleanup check. Nothing unrelated riding along. |

## Tests run on release branch (independent re-verification)

Full-scope, CI-equivalent command (`test_cmd_scope: full-suite`):

```
DOCKER_HOST="unix:///run/user/$(id -u)/podman/podman.sock" \
TESTCONTAINERS_RYUK_DISABLED=true \
make test
```

Result: exit 0. `ok_packages=97 fail_packages=0 notest_packages=1`. No
`FAIL`/`panic` lines anywhere in the log.

**Note on test command:** an earlier attempt additionally set
`BEADS_TEST_ENV_RUN_DOLT=1`, intending to force real (non-skipped) Dolt
coverage. That var is scoped exclusively to the separate, dedicated
`test-server-storage` CI shard (`.github/scripts/server-storage-test-shard.sh`),
which pairs it with a pre-built dedicated binary and a provisioned real
Dolt server — infrastructure a bare `make test` does not set up. Setting it
without that infrastructure made `internal/storage/dolt/federation_test.go`
correctly `t.Fatal` by design (`federation_test.go:1389-1390`), cascading
into ~30 failing/timed-out tests across `cmd/bd`, `cmd/bd/doctor`,
`cmd/bd/doctor/fix`, and `internal/storage/dolt`. That was a self-inflicted
test-invocation error, not codebase evidence, so it was discarded rather
than attributed; the corrected command above (no `BEADS_TEST_ENV_RUN_DOLT`)
is the run of record. `internal/storage/uow` (diff-owned) is unaffected by
this variable either way — `buildBDBinary` builds and runs its own `bd`
binary directly and never reads `beads_test_env`/this gate.

`diff_tests_executed` (`internal/storage/uow/doltserver_provider_test.go`,
re-run individually and verbosely, `-race`, for by-name confirmation):

```
--- PASS: TestNewDoltServerUOWProvider_ValidationErrors (0.00s)          [4/4 subtests PASS; does not call buildBDBinary]
--- PASS: TestNewDoltServerUOWProvider_HappyPath (26.31s)                [calls buildBDBinary; exercises the fix directly]
--- PASS: TestNewDoltServerUOWProvider_ConcurrentInstantiation (12.87s)  [calls buildBDBinary; exercises the fix directly]
ok  	github.com/steveyegge/beads/internal/storage/uow	40.460s
```

`TestMain`'s cleanup-check has no separate PASS line (it is not a `Test*`
function) — its evidence is negative-space: neither this run nor the
full-suite run above printed its
`"FAIL: ... was not cleaned up after the test run"` diagnostic, and neither
exited non-zero, which is the only way that check can fail.

`waiver_ref`: none needed. `skip_justification`: none needed (0 skips).

## Findings from review (no action required)

From be-zf5a4: no style or security findings. `gofmt -l` clean, `go vet`
clean; `golangci-lint` does not apply (`.golangci.yml` sets
`run.tests: false` and this diff touches only a `_test.go` file). Diff is
`os.RemoveAll` on a directory this same process created via
`os.MkdirTemp("", "bd-uow-test-*")` — no untrusted path content, no
symlink/TOCTOU concern, no auth/secrets/config/dependency surface.

## Verdict

**PASS** — all 7 criteria (plus 3a/3b/3c) clear. Per this repo's
contributor-only status (gastownhall/beads — we hold no merge rights here),
the job ends at the open PR: no merge-request will be routed to mayor and
no deploy-clearance status will be posted; the merge belongs to the
upstream maintainers.
