# Release gate — Make git-protocol CLI/SQL routing and Pull's silent-staleness failure mode observable (be-9i0yq.2)

- **Builder bead (CLOSED):** be-9i0yq.2 — add `logRouteDecision` logging at
  every CLI/SQL routing decision point in `pushToRemote` and
  `pullTransportReporting`, and cover it with tests, including a regression
  test proving Pull's silent-stale-success detection (`verifyPullLanded`)
  already exists and works.
- **Deploy bead:** be-j4oer
- **Review bead:** be-5u36o (CLOSED), verdict **PASS**, recorded on commit
  `efca1a21ca9d77723bf6c19c0859cfc200a98f28`. Round 1 (be-j7eon,
  request-changes) flagged one test-construction defect only — zero
  style/security findings in either round.
- **Commits:** `2fdfce0ff049a3cc0e4236f172edda48af13950c` (tdd_red) →
  `bcad1edbb938a39058c226f60006b65352501ff9` (tdd_green, round 1) →
  `efca1a21ca9d77723bf6c19c0859cfc200a98f28` (round-2 fix, tdd_green), 3
  files over `origin/main`.
- **Branch:** `builder/be-9i0yq.2` (provenance only; possibly shared, not a
  push target). Isolated deploy branch `deploy/be-j4oer-gate` cut fresh
  from `efca1a21c` and pushed to `headfork`
  (`quad341/beads-sec003-contrib`) — `fork`/`headfork`/`prhead` all
  independently confirmed push-capable via `git push --dry-run` exit-status
  this round (fork's older `quad341/beads.git` URL GitHub-redirects to the
  same renamed repo); `headfork` used to avoid the rename-redirect
  ambiguity. `origin`'s push URL is the literal disabled sentinel
  (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`, confirmed exit 128
  on dry-run) — fetch-only by design.
- **Evaluated:** 2026-08-22 by beads/deployer

## Scope

Diff scope, confirmed via `git diff origin/main...HEAD --stat` (three-dot,
merge-base — `origin/main` has advanced by one unrelated commit since this
branch's fork point, so two-dot would misreport that commit's own changes as
deletions):

```
 internal/storage/dolt/route_logging_integration_test.go | 100 ++++++++++++
 internal/storage/dolt/route_logging_test.go              |  89 ++++++++++
 internal/storage/dolt/store.go                            |  21 +++
 3 files changed, 210 insertions(+)
```

All three files are inside `internal/storage/dolt/`. `store.go` adds
`logRouteDecision` calls at 8 call sites (push: 3898/3908/3918/3924/3927;
pull: 4183/4194/4201/4207), each fed a `remote string` parameter traced
end-to-end to the `remote string // Default remote for push/pull` struct
field (store.go:351) — a configured name, never a URL or credential.
`route_logging_integration_test.go` is new (round-2 fix): moves
`TestPushPullLogCLIRouteForGitProtocolRemote` into its own
`//go:build integration` file with a real local `dolt sql-server` via
`startLocalDoltServer`, fixing a self-skip defect where the test's original
`New()`-based store never had a local `.dolt` dir to route CLI through.
`route_logging_test.go`'s diff is the other side of that move: the old
defective test body removed, replaced with a short pointer comment;
`TestPushPullLogSQLRouteForFileRemote` in the same file is untouched.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-5u36o records verdict PASS on commit `efca1a21c`, zero style/security findings in round 2 (independently re-run, not carried over from round 1). Round 1 (be-j7eon) was request-changes solely on a spec/test-construction defect (self-skip) — its own style_findings and security_findings were already `none` too. |
| 2 | Acceptance criteria met | **PASS** | All 4 round-1 exit_contract items independently re-confirmed by the reviewer in round 2, not re-accepted on the builder's word: (1) pull silent-stale-success detection (`verifyPullLanded`, store.go:4113, called from `pullFromRemoteUnchecked` store.go:4026) predates this branch's fork point per `git merge-base --is-ancestor 2240ee784 5cbe3a29a`; (2) no new false positives; (3) route-taken logging unregressed; (4) `TestPushPullLogCLIRouteForGitProtocolRemote` self-skip fixed. I independently re-ran all four named tests myself this round — see criterion 3. |
| 3 | Tests pass | **PASS** | Independently re-run against `efca1a21c` (not trusting reviewer's counts): `go build` clean (untagged + `-tags=integration,gms_pure_go`); `go vet` clean (both); `gofmt -l` clean on all 3 touched files. Targeted diff-owned + exit-contract run: **5 PASS / 0 FAIL / 0 SKIP** (`TestGitRemoteSyncRoundTrip`, `TestGitRemoteExternalServerRouting`, `TestPullReportsSuccessOnlyWhenTheMergeLanded`, `TestPushPullLogCLIRouteForGitProtocolRemote` [the sole diff-owned test — `git diff --name-only efca1a21c^!` touches only the two route_logging files], `TestPushPullLogSQLRouteForFileRemote`) — matches reviewer's reported counts exactly. Broad untagged sweep: **129 PASS / 0 explicit FAIL / 0 SKIP**, package run terminated by a pre-existing suite-wide 10-minute panic-timeout (see 3a). |
| 3a | Pre-existing-failure attribution independently re-confirmed | **PASS** | `TestCrossProject_ReadIsolation_DifferentPrefixes` is the test in flight when this run's own 10m timeout panic fired (`running tests: TestCrossProject_ReadIsolation_DifferentPrefixes (7s)`) — independently reproducing the identical failure signature the reviewer documented, in a separate process/run. Port 28231 reconfirmed live-held by the same foreign `dolt` process, same PID (`2479577`), as the reviewer's own live check. `cross_project_test.go` confirmed outside this diff's scope (`git diff --name-only origin/main...efca1a21c` — zero hits). Not diff-owned, not a regression, proven pre-existing by independent reproduction rather than trusted attribution. |
| 3b | Policy/lint lane | **PASS** | `make ci-pr-policy`: build-tag policy clean (99 files), go-install guidance clean, 7/8 version-consistency checks clean. One known non-diff-owned, non-repo-owned false positive — see below. |
| 4 | No unresolved HIGH findings | **PASS** | Zero style or security findings in both review rounds. Round 1's only gap (self-skip test construction) was a spec finding, not security/style, and is fixed + independently re-verified this round. |
| 5 | Clean working tree | **PASS** | `git status --porcelain` on the evaluated commit is empty before cutting the deploy branch. |
| 6 | Clean divergence from `origin/main` | **PASS** | `origin/main` advanced by exactly one commit (`3641fcf8f`, "key the deferred-parent table gate on which table is missing") since this branch's merge-base (`5cbe3a29a`) — touches only `internal/storage/domain/db/ready_work*.go`, `internal/storage/issueops/ready_work*.go`, and `internal/storage/embeddeddolt/ready_work_missing_table_test.go`: zero path overlap with this diff's 3 files. `git merge-tree --write-tree origin/main efca1a21c` exits 0, printing only the resulting tree OID — no conflict messages. No self-rebase needed. |
| 7 | Single feature theme | **PASS** | All 3 changed files live under `internal/storage/dolt/`, all serving one theme: CLI/SQL route-decision observability plus test coverage for it and for pull silent-staleness detection. No unrelated changes riding along. |

## Tests run on release branch (independent re-verification)

| Check | Result |
|---|---|
| `go build ./internal/storage/dolt/...` (untagged + `-tags=integration,gms_pure_go`) | both clean |
| `go vet ./internal/storage/dolt/...` (untagged + `-tags=integration,gms_pure_go`) | both clean |
| `gofmt -l` on all 3 touched files | clean (no output) |
| `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true CGO_ENABLED=1 GOFLAGS=-tags=integration,gms_pure_go go test ./internal/storage/dolt/ -run 'TestPushPullLog\|TestPullReportsSuccessOnlyWhenTheMergeLanded\|TestGitRemoteSyncRoundTrip\|TestGitRemoteExternalServerRouting' -v` | 5 PASS / 0 FAIL / 0 SKIP, 52.2s, real podman-backed `dolt-sql-server:2.2.0` container |
| `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true CGO_ENABLED=1 go test ./internal/storage/dolt/... -v` (untagged broad sweep) | 129 PASS / 0 explicit FAIL / 0 SKIP; package terminated by pre-existing 10m panic-timeout on `TestCrossProject_ReadIsolation_DifferentPrefixes` (624.5s total), see criterion 3a |
| `make ci-pr-policy` | 1 non-diff-owned false positive, see below; otherwise clean |

### `make ci-pr-policy` non-blocking finding (not diff-owned, not repo-owned)

`.githooks/commit-msg` fails the `BEGIN/END BEADS INTEGRATION` marker check.
Independently confirmed this is a local artifact, not a repo file:
`git ls-files .githooks/commit-msg` returns nothing (untracked), and
`git check-ignore -v .githooks/commit-msg` resolves it to
`.git/info/exclude` (a local, per-clone exclude list, not a committed
`.gitignore`). Also confirmed outside this diff's scope
(`git diff --name-only efca1a21c^! | grep commit-msg` — no match). Fires on
every gate round in this worktree regardless of diff; does not gate this
deploy.

## Findings from review (no action required)

From be-5u36o (round 2): zero style findings, zero security findings.
`route_logging_integration_test.go` read in full by the reviewer: correct
build tag, concurrency-slot guard matching suite convention, hard
`t.Fatalf` precondition (not a silent skip) — the direct fix for round 1's
defect. `route_logging_test.go`'s diff is a clean move (removal + pointer
comment), nothing orphaned. Security: pure test-construction-fix commit,
zero production-code changes this round; `remote` param re-confirmed
end-to-end as a configured name, never a URL, across all 8 call sites and
their callers.

## Verdict

**PASS** — all 7 criteria (plus 3a/3b) clear, including an independent
re-verification of every criterion 2/3 claim (build, vet, gofmt, the full
named test set, and the pre-existing-failure fingerprint) rather than
trusting the reviewer's evidence alone. Proceeding to open the PR from
`deploy/be-j4oer-gate` (`efca1a21ca9d77723bf6c19c0859cfc200a98f28`).

This repo (`gastownhall/beads`) is a contributor-only upstream for this
rig — merge authority belongs to upstream maintainers, not our
mayor/mpr. Per the repo-scoped carve-out, this deploy's job ends at the
open PR: no `gh pr merge`, no `release-gate/deploy-clearance` commit
status, no formal merge-request routing. Gate result is reported to mayor
as an FYI only.
