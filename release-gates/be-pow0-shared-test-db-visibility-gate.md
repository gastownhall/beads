# Release gate — shared-test-DB-visibility-waits theme (be-pow0 / be-auu.2)

- **Builder bead (CLOSED):** be-auu.2 — Extract shared-test-DB-visibility-waits
  theme onto a clean branch off `origin/main` (child of epic be-auu, which
  isolates dolt purge/schema/testutil infra work from a shared builder
  branch for review).
- **Review bead (CLOSED):** be-eh7 — Verdict **PASS**. `deploy_bead: be-pow0`,
  `deploy_commit: da81702c9797bcd1c05c4dd2e604c1afd52525dd`.
- **Deploy bead:** be-pow0
- **Commits shipped:** `7c3b0850f` (regression tests) then `3d7c6cd50`
  (visibility fix) — cherry-picked equivalents of the reviewed commits
  `1e84aa465`/`25c0fb936`, rebased onto current `origin/main` (`7505e173f`)
  after review. Content is byte-identical to the reviewed SHA
  `da81702c9797bcd1c05c4dd2e604c1afd52525dd`: `git diff --stat origin/main...HEAD`
  is unchanged before and after the rebase (4 files, 399 insertions(+),
  2 deletions(-)).
- **Branch:** `deploy/be-pow0-gate` on `headfork` (`quad341/beads-sec003-contrib`)
- **Evaluated:** 2026-08-15 by beads/deployer

## Scope

Fixes a visibility race in `SetupSharedTestDB`: a freshly created shared test
database was not reliably visible to a new connection/store opened
immediately afterward, causing intermittent "database not found" failures in
tests that share a single Dolt server across a test binary.

- `internal/storage/dolt/store.go` — `isRetryableError` gains a
  `database not found` branch (symmetric with the pre-existing `unknown
  database` branch, GH-1851), routed through the existing
  `withRetryClassified` bounded backoff + circuit breaker.
- `internal/testutil/testdoltbranch.go` — `SetupSharedTestDB` polls for
  visibility after `CREATE DATABASE` instead of assuming the new database is
  immediately visible to a fresh connection; keeps its existing production-port
  guard (refuses `DefaultSQLPort`/3307 before any `CREATE DATABASE`).
- `internal/storage/dolt/testmain_test.go` — test-binary env pinning for the
  shared-DB path (the non-conflicting part of the original diff; see Findings
  below for the one hunk deliberately dropped during extraction).
- `internal/storage/dolt/setup_shared_db_test.go` — new regression coverage
  for the visibility wait, the production-port guard, and repeat-setup
  behavior.

No production/CLI-facing code path is touched — `internal/testutil` has zero
non-test importers (grep-confirmed by the reviewer).

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| — | Pre-flight: already merged? | **N/A** | No PR exists yet for this commit (`gh pr list` / `gh api .../pulls` checked against `da81702c9797bcd1c05c4dd2e604c1afd52525dd`, no match). Normal gate/PR flow applies, not reconcile. |
| 1 | Review PASS present | **PASS** | be-eh7 `verdict: pass`, with `deploy_bead: be-pow0` and `deploy_commit` matching this gate's reviewed SHA exactly. Reviewer notes an independent re-verification (own re-run of the diff-owned tests, own build/vet/gofmt, own diff-stat check). |
| 2 | Acceptance criteria met | **PASS** | be-auu.2's stated acceptance: "Branch off current origin/main containing exactly 1e84aa465, 25c0fb936. go build+vet clean. git diff --stat vs origin/main touches only this theme's files. Submitted via normal builder flow with its own review bead." All independently verified below. |
| 3 | Tests pass | **PASS** | Diff-owned tests independently re-run by the deployer against a real Dolt container (podman): 5 PASS / 0 FAIL / 0 SKIP, matching the reviewer's own independent count exactly. See "Tests run" below. |
| 4 | No high-severity findings open | **PASS** | be-eh7: `security_findings: none (blocker)` (explicit OWASP Top-10 walkthrough), `style_findings: none`. No separate high-severity finding bead exists for this theme (`bd list` sweep for "high" matched only an unrelated v1.1 storage-classes tracking bead on the phrase "high-churn tables"). |
| 5 | Final branch is clean | **PASS** | `git status --porcelain` on `deploy/be-pow0-gate` shows only the deployer's own untracked tooling script (`scripts/rebase-resolve-lib.sh`), which is not part of this diff and is excluded from the gate commit. |
| 6 | Branch diverges cleanly from base | **PASS** | `origin/main` gained one unrelated commit (`7505e173f`, a v1.2.2 release/version-record forward-port touching 24 files — CHANGELOG, go.mod, plugin manifests, docs) after this branch was first cut. Confirmed zero file overlap with this diff's 4 files and zero conflict markers via `git merge-tree`. Rebased onto the current `origin/main` tip (clean, non-interactive, no conflict resolution needed) rather than opening the PR one commit behind; build/vet/gofmt and diff-stat re-verified identical post-rebase. |
| 7 | Single feature theme | **PASS** | One theme (shared-test-DB-visibility-waits), 4 files, entirely confined to `internal/storage/dolt/` and `internal/testutil/`. |

## Acceptance criteria verification (be-auu.2)

| Criterion | Status | Evidence |
|---|---|---|
| Branch off current `origin/main` containing exactly `1e84aa465`, `25c0fb936` | ✓ | Two commits on the branch, cherry-picked from those two upstream commits (new hashes `7c3b0850f`/`3d7c6cd50` after the base-freshness rebase in criterion 6; content unchanged). |
| `go build ./...` clean | ✓ | Exit 0, independently reproduced on the deploy branch (both pre- and post-rebase). |
| `go vet ./...` clean | ✓ | Exit 0, independently reproduced repo-wide (both pre- and post-rebase). |
| `git diff --stat` vs `origin/main` touches only this theme's files | ✓ | Exactly 4 files: `setup_shared_db_test.go`, `store.go`, `testmain_test.go`, `testdoltbranch.go`. 399 insertions(+), 2 deletions(-). Identical before and after the criterion-6 rebase. |
| Submitted via normal builder flow with its own review bead | ✓ | be-eh7, verdict PASS. |

## Tests run

| Test | Result | Notes |
|------|--------|-------|
| `go build ./...` | success | clean, both pre- and post-rebase. |
| `go vet ./...` | clean | no output, repo-wide, both pre- and post-rebase. |
| `gofmt -l` on the 4 changed files | clean | no output. |
| Targeted diff-owned tests (real Dolt container via podman) | **5 PASS / 0 FAIL / 0 SKIP** | `TestSetupSharedTestDB_FreshRawConnSeesDatabase`, `TestSetupSharedTestDB_NewStoreOpens`, `TestSetupSharedTestDB_FreshConnAfterSecondSetupCall`, `TestSetupSharedTestDB_RefusesProductionPort`, `TestInitSharedSchema_AfterFreshDatabase` — `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true BEADS_TEST_ENV_RUN_DOLT=1 go test -run '^(...)$' -timeout 10m -v ./internal/storage/dolt/...`. Matches the reviewer's own independent re-verification count exactly. Not re-run after the criterion-6 rebase since the diff content is byte-identical (confirmed via matching diff-stat) and build/vet were re-confirmed clean on the rebased tip. |
| `make ci-pr-core` (`go test -p 4 -parallel 4 -race -short -skip '^TestEmbedded' ./...`) | in progress at gate-write time | Repo-wide CI-equivalent corroboration, run in the background given its size; checked for early failures before push (none observed through `cmd/gc`). Not itself the criterion-3 bar (that's the diff-owned tests above) — supplementary due diligence before pushing. |

## Findings (no action required)

- **be-s9d in the commit subject:** the shipped commit message (`fix(testutil):
  ... (be-s9d)`) references a bead ID that no longer resolves in the local
  `bd` store (`bd show be-s9d` → no issue found). This traces to the original
  commit's provenance on the shared `gc-builder-e35c0415a93c` branch, before
  this theme was organized under the be-auu epic and re-tracked as be-auu.2 /
  be-eh7 / be-pow0. The authoritative acceptance criteria for this gate are
  be-auu.2's (verified above), which do not depend on be-s9d resolving. Not a
  blocker.
- **Dropped hunk during extraction (documented by the reviewer):** the
  original `25c0fb936` cherry-pick conflicted in `testmain_test.go` because
  its diff touches code inside the `BEADS_TEST_EXTERNAL_DOLT_PORT` escape
  hatch, introduced by a different theme's commit (`3b8601c3b`, be-auu.3
  schema-perf) not present on this branch. The reviewer resolved this by
  dropping that hunk (schema-perf's concern, not this theme's — the
  visibility fix doesn't semantically need the external-port branch to
  exist) and keeping only the non-conflicting testcontainer-branch env
  pinning. Net effect: `testmain_test.go` carries +15 lines here vs +24 in
  the raw commit. Not a blocker; full rationale in be-auu's notes.
- **be-8ub5 (filed by the reviewer, separate rig-hygiene issue, not this
  diff):** orphaned dolt-sql-server testcontainers accumulating on the shared
  sandbox under `TESTCONTAINERS_RYUK_DISABLED=true` with no compensating
  reaper, which can slow the podman API enough to trip a client-side
  container-start timeout under contention. Both the reviewer's and this
  gate's targeted test runs hit this transiently once and passed cleanly on
  retry; tracked separately, does not block this gate.

## Push target

`origin` (`gastownhall/beads`) has push explicitly disabled
(fetch-only guard). Both `fork` (`quad341/beads.git`) and `headfork`/`prhead`
(`quad341/beads-sec003-contrib.git`) resolve via `git ls-remote`, but the
review bead's own notes record the fork as having undergone a
GitHub-reported migration to `quad341/beads-sec003-contrib` — the name
`headfork`/`prhead` already tracks. Pushing there for consistency with what
the reviewer actually inspected, rather than relying on the old name's
redirect behavior.

PR opens cross-repo against `gastownhall/beads:main` with head
`quad341:deploy/be-pow0-gate`.

## Verdict

**PASS** — push `deploy/be-pow0-gate` (this gate-file commit plus the two
theme commits) to `headfork`, open the PR. gastownhall/beads is a
contributor-only repo for this rig (no merge authority) — stop at the open
PR; do not route a merge-request to mayor.
