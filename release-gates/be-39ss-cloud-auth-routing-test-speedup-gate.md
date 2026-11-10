# Release gate — Shared-store speedup for `TestCloudAuthCLIRouting` (be-4wy3)

- **Builder bead (CLOSED):** be-4wy3 — `TestCloudAuthCLIRouting` spent 566s
  (measured in CI) creating 16 separate Dolt databases to test a pure
  routing predicate; restructured to share one store across all 16 cases,
  distinguished by remote name instead of by database.
- **Deploy bead:** be-39ss
- **Review bead:** be-yxlr — verdict **PASS**, zero findings (style/security/spec
  all "none"), recorded on commit `4ed9ee5e9e646e4244ad4127d863e38f6d55886a`
- **Commits:** `354660f66e2a7acb5f793271969c5d532be11f38` (RED — wall-clock
  regression guard) then `4ed9ee5e9e646e4244ad4127d863e38f6d55886a` (GREEN —
  single shared store), 1 file over `origin/main`
- **Branch:** `deploy/be-39ss-gate`, cut from
  `4ed9ee5e9e646e4244ad4127d863e38f6d55886a` (the bead's recorded `Commit:`
  field, resolved and verified — never a branch tip). Provenance-only source
  branch was `builder/be-4wy3`; not pushed to, not used as a PR head.
- **Evaluated:** 2026-08-18 by beads/deployer

## Scope

Restructures `TestCloudAuthCLIRouting` (`internal/storage/dolt/credentials_test.go`)
to open one shared Dolt store for all 16 table-test cases instead of one
store per case, distinguishing cases by remote name (`origin_0`..`origin_15`)
via `store.AddRemote` + `addCloudAuthCLIRemote` rather than by database. Adds
a `start := time.Now()` wall-clock regression guard (fails if the test
exceeds 90s) and explanatory comments to two `TestCloudAuthCLIRoutingStructural`
subtests that must keep their own fresh stores. Per-case `t.Setenv` /
`clearCloudAuthEnv` behavior is unchanged.

Diff scope, confirmed via `git diff --stat origin/main...HEAD` (1 file):

- `internal/storage/dolt/credentials_test.go` — test-only, 30 insertions / 4
  deletions. **No production code changed** — `shouldUseCLIForCloudAuth` and
  `prepareCLIRouteForCloudAuth` are untouched (confirmed by both builder and
  reviewer; routing stays keyed purely by remote name).

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-yxlr records `verdict: pass` on `4ed9ee5e9e646e4244ad4127d863e38f6d55886a`, with style/security/spec write-ups all "none". |
| 2 | Acceptance criteria met | **PASS** | be-4wy3's 4 done-when items all directly verified in be-yxlr's notes: all 16 cases still covered with identical expectations (16/16 subtests PASS by name); wall time 6.58s (builder GREEN) / 5.99s (reviewer re-run), both far under the <60s target and the 90s regression guard; all 4 named tests (`TestCloudAuthCLIRouting`, `TestCloudAuthCLIRoutingStructural`, `TestPerRemoteCloudAuthHybrid`, `TestEnvPrefixesForRemoteURL`) pass against a live server; no production-code behavior change. |
| 3 | Tests pass | **PASS** | Independently re-ran the reviewer's exact diff-owned test command against the checked-out deploy SHA (not trusted from the report) — see "Tests run" below. |
| 3a | Pre-existing-failure attribution | **N/A** | No test failures encountered in the diff-owned re-run; nothing to attribute. |
| 3b | CI-equivalent = every required lane | **PASS (attributed)** | Ran all three required PR lanes, not just the diff-owned test command. `ci-pr-core` and `ci-pr-lint` clean. `ci-pr-policy` failed on a version-consistency check unrelated to this diff — attributed per the non-diff-owned-gate-failure protocol; see below. |
| 4 | No unresolved HIGH findings | **PASS** | be-yxlr: zero findings of any severity (style/security/spec all "none"). |
| 5 | Clean working tree | **PASS** | `git status --porcelain` on the deploy branch shows no staged/unstaged changes (only pre-existing, unrelated untracked scratch files: 3 other `release-gates/*.md` and the stray untracked `scripts/rebase-resolve-lib.sh` copy — never sourced, never staged). |
| 6 | Clean divergence from `origin/main` | **PASS** | HEAD (`4ed9ee5e9e6`) is exactly 2 commits (RED + GREEN) on top of `origin/main`'s current tip; zero staleness, no rebase needed. Ancestry-scope check (`assert_deploy_ancestry_scope`) passed naming `be-39ss be-4wy3` — both flagged commits cite `be-4wy3`, confirmed as the legitimate build bead for this exact deploy via cross-reference in both be-39ss's and be-yxlr's own notes (a themed-deploy widening, not the be-27c unrelated-theme shape: `.claude/**` check also clean). |
| 7 | Single feature theme | **PASS** | Single file, test-only, both commits serve the one store-sharing speedup. No unrelated changes riding along. |

## Tests run on deploy branch (independent re-verification)

Diff-owned tests, run against the checked-out deploy SHA with real
podman/Dolt containers, matching the reviewer's own documented methodology:

```
DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock \
TESTCONTAINERS_RYUK_DISABLED=true BEADS_TEST_ENV_RUN_DOLT=1 scripts/test.sh \
./internal/storage/dolt/... -run '^(TestCloudAuthCLIRouting|TestCloudAuthCLIRoutingStructural|TestPerRemoteCloudAuthHybrid|TestEnvPrefixesForRemoteURL)$' -v -count=1
```

Result: **34 PASS, 0 FAIL, 0 SKIP** (4 top-level tests + 30 subtests),
19.046s — matches the reviewer's report (34/34, 45.269s) within environment
variance.

Required PR gate lanes (per `be-q8or`/`be-s424`/PR#5796 precedent: CI-equivalent
means every required lane, not just the test command):

| Lane | Result |
|---|---|
| `make ci-pr-core` (full-tree `go test -race -short -skip '^TestEmbedded' ./...`) | **PASS**, clean |
| `make ci-pr-lint` (gofmt + golangci-lint, `BD_LINT_NEW_FROM_MERGE_BASE=origin/main`) | **PASS**, clean |
| `make ci-pr-policy` (`scripts/ci/pr-policy.sh`) | **FAIL** — attributed, see below |

### `ci-pr-policy` failure attribution (non-diff-owned-gate-failure protocol)

`make ci-pr-policy` fails on the deploy SHA: `.githooks/commit-msg` is
missing the `BEGIN BEADS INTEGRATION` / `END BEADS INTEGRATION` markers that
every other `.githooks/*` file and version-pinned artifact carries, which
`scripts/update-versions.sh`'s version-consistency check treats as an error
(exit 1). All four attribution clauses independently checked:

1. **Not diff-owned** — `git diff --name-only origin/main...HEAD -- .githooks/ scripts/update-versions.sh version.go` is empty; the deploy diff touches only `credentials_test.go`.
2. **Tracked bead id** — none existed at evaluation time; filed **be-2q84** (P2) documenting the failure, its reproduction, and its non-relation to be-39ss.
3. **Proven pre-existing** — checked out `origin/main` directly (stashing untracked leftovers first, restoring after): `.githooks/commit-msg` is missing the same markers there too, independent of this diff.
4. **No path overlap** — same evidence as clause 1.

All four clauses satisfied → attributed, not blocking:
`failure_attribution: make ci-pr-policy version-consistency (.githooks/commit-msg markers) -> be-2q84, proven pre-existing on origin/main@7505e173f`.

## Findings from review (no action required)

From be-yxlr: zero HIGH, MEDIUM, or informational findings. Diff is
test-only; remote names passed to `AddRemote`/`shouldUseCLIForCloudAuth` are
internally generated (`fmt.Sprintf` loop index), not attacker-controlled —
no injection surface. No auth, access-control, logging, or dependency
changes.

## Verdict

**PASS** — all 7 criteria pass (3b via documented non-blocking attribution
to be-2q84). Proceeding to push `deploy/be-39ss-gate` and open a PR.

`gastownhall/beads` is a contributor-only repo (`origin` push is disabled —
`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`; neither this deployer
nor mayor holds merge rights there). Per the deployer's step-8 carve-out for
repos we do not merge, this gate's job ends at the open PR: push to `fork`,
open the PR, record its URL on be-39ss, close be-39ss. **No merge-request is
routed to mayor/mpr** — upstream maintainers own the merge — and no
`release-gate/deploy-clearance` commit status is published (that machinery
applies only to repos we actually merge: `gastownhall/gascity`,
`MechaCorpsGames/MCDClient`).
