# Release gate — Fix: runDoltSQL passes 134KB of schema SQL in one argv, past Linux MAX_ARG_STRLEN (E2BIG)

- **Build beads (CLOSED):** be-go6d (round 1 fix), be-wq49 (follow-up: wire
  `TestRunDoltSQLHandlesLargeScript` into the default lane)
- **Deploy bead:** be-al8j
- **Review bead:** be-d3zx — round 3, verdict **PASS**, recorded on commit
  `1cde0f0931025ea3783ffd757a444f3ff88084a0`
- **Commits:** 5 commits over `origin/main` —
  `9cc80debf` (red, be-go6d), `876b58cf8` (green, be-go6d),
  `ed302a5f3` (wire into default lane, be-wq49), `e6fd799c6` (red, be-wq49),
  `1cde0f093` (green, be-wq49 — the reviewed/deploy SHA)
- **Branch:** `builder/be-go6d` (provenance only, not a push target) — deploy
  cut to `deploy/be-al8j-gate`, pushed to `fork`
  (`quad341/beads-sec003-contrib`)
- **Evaluated:** 2026-08-19 by beads/deployer

## Scope

`runDoltSQL` (test helper that shells out to `dolt sql -q <query>`) was
passing large generated schema SQL as a single CLI argument, exceeding
Linux's `MAX_ARG_STRLEN` and failing with E2BIG — breaking 10 dolt-backed
tests. The fix, plus the follow-up making the regression test itself run in
the default (untagged) lane instead of only under `-tags=integration`.

Diff scope, confirmed directly via `git diff --name-only origin/main...HEAD`
(3 files) and independently reviewed file-by-file, not just diffstat'd:

- `internal/testutil/testdoltcommon.go` — extracts `requireDoltBinaryPresent`
  as a shared helper; adds `RequireDoltCLIOnly` (skips the `BEADS_TEST_SKIP=dolt`
  check for tests that only need the local `dolt` binary, not the
  containerized SQL server); `RequireDoltBinary`'s external behavior
  unchanged.
- `internal/storage/dolt/dolt_sql_large_script_test.go` (new) — the
  regression test, `TestRunDoltSQLHandlesLargeScript`, plus a locally-scoped
  `runDoltSQL` fixed to avoid the argv-length fault.
- `internal/storage/dolt/git_remote_test.go` — **not called out in the
  reviewer's style_findings list, independently caught and verified here.**
  Removes the old `runDoltSQL` (identical fix content) from this
  `integration`-tagged file, replaced with a comment pointing to its new
  home. Pure relocation, zero net logic change — necessary so the new
  default-lane test can call it without requiring the integration tag. Same
  single theme as the other two files.

`assert_deploy_ancestry_scope origin/main 1cde0f0931025ea3783ffd757a444f3ff88084a0 be-wq49 be-go6d`
run as a mechanical second check on top of the manual diff read: rc=0 — zero
`.claude/**` paths introduced, every one of the 5 commits cites `be-wq49` or
`be-go6d`.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 0 | Pre-flight: not already merged | **PASS** | `gh api repos/gastownhall/beads/commits/1cde0f0931025ea3783ffd757a444f3ff88084a0/pulls` → `[]`, no existing PR contains this commit. |
| 6 | Clean divergence from `origin/main` | **PASS** | `git merge-base --is-ancestor origin/main <deploy-sha>` confirms origin/main is a strict ancestor; `git rev-list --left-right --count origin/main...<deploy-sha>` → `0 5` (0 behind, 5 ahead). Trivial fast-forward, no self-rebase needed. |
| 1 | Review PASS present | **PASS** | be-d3zx closed, `verdict: pass`, round 3, on this exact commit. |
| 2 | Acceptance criteria met | **PASS** | Reviewer's round-3 exit_contract (5 criteria) independently re-verified below, plus full-suite re-run beyond what the reviewer scoped to. |
| 3 | Tests pass | **PASS** | See "Tests run" below — diff-owned test independently re-run, plus a full-repo `make test` sweep: 94/94 packages ok, 0 FAIL. |
| 3a | Non-diff-owned failures correctly attributed | **N/A** | No test failures occurred at all (diff-owned or otherwise) — this clause has nothing to attribute. Applies to the lint/policy lane instead; see criterion 3b. |
| 3b | Policy/lint lane | **PASS (non-diff-owned failures present, correctly attributed)** | `ci-pr-lint` and `ci-pr-policy` both exit nonzero, but both failures trace to files with **zero diff** against `origin/main` — see below. Diff-owned lint/policy surface is clean. |
| 4 | No unresolved HIGH findings | **PASS** | Reviewer: zero HIGH/MEDIUM findings, diff is pure test-infrastructure with no new dependencies or injection surface. The only findings anywhere in scope for this gate (3 gosec `G602`) are non-diff-owned, see below. |
| 5 | Clean working tree | **PASS** | `git status` at the deploy SHA shows no staged/unstaged changes on tracked files; only the pre-existing, unrelated untracked scratch files already documented in this worktree (`scripts/rebase-resolve-lib.sh`, prior gate leftovers), never staged. |
| 7 | Single feature theme | **PASS** | All 3 files serve exactly one purpose — see Scope above. Confirmed two independent ways: manual file-by-file diff read, and the mechanical `assert_deploy_ancestry_scope` guard (rc=0). |

## Tests run on release branch (independent re-verification)

Diff-owned test, run standalone under the real harness (matching the
reviewer's own documented methodology, bash not zsh):

```
source .buildflags && source scripts/ci/lib/test-env.sh && beads_test_env_enter && \
  go test -run '^TestRunDoltSQLHandlesLargeScript$' -v ./internal/storage/dolt/...
```

| Test | Result |
|---|---|
| `TestRunDoltSQLHandlesLargeScript` | **PASS** (matches reviewer's independent PASS) |

Full-repo canonical CI-equivalent (`make test` = `TEST_COVER=1 ./scripts/test.sh`,
the Makefile `test:` target), run against the checked-out deploy SHA with
`DOCKER_HOST=unix:///run/user/1000/podman/podman.sock` and
`TESTCONTAINERS_RYUK_DISABLED=true` (cached `dolthub/dolt-sql-server:2.2.0`
image confirmed present beforehand, no pull needed):

- **94/94 packages `ok`, 0 `FAIL`.** `Skipping: ` printed empty — no
  `BEADS_TEST_SKIP` override applied, this is the real default lane.
- `internal/storage/dolt` (the diff-owned package): `ok 10.519s`.
- `internal/testutil` (the other diff-owned package): `ok 0.117s`.
- Total coverage 38.5%, no regressions observed anywhere in the sweep.

This exceeds the reviewer's own scope (package-level regression only); a
full-repo sweep found nothing the reviewer's narrower run wouldn't have
caught, and adds confidence beyond it.

### Non-diff-owned lint/policy failures (criterion 3b)

`make ci-pr-lint` → golangci-lint reports 3 `gosec G602` (slice index out of
range) findings, all in `backend/conformance/{importer_contract,relations_contract}.go`.
`make ci-pr-policy` → version-consistency check reports `.githooks/commit-msg`
missing `BEGIN/END BEADS INTEGRATION` markers.

Both attributed, not waved through:

- `git diff --stat origin/main..HEAD -- backend/conformance/` and
  `-- .githooks/commit-msg` are **both empty** — neither path appears
  anywhere in this diff at all.
- `git blame` on the flagged `importer_contract.go` lines: last touched
  2026-08-09 by Julian Knutsen (`d38cd9d435`). `relations_contract.go`:
  last touched 2026-08-04, same author (`d801ec43dc`). Both predate this
  diff's commit chain (2026-08-18/19) entirely.
- Neither file shares any import or symbol with the two diff-owned packages
  (`internal/storage/dolt`, `internal/testutil`) — a pure test-infra argv
  fix in one package cannot plausibly have introduced a slice-bounds issue
  or a githook marker gap in unrelated files.

Both are real, pre-existing repo-wide issues, worth a follow-up bead, but
neither is owned by or attributable to this diff. Not filed by the deployer
directly (outside gate scope) — flagging to the operator/mayor in the
close-out instead.

## Findings from reviews (no action required)

From be-d3zx (round 3): no HIGH or MEDIUM findings. Security: pure
test-infrastructure diff, zero production code, zero new dependencies;
`requireDoltBinaryPresent`/`RequireDoltCLIOnly` only call `exec.LookPath`
and `os.Getenv` — no injection surface.

## Verdict

**PASS** — all 7 applicable criteria clear. Proceeding to cut
`deploy/be-al8j-gate` from `1cde0f0931025ea3783ffd757a444f3ff88084a0`, push
to `fork`, and open a contributor PR against `gastownhall/beads`. Per the
merge-authority carve-out for repos this deployer role does not maintain,
the job ends at the opened PR — no merge-request, no deploy-clearance
status.
