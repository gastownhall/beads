# Release gate — be-dy66n (Review: Phase 1: additive schema + migration slot claim - gastownhall/beads#6134)

**Date:** 2026-09-05
**Deployer:** beads/deployer
**Bead (deploy):** be-4ka6t
**Source bead:** be-dy66n — status closed, verdict `pass`. Reviewer notes confirm: gofmt/vet/build clean, full OWASP-Top-10-style security walk (9 categories, all N/A/checked, one non-blocking minor: insufficient logging, not required for this PR's exit contract), and all 5 exit-contract items verified first-hand (IF NOT EXISTS on both CREATE TABLEs; non-PREPARE ADD COLUMN idempotency guard; `TestProtocol_V2_DesignatedMigratorOverride` passes; no regression in 3 pre-existing diff-owned tests; redundant CLAIMED.md commit dropped). `uncovered_criteria: none`.
**Source commit:** `26033d8e476a88c6220ea9febd42e51272b0455c`
  - Parent: `41fd9c9b12737f5933c7ab9baf2c9efccf0f58af` (test(feat): red, refs be-xrl84)
  - Branch history: `460a2e660` (red, refs be-hs42e.2) → `74a9b8906` (green, refs be-hs42e.2) → `41fd9c9b1` (red, refs be-xrl84) → `26033d8e4` (green, refs be-xrl84)
  - All 5 SHAs (4 commits + base) independently re-verified via `git rev-parse --verify --quiet "<sha>^{commit}"` — all resolve.
  - Base: `origin/main` @ `c0d8da42de5fd15c95adac85e342ba4a121da0fb`. Re-confirmed via fresh `git fetch origin main` immediately before writing this gate: `origin/main` tip and `merge-base(HEAD, origin/main)` are identical — origin/main has not moved since the branch was cut.
**Branch:** `deploy/be-dy66n-gate`
**Push target:** `headfork` (`quad341/beads-sec003-contrib`) — pushed and independently re-verified: `git ls-remote headfork refs/heads/deploy/be-dy66n-gate` returns `26033d8e476a88c6220ea9febd42e51272b0455c`, matching local `HEAD` exactly.
**PR:** [gastownhall/beads#6304](https://github.com/gastownhall/beads/pull/6304) — `quad341:deploy/be-dy66n-gate` → `gastownhall:main`. Verified via `gh pr view 6304`: `state=OPEN`, `mergeable=MERGEABLE`, `author=quad341` (our own account — not an external contributor, no human-hold triggered).

## Verdict: 7/7 — PASS, no waivers

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review pass | PASS | be-dy66n closed, verdict `pass`, full first-hand reviewer verification of all 5 exit-contract items |
| 2 | Acceptance criteria met | PASS | All 4 diff-owned tests confirmed passing (see Criterion 3); reviewer's exit-contract walk independently corroborates |
| 3 | Full-suite tests | PASS (attributed) | See Criterion 3 detail below — 4 pre-existing, non-diff-owned failure classes attributed to trackers; zero diff-owned failures |
| 3a | Pre-existing-failure attribution | PASS | 4 failure classes, all attributed with clause-3 proof (see below) |
| 3b | Policy/lint lane (`ci-pr-policy` + `ci-pr-lint`) | PASS (attributed) | 2 further pre-existing findings attributed to trackers; see Criterion 3 detail |
| 3c | CI-config-diff live-run | N/A | Diff touches no `.github/` or `scripts/ci/` files (`git diff --name-only origin/main...HEAD` confirms) |
| 4 | Zero open HIGH | PASS | Reviewer's OWASP walk: 9 categories, all N/A/checked, one non-blocking minor (logging), no HIGH findings |
| 5 | Clean git status | PASS | `git status --short` clean on `deploy/be-dy66n-gate` at SHA `26033d8e4` |
| 6 | No merge conflicts with BASE_REF | PASS | `origin/main` unchanged since branch cut; merge-base == origin/main tip; no conflict possible |
| 7 | Single feature theme/ancestry scope | PASS | 5 files, +424/-1, all on-theme for migration-0067 versioned-beads schema (see diffstat below) |

**Diffstat** (`git diff --stat origin/main...HEAD`):
```
internal/storage/dolt/initschema_0067_versioned_beads_replay_test.go | 106 ++++++++++++
internal/storage/schema/migration_0067_versioned_beads_test.go      | 178 +++++++++++++++++++++
internal/storage/schema/migrations/0067_add_versioned_beads_schema.down.sql |  10 ++
internal/storage/schema/migrations/0067_add_versioned_beads_schema.up.sql   |  56 +++++++
internal/storage/schema/schema.go                                   |  75 ++++++++-
5 files changed, 424 insertions(+), 1 deletion(-)
```

## Criterion 3 — full-suite test evidence + policy/lint lane + CI-config-diff (3c)

**test_cmd:** `TEST_COVER=1 ./scripts/test.sh` (via `make test`)
**test_cmd_scope:** whole-repo, `BEADS_TEST_SKIP=dolt` default (skips all Dolt-backed tests repo-wide, including all `internal/storage/dolt` package tests)
**test_counts:** 90+ packages clean; `cmd/bd` package FAILed overall (282.560s) due to 4 named tests (below); all other packages `ok`

**diff_tests_executed** (all PASS):
- `TestLatestVersionIncludesMigration0067`
- `TestMigration0067AddsVersionedBeadsSchema`
- `TestMigration0067AddsVersionedBeadsSchemaThroughDoltCLI`
- `TestSchemaInitReplaysMigration0067WhenBookkeepingRowMissing` — independently re-verified by name under `BEADS_TEST_ENV_RUN_DOLT=1` + `-tags=integration,gms_pure_go` (this test lives in `internal/storage/dolt`, package-skipped by plain `make test`'s `BEADS_TEST_SKIP=dolt`)

**failure_attribution:**

1. `TestFindBeadsRepoRoot_WorktreeFallback, TestCountExistingIssues_WorktreeNoBeadsAnywhere, TestReset_WorktreeNoBeadsReturnsEmpty, TestReset_WorktreeSubdirFindsBeadsDir` (`cmd/bd`) -> `be-9ogs6` | clause 3: same root-condition class as be-9ogs6's tracked "ambient `.beads` directory defeats clean-workspace walk-up assumption" — this environment has a real ambient `/tmp/.beads` (birth ~13h before this run, unrelated to this diff) that satisfies these tests' "no `.beads` found anywhere under `/tmp`" assumption during walk-up. SHA `26033d8e476a88c6220ea9febd42e51272b0455c`, log in deployer scratchpad `be-dy66n-full-suite.log`.
2. `internal/storage/dolt` package, 9 pre-existing failures -> `be-irise` | clause 3: pre-existing, independently tracked failure class, not caused by this diff. clause 4 note: this diff adds a new test file to the *same package* (`initschema_0067_versioned_beads_replay_test.go`); flagged as a non-blocking observation on `be-irise` for whoever next runs the whole-package suite. Not gate-blocking: `make test`'s required full-suite command skips Dolt tests entirely, so none of the 9 appear in this gate's own criterion-3 evidence — confirmed via this run's own full-suite output, `internal/storage/dolt` reports clean `ok`.
3. `ci-pr-lint` (`golangci-lint`): 3x `G602: slice index out of range (gosec)` in `backend/conformance/importer_contract.go:390,392` and `relations_contract.go:672` -> `be-w4qbu` | clause 3: matches `be-w4qbu`'s known false-positive class (via its own comment history citing `be-ckoic`); confirmed not diff-owned (`git diff --name-only origin/main...HEAD -- backend/` empty). A first `ci-pr-lint` attempt also surfaced 29 issues under a phantom, nonexistent path (`../builder/worktrees/be-kh65q/...`) — diagnosed as a stale, content-hash-keyed `golangci-lint` shared result cache (`~/quad341-claude/.cache/golangci-lint`) leaking path metadata from a since-deleted worktree; `golangci-lint cache clean` + rerun reproduced only the 3 genuine, correctly-pathed findings above. This cache phenomenon itself also matches `be-w4qbu`'s own tracked history (`be-vf95`). Log `be-dy66n-ci-lint-2.log`.
4. `ci-pr-policy`: "check version consistency" step fails on `.githooks/commit-msg: no 'BEGIN BEADS INTEGRATION' marker found` -> `be-a0dxu` | clause 3: identical symptom reproduced verbatim, matches `be-a0dxu`'s tracked condition exactly — `.githooks/commit-msg` on disk is a local, untracked dev shim (excluded via `.git/info/exclude:40`), not part of the repo's committed `.githooks/` fileset; confirmed not diff-owned (`git diff --name-only origin/main...HEAD -- .githooks/` empty). **Cross-confirmed by the real upstream CI**: PR #6304's own "Check version consistency" GitHub Actions check passed cleanly on first report — this local-only shim does not exist on a clean checkout. Log `be-dy66n-ci-policy.log`.

**attribution_evidence:** all 4 citations posted as `bd comment` to their respective tracker beads this gate round (be-9ogs6, be-irise, be-w4qbu, be-a0dxu), each including SHA, log reference, and clause-3 proof.

**ci_lane_run (3c):** N/A — diff touches no `.github/` or `scripts/ci/` paths, so no CI-config live-run is required. `make ci-pr-policy` and `make ci-pr-lint` were still run in full as the standard 3b policy/lint lane (see failure_attribution items 3–4 above); both are attributed-PASS with zero diff-owned findings.

**waiver_ref:** none — no waiver needed, all failures cleanly attributed to pre-existing, non-diff-owned conditions with clause-3 proof and zero clause-4 path overlap (except the single non-blocking dolt-package observation on be-irise, which does not block this gate per the reasoning above).

**uncovered_criteria:** none

Beyond the above, `gc beads-contributor pre-pr-check` (10-point pre-PR sanity check) ran clean: 0 blockers, 0 warnings — branch 0 commits behind origin/main, 5 changed files, 4 commits, 1 top-level dir touched, no `.claude/**` paths, no postgres tokens, no undefined build tags. On the live PR, `gh pr checks 6304` at open time shows two already-resolved checks passing (`Resolve versions to test`, `Check version consistency` — the latter corroborating the local-shim diagnosis above) and the remainder queued/pending with zero immediate failures.

## Merge authority

This rig is a **contributor-only** participant in `gastownhall/beads` (upstream `origin` is fetch-only by design; all push/PR traffic goes through `headfork`/`prhead`, both `quad341/beads-sec003-contrib`). No rig agent — builder, reviewer, or deployer — holds merge rights on `gastownhall/beads`, and no rig agent ever runs `gh pr merge`. Per standing policy, the deployer's job for a contributor-only rig ends at a verified open, mergeable PR; this gate stops there and reports to mayor for **visibility only**, with no merge-request routed.

This follows established precedent: be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3, be-vc1m (PR #5792), be-7q688 (PR #6003), be-6iglh/be-0l89e (PR #6082), be-c8kgv (PR #6221), be-1wwre (PR #6247), be-3vzut (PR #6262), be-kqg23 (PR #6271).

## Disposition

**PASS, 7/7, no waivers.** PR [gastownhall/beads#6304](https://github.com/gastownhall/beads/pull/6304) opened, verified OPEN and MERGEABLE, authored by our own account (no external-contributor human-hold triggered). Four pre-existing, non-diff-owned failure/finding classes encountered during gate evaluation, all attributed to their exact pre-existing tracker beads (be-9ogs6, be-irise, be-w4qbu, be-a0dxu) with clause-3 proof and citation comments posted this round. Reporting to mayor for visibility only; no merge-request routed, per contributor-only merge-authority carve-out.

## Fix-round amendment — 2026-09-05, maintainer review of PR #6304

This gate's verdict stands as recorded for source commit `26033d8e4`. It is **not** re-run here; this section exists so the record does not read as current for a head it never evaluated.

Maintainer review on PR #6304 returned two decisions that change facts asserted above:

1. **`current_revision` is now on both planes.** `wisps.current_revision BIGINT NOT NULL DEFAULT 1` is carried for shape parity (nothing reads or writes it in any phase) — `TestSchemaParityIssuesVsWisps` enforces strict issues↔wisps column-name parity with no exemption list. This amends the section-8 issues-only decision the gated commit encoded. Because `wisps` is dolt-ignored, the change ships with an ignored-series twin, `migrations/ignored/0026_add_wisps_current_revision.up.sql` (check D of `scripts/check-migration-hygiene.sh`; precedent ignored/0013 for 0054, ignored/0020 for 0060).

2. **The ADD COLUMN idempotency guard moved from Go into the SQL.** Criterion 1's evidence line above records a verified "non-PREPARE ADD COLUMN idempotency guard" (`execMigration0067Body` in `schema.go`). That guard is gone and `schema.go` is back at `origin/main`: `internal/storage/dolt/pr4107_corruption_test.go`'s replay harness re-executes the frozen `.up.sql` bytes directly, so no Go-side guard is in that path and every migration ≥ 0046 must be idempotent as raw SQL on its own. Both ADD COLUMNs are now INFORMATION_SCHEMA-guarded PREPAREs (0060/0066 pattern), with a direct-DDL fresh-bundle override in `cliCompatibleMigrationSQL` for the pre-2.3 CLI hazard (0060/0065/0066 precedent).

The diffstat recorded above is likewise the gated commit's, not the fix round's.
