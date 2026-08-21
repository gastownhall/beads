# Release Gate: be-2ym7w — Deploy review, container-local git-remote fixture for dolt server-mode tests

- **Bead:** be-2ym7w (deploy review; molecule be-q1w44)
- **Review bead:** be-r2msq (PASS round 2, beads/reviewer) — round 1 request-changes was solely a
  diff-owned-SKIP/no-waiver gate; mayor granted a narrow, dated, be-r2msq-scoped waiver for the 3
  SKIPs below, recorded in be-r2msq's own notes and `metadata.waiver_ref`. Style, security, and
  acceptance-criteria coverage were already clean in round 1; commit unchanged since.
- **Build bead:** be-nn2m (be-b81f decision: container-local git-remote fixture, Option D)
- **Deploy commit:** `fe59756f8de2a0bcf7e7999fa32277fa72541d0e`
- **Source branch:** `builder/be-nn2m` — provenance only, not pushed to or used as PR head
- **Deploy branch:** `deploy/be-2ym7w-gate` — freshly cut, isolated, checked out directly from the
  commit above.
- **Push target:** `headfork` (`quad341/beads-sec003-contrib.git`)
- **PR:** https://github.com/gastownhall/beads/pull/5925 (base `main`, head
  `quad341:deploy/be-2ym7w-gate`, 2 commits, tip `fe59756f8de2a0bcf7e7999fa32277fa72541d0e`)
- **Date:** 2026-08-21

## Scope

Provisions a bare git remote inside the shared `dolt-sql-server` testcontainer (be-b81f Option D)
via a bind-mounted host directory visible to both the test process and the container at the same
path, plus a container `Exec` accessor for tests that need it directly. `TestGitRemoteSyncRoundTrip`'s
clone step now clones server-side via `CALL DOLT_CLONE` over a raw SQL connection
(`versioncontrolops.DoltClone`), mirroring `cmd/bd/bootstrap.go`'s `cloneViaServer` pattern.

## Gate criteria

| # | Criterion | Result | Notes |
|---|-----------|--------|-------|
| 1 | Review PASS | ✅ PASS | be-r2msq: reviewed and passed by beads/reviewer (round 2) |
| 2 | Acceptance criteria met | ✅ PASS | be-r2msq: all be-nn2m deliverables (1-4) covered; deliverable 3 (comment reword) non-functional/untestable by nature |
| 3 | Tests pass (diff-owned) | ✅ PASS | 15 PASS, 0 FAIL, 3 SKIP re-run independently — see table below, exact match to be-r2msq's counts |
| 3b | Policy/lint lane | ✅ PASS* | `make ci-pr-policy` fails solely on the documented `.githooks/commit-msg` environmental false positive; see below |
| 4 | No unresolved HIGH findings | ✅ PASS | be-r2msq recorded no blocking security findings (2 informational-only, both traced and non-exploitable) |
| 5 | Clean working tree | ✅ PASS | No stray stash activity this round (checked `git stash list` before and after — unchanged 12 stale entries, none new); working tree matched target commit byte-for-byte throughout |
| 6 | Clean divergence from origin/main | ❌ **NOT CLEAN** | Real content conflict — see below. Flagged, not resolved, per established precedent of surfacing rather than unilaterally fixing what's outside deployer's decision authority. |
| 7 | Single feature theme | ✅ PASS | Both non-merge commits (`74a46b64a`, `fe59756f8`) cite `be-nn2m`; `assert_deploy_ancestry_scope` clean (rc=0), no `.claude/**` paths |

### Criterion 6 — merge conflict with origin/main (real finding, not resolved here)

`origin/main` carries 11 commits not in this deploy. Diff-overlap check found 2 files touched by
both sides: `internal/storage/dolt/store.go` (auto-merges cleanly) and
`internal/storage/dolt/git_remote_test.go` (**does not** — 8 real conflict hunks).

Root cause: `main` independently picked up a *different* rewrite of the same shared git-remote
test fixture via #5892 (`2240ee784`) and #5889 (`4daf88082`) — a host-local `dolt sql-server`
design (`startLocalDoltServer`, `gitRemoteSetup.serverPort`, remote provisioned on the host).
This deploy's fixture takes a container-based design instead (`gitRemoteSetup.dbName`, remote
provisioned inside the shared testcontainer via bind-mount). Both rewrote the same
`gitRemoteSetup` struct, `setupGitRemote`/`setupContainerGitRemote`, and several call sites, in
mutually incompatible ways — confirmed via a throwaway `git merge --no-commit --no-ff
origin/main` (aborted, no changes kept): struct-field-level conflicts (`dbName` vs `serverPort`,
possibly both needed), a whole added function (`startLocalDoltServer`) only on `main`'s side, and
divergent fixture-setup bodies (container-provisioned vs host-provisioned bare repo). This is not
a mechanical/whitespace conflict — reconciling it requires engineering judgment from someone with
context on both fixture designs (does the merged fixture need both `dbName` and `serverPort`? do
the two provisioning strategies need to coexist or does one supersede the other?), which is
outside the deployer's role and this session's authority to decide blind.

**Not resolved here.** The deploy branch and PR carry the exact reviewed commit
(`fe59756f8de2a0bcf7e7999fa32277fa72541d0e`), unmodified — attempting a conflict resolution would
mean shipping code be-r2msq never reviewed. PR #5925 is confirmed via `gh pr view` to show
`mergeable: CONFLICTING`, `mergeStateStatus: DIRTY` — this is visible and honest on the PR itself,
not papered over. Flagged in the PR body and in bd notes for mayor/Jim to route to a rebase-aware
builder (likely whoever owns be-9i0yq's broader SQL-routing investigation, since both fixture
designs exist because of the same `hasCLIDatabase()`-false gap).

This is a materially different situation from be-g3iz8's round (zero file overlap, no rebase
needed) — not the same "clean" pattern, called out explicitly rather than reusing that verdict.

The `.claude/**` denylist check is clean (covered by `assert_deploy_ancestry_scope`, rc=0).

## Tests run on release branch

Diff-owned test file: `internal/storage/dolt/git_remote_test.go` (also touches `store.go`,
`internal/testutil/testdoltserver.go`).

Independently re-ran be-r2msq's own `test_cmd` (full diff-owned file, all 18 functions) against a
live Dolt/podman testcontainer:

```
cd internal/storage/dolt && env -u BEADS_DOLT_SERVER_PORT -u BEADS_DOLT_AUTO_START \
  TMPDIR=$HOME/.gotmp GOTMPDIR=$HOME/.gotmp DOCKER_HOST=unix:///run/user/1000/podman/podman.sock \
  TESTCONTAINERS_RYUK_DISABLED=true BEADS_TEST_ENV_RUN_DOLT=1 \
  go test -tags=integration,gms_pure_go -count=1 -v \
  -run '^TestGitRemote|^TestCreateIssueAfterPull|^TestSQLRemotePersistsAcrossExternalServerRestart|^TestCredentialCLIRoutingE2E$' .
```

| Test | Result | Time |
|------|--------|------|
| TestGitRemoteAdd | PASS | 1.11s |
| TestGitRemotePushEmptyDB | PASS | 1.66s |
| TestGitRemotePushWithData | PASS | 2.45s |
| TestGitRemotePushIdempotent | PASS | 3.05s |
| TestGitRemotePushIncremental | PASS | 2.26s |
| TestGitRemoteClone | PASS | 2.00s |
| TestGitRemotePull | PASS | 2.17s |
| TestGitRemotePullWithLocalChanges | PASS | 2.30s |
| TestGitRemoteRoundTripAllTables | PASS | 3.44s |
| TestGitRemoteSpecialCharacters | PASS | 3.09s |
| TestGitRemotePushPull | SKIP | 0.00s (be-9i0yq, mayor-waived) |
| TestGitRemoteHasRemote | PASS | 6.11s |
| TestGitRemotePushSkipsUserPrePushHook | SKIP | 0.00s (be-9i0yq, mayor-waived) |
| TestGitRemoteSyncRoundTrip | PASS | 14.55s |
| TestCreateIssueAfterPull | SKIP | 0.00s (be-9i0yq, mayor-waived) |
| TestGitRemoteExternalServerRouting | PASS | 5.82s |
| TestSQLRemotePersistsAcrossExternalServerRestart | PASS | 3.33s |
| TestCredentialCLIRoutingE2E | PASS | 15.33s |

`ok github.com/steveyegge/beads/internal/storage/dolt 74.398s` — 15 PASS, 0 FAIL, 3 SKIP, exact
match to be-r2msq's recorded counts. The 3 SKIPs are the mayor-waived set (waiver_ref on
be-r2msq: "diff-owned SKIP waived on be-r2msq notes ... conditional on be-9i0yq").

Also: `gofmt -l .` clean. `go vet ./...` clean. `go build ./...` clean.

### Policy/lint lane (criterion 3b)

`make ci-pr-policy` fails solely on the `.githooks/commit-msg` BEGIN/END marker check — the
established, repeatedly-reconfirmed non-issue (be-jy56/be-jygq, reconfirmed in be-p7dzx, be-y1jo,
be-g3iz8; see `.gc` memory `reference_githooks_commit_msg_false_positive`). Not a repository file;
a git-ignored per-session shim regenerated every session start. All other sub-checks
(build-tag policy, go-install guidance, version-consistency) PASS.

### Pre-PR check

`gc beads-contributor pre-pr-check --remote-checks --title=... --body-file=...` run **before**
`gh pr create` (proactively this round): 0 blockers, 1 warning (title/body cites `be-r2msq` not
present in commits — expected noise, review-bead ids never appear in feature commits). Its
mergeability check ("branch is 11 commits behind, <=25 OK") is commit-count-only and does **not**
detect real content conflicts — it did not catch the criterion-6 finding above; that was found
via a manual throwaway `git merge --no-commit` check, not by this tool.

## Findings from reviews

be-r2msq (beads/reviewer): PASS (round 2). No unresolved HIGH findings. 2 informational security
notes (fixed shared bind-mount path; unquoted shell suffix in a test helper), both traced and
judged non-exploitable — test-only code, fixed random-hex-only input.

## Verdict

**PASS, with an unresolved criterion-6 flag.** Deploy branch `deploy/be-2ym7w-gate` cut from
verified commit `fe59756f8...`, pushed to `headfork`, PR #5925 opened against
`gastownhall/beads:main` carrying the exact reviewed commit, unmodified. The reviewed code itself
is correct, tested, and secure — what's not clean is a routine (if unusually substantive) merge
conflict with unrelated concurrent work on `main`, which is a mergeability/rebase problem, not a
defect in this deploy's content. Surfaced explicitly in the PR body, this gate file, and bd notes
rather than silently resolved or silently hidden.

`gastownhall/beads` is contributor-only for this rig (established repeatedly: be-vc1m, be-gd3v,
be-79jh, be-krza3, be-pp7e, be-y1jo, be-g3iz8) — merge authority does not extend to an upstream
repo this rig doesn't own. The deployer's job ends at the open PR; no functional merge-request is
routed to mayor/mpr (be-2ym7w's own instruction text to do so is boilerplate that assumes
maintainer status, inapplicable here per established precedent). Gate result reported to mayor
per the bead's separate, applicable instruction to do so.
