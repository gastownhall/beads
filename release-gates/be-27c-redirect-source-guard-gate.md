# Release gate — explicit --db target must override ambient redirect-source database (be-27c)

- **Original bead (CLOSED):** be-xil — `bd doctor` reports "Wrong database —
  tables found in crn" and offers a `--fix` that repoints the rig at another
  rig's store (P1)
- **Build bead (CLOSED):** be-fyt — follow-on regression fix in the same area:
  an explicit `--db`/`BEADS_DB`/`BD_DB` target could be silently shadowed by
  an unrelated ambient repo's redirect-source database
- **Review bead (CLOSED):** be-7m2 — Verdict **PASS** on commit `e93f14490`
- **Deploy bead:** be-27c
- **PM scoping decision (be-27c notes, 2026-08-15):** the reviewed commits sit
  atop 10 unrelated, unreviewed commits on the shared branch
  `gc-builder-e35c0415a93c` (an incremental-export feature and Dolt
  purge/schema/testutil infra, now tracked separately as be-hka/be-auu) plus
  one internal-tooling commit (`c0b942088`, `.claude/settings.json`) that must
  never reach a public OSS PR. PM authorized cutting the deploy branch from a
  clean `origin/main` and cherry-picking exactly the 3 reviewed commits,
  in order, with a hard constraint that the branch-tip diff stay
  scope-identical to what be-7m2 reviewed. All 3 applied with zero conflicts;
  contingency ("stop and report back") was not triggered.
- **Commits cherry-picked (in order):**
  1. `169ca8bcd` → `e7ca01273` — fix(doctor): preserve redirect-source
     dolt_database for no-DB commands (be-xil)
  2. `a1150637c` → `5a2287747` — test(doctor): red — explicit --db target
     must override ambient redirect
  3. `e93f14490` → `848ed9f90` — fix(doctor): green — guard redirect-source
     preservation on explicit --db
- **Deploy branch:** `deploy/be-27c-gate`, cut from `origin/main` @
  `185b339be6d5bf7553dc4af0e8a535055f02de4e` (verified exact match to
  `origin/main`'s current tip at evaluation time)
- **Source branch:** `gc-builder-e35c0415a93c` (provenance only for the 3
  cherry-picked commits — NOT the cut point and NOT a push target; see PM
  scoping decision above)
- **Evaluated:** 2026-08-14 by beads/deployer

## Scope

Fixes `cmd/bd/main.go`'s persistent pre-run logic: `preserveRedirectSourceDatabase(beads.GetRedirectInfo().LocalDir)`
now only fires when `dbPath == ""`, mirroring the sibling guard at ~line 1223.
Before this fix, an explicit `--db`/`BEADS_DB`/`BD_DB` target's own database
could be silently shadowed by an unrelated ambient repo's redirect-source
database — a narrower-trigger variant of be-xil's original wrong-database
failure mode. `cmd/bd/doctor_context_test.go` adds
`TestDoctorPersistentPreRunExplicitDBTargetOverridesAmbientRedirect` and
`TestDoctorPersistentPreRunPreservesSourceDatabaseAcrossRedirect`, asserting
`BEADS_DOLT_SERVER_DATABASE` stays empty and `BEADS_DIR` resolves to the
explicit target when both an ambient redirect and an explicit `--db` are
present simultaneously.

**Explicitly out of scope (PM scoping decision, not a gate concern):** the
incremental-export feature and Dolt purge/schema/testutil infra riding the
same shared builder branch — unreviewed this round, sequenced separately as
be-hka and be-auu. `c0b942088` (internal `.claude/settings.json` tooling
config) is excluded outright and must never reach this public repo.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-7m2 recorded `verdict: pass` (2026-08-15) on commit `e93f14490`, the exact tip of the 3 cherry-picked commits. Single-pass (Claude reviewer via beads/builder role). |
| 2 | Acceptance criteria met | **PASS** | Independently re-read the `cmd/bd/main.go` diff myself (not just the reviewer's restatement) — the `if dbPath == ""` guard at the `preserveRedirectSourceDatabase` call site matches be-fyt's exit_contract exactly. See "Acceptance" below. |
| 3 | Tests pass | **PASS** | See "Tests run on release branch" below — re-run independently by the deployer on the actual deploy branch, not copied from the review. |
| 4 | No high-severity review findings open | **PASS** | Zero HIGH findings. be-7m2's style_findings and security_findings both conclude no blockers/majors. Independently reconfirmed `gofmt -l`, `go vet ./...`, `go build ./...` myself — all clean. |
| 5 | Final branch is clean | **PASS** | `git status --short` on `deploy/be-27c-gate` shows no tracked-file changes (only a pre-existing untracked deployer tooling script, `scripts/rebase-resolve-lib.sh`, present in the worktree before this session and unrelated to the diff — not part of any commit, never pushed). |
| 6 | Branch diverges cleanly from main | **PASS** | `git merge-base HEAD origin/main` = `185b339be6d5bf7553dc4af0e8a535055f02de4e`, `origin/main`'s exact current tip — the deploy branch is a direct 3-commit fast-forward descendant. Zero conflict risk; no self-rebase needed. Checked first, per protocol, before the rest of the gate. |
| 7 | Single feature theme | **PASS** | `git diff origin/main..HEAD --stat`: exactly 2 files, `cmd/bd/main.go` (+23) and `cmd/bd/doctor_context_test.go` (+119), both in the same doctor-context-guard fix. No `.beads/` data. Matches be-7m2's reviewed diff byte-for-byte in scope. |

## Acceptance (per be-fyt's exit_contract, independently re-verified against the diff)

| Criterion | Status | Evidence |
|---|---|---|
| Guard `preserveRedirectSourceDatabase` on `dbPath == ""`, mirroring the sibling guard | ✓ | `cmd/bd/main.go`: new `if dbPath == "" { preserveRedirectSourceDatabase(...) }` inside the existing `skipsStoreInit` branch, matching the pre-existing sibling guard pattern at ~line 1223. Read the full diff myself. |
| New test: ambient CWD repo with active redirect + configured source `dolt_database`, explicit `--db` target at an unrelated repo with its own `dolt_database` — asserts `BEADS_DOLT_SERVER_DATABASE` stays empty and `BEADS_DIR` resolves to the explicit target | ✓ | `TestDoctorPersistentPreRunExplicitDBTargetOverridesAmbientRedirect` — read the full test body; setup and both assertions present verbatim, and it PASSED when I ran it (see below). |
| `go build ./...` and `go vet ./...` clean repo-wide, no collateral breakage in `TestDoctor*`/`TestBootstrap*`/`TestLoadSelectionEnvironment*` | ✓ | Both exit 0, zero output, run by me on `deploy/be-27c-gate`. Full `cmd/bd` package (which includes those families) is part of the zero-failure `ci-pr-core` run below. |

## Tests run on release branch

Ran independently by the deployer on `deploy/be-27c-gate` at `848ed9f90`
(not copied from the review) — Docker unavailable in this sandbox, so
Dolt-container-gated tests self-skip via their own package-level fixture;
neither diff-owned test depends on that fixture.

| Test | Result | Notes |
|------|--------|-------|
| `go build ./...` | success | clean, exit 0. |
| `go vet ./...` | success | clean, exit 0, repo-wide. |
| `gofmt -l cmd/bd/main.go cmd/bd/doctor_context_test.go` | clean | no output. |
| `TestDoctorPersistentPreRunExplicitDBTargetOverridesAmbientRedirect` (diff-owned) | **PASS** (0.01s) | `-race`, ran by name. |
| `TestDoctorPersistentPreRunPreservesSourceDatabaseAcrossRedirect` (diff-owned) | **PASS** (0.01s) | `-race`, ran by name. |
| `make ci-pr-lint` (`BD_LINT_NEW_FROM_MERGE_BASE=origin/main`, matching `.github/workflows/pr.yml`'s actual PR-lane invocation) | **PASS** | gofmt clean; golangci-lint 0 issues; golangci-lint (windows) 0 issues. An initial unscoped run surfaced 3 pre-existing `gosec` findings in `backend/conformance/*.go` — confirmed unrelated to this diff (file not touched) and an artifact of running push-lane whole-tree mode instead of PR-lane merge-base-scoped mode; they disappear entirely once scoped correctly. |
| `make ci-pr-policy` | **PASS** | All checks pass (stale-command refs, `bd init` flags, CLI docs coverage/freshness, plugin CLI pointer policy, `testing.Short` boundaries, workapi frontend boundary, openapi spec drift gate + `internal/httpapi` tests). `.beads/issues.jsonl` diff-check: "No .beads/issues.jsonl changes detected." One pre-existing `WARN` (legacy SQLite doc references in unrelated docs files) — not a failure, not touched by this diff. |
| `make ci-pr-core` (`go test -p 4 -parallel 4 -race -short -skip '^TestEmbedded' ./...`, full repo, hermetic `beads_test_env_enter`) | **ALL ~90 packages ok, ZERO FAIL** | 477s. Includes `cmd/bd` (289.972s) and `cmd/bd/doctor` (5.091s) — the packages that show ambient-environment-only failures under a non-hermetic invocation — both clean here, consistent with the known be-j8e ambient-contamination root cause rather than anything in this diff. |

`diff_tests_executed`: both diff-owned tests PASS, run by name with `-race`,
independent of the reviewer's own run. `skip_justification`: no diff-owned
test was skipped; environmental Dolt/Docker skips are pre-existing and
untouched by this diff. `waiver_ref`: none needed.

## Findings from review (no action required)

From be-7m2: no blockers or majors in either style or security review.

- OWASP A03 injection / broken-access-control: not applicable — pure local
  CLI guard, zero new deps, zero network/SQL/shell/template string
  construction. Independently confirmed by reading the diff.
- Minor, non-blocking (from review, still valid): the new guard adds no log
  line when it skips `preserveRedirectSourceDatabase` for an explicit `--db`
  target, so a future ambient-contamination regression here would again be
  silent. Matches the pre-existing sibling guard's behavior exactly — not a
  new gap introduced by this diff. Worth a follow-up bead for observability,
  not a blocker for this round.

## Process notes / deviations

- be-27c's literal description says to run `pre-pr-check.sh` before `gh pr
  create`. No such script exists anywhere in this repository (confirmed via
  `find` and a repo-wide grep) — this reads as stale boilerplate from a
  different rig's bead template. The actually-applicable, repo-authoritative
  gates are `make ci-pr-lint` / `ci-pr-core` / `ci-pr-policy`, per
  `CONTRIBUTING.md`'s explicit statement that "CI runs the same required
  wrapper on all pull requests" — all three run above.
- `docs/PROJECT_MANIFEST.md` (referenced generically as a possible criteria
  source) does not exist in this repo. The 7-criterion table already used
  above is self-contained and authoritative for this evaluation.
- Pre-flight already-merged check: searched open/closed PRs on both
  `gastownhall/beads` and `quad341/beads` for be-27c/be-xil/be-7m2 and for
  the new test names — no existing or prior PR covers this work.

## Push target

`origin` (`gastownhall/beads`) has push deliberately disabled
(`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR` — confirmed via
`git push --dry-run origin HEAD`, which fails with "does not appear to be a
git repository"); `fork` (`quad341/beads`) accepts (`git push --dry-run fork
HEAD` succeeds cleanly). PR opens cross-repo against `gastownhall/beads:main`
with head `quad341:deploy/be-27c-gate`.

`gastownhall/beads` is upstream-only for this deployer — we are
contributors, not maintainers. Per standing policy, job ends at PR-open; no
merge-request is routed, merge belongs to the upstream maintainers.

## Verdict

**PASS (7/7)** — commit this gate file, push to `fork`, open PR.
