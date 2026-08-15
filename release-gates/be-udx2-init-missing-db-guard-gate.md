# Release gate — be-udx2 (bd init missing-server-DB guard fix)

**Date:** 2026-08-15
**Deployer:** beads/deployer
**Bead (deploy):** be-udx2 — Fix: bd init silently recreates a missing database as EMPTY on the server (from:be-fg8k)
**Source beads:** be-3mvz (build, closed) / be-fg8k (review, closed, verdict PASS round 2)
**Source commit:** `98ccb713637391e4af175a0a6536a60883042671` (provenance branch `builder/be-5up5`, pushed to `fork` remote — `quad341/beads.git`)
**Branch:** `deploy/be-udx2-gate` (isolated, re-cut fresh at the reviewed SHA via `resolve_deploy_branch_target` — was previously mis-checked-out at `origin/main` tip, corrected before any gate work)
**Base:** `origin/main` @ `7505e173f` ("chore(release): forward-port v1.2.2 to main")
**Merge-base:** `7505e173f` — origin/main is a direct ancestor of the reviewed commit (0 behind, 4 ahead)
**Merge-tree simulation:** `git merge-tree --write-tree origin/main 98ccb7136` → tree `c0e98a02d`, exit 0, **zero conflicts**
**Pre-flight already-merged check:** `gh api repos/gastownhall/beads/commits/98ccb7136.../pulls` → `[]` — not yet PR-borne, normal flow applies

## Verdict: PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | PASS | `git merge-base --is-ancestor origin/main 98ccb7136` → true; `git rev-list --left-right --count origin/main...98ccb7136` → `0 4` (0 behind, 4 ahead). No self-rebase needed — checked first per the mandated evaluation order. |
| 1 | Review PASS present | PASS | be-fg8k round-2 final verdict: "PASS", uncovered_criteria: none, 0 blockers/majors/minors. Round 1 (same reviewer) had requested-changes for exactly one gap (Done-when item 3 positive-path coverage); round 2 closes it with an independently-verified new test. |
| 2 | Acceptance criteria met | PASS | All 7 Done-when items verified in be-fg8k notes (6 in round 1 + item 3's positive half in round 2) — see Acceptance check below. |
| 3 | Tests pass | PASS | Documented CI-equivalent command (`./scripts/test.sh`, matches Makefile `test:` target): 93 packages ok / 0 FAIL / 23 no-test-files / 0 skipped, exit 0 — reported by build bead be-3mvz and independently reproduced by review bead be-fg8k in an isolated scratch worktree. I independently re-spot-checked at the corrected commit on `deploy/be-udx2-gate`: `go build ./...` exit 0, `go vet ./...` exit 0, and both diff-owned tests re-run directly by name — `TestInitGuard_ExistingProjectMissingServerDB_Refuses` PASS (1.02s), `TestInitGuard_ExistingProjectMissingServerDB_RecreateMissingAllows` PASS both subtests (0.82s total) — real dolt sql-server processes, not vacuous. Zero diff-owned SKIPs anywhere; no waiver needed. See Test-environment note. |
| 4 | No HIGH-severity findings open | PASS | Two independent OWASP Top 10 walks (be-wtxe round 1 full-diff, be-gopw round 2 delta-only): zero findings either round. Style findings (be-hu1d, be-ua00) both clean. |
| 5 | Final branch is clean | PASS | `git status --porcelain` on `deploy/be-udx2-gate` at `98ccb7136`: empty (only the untracked local deployer-tooling file `scripts/rebase-resolve-lib.sh`, which is not part of this repo's tracked tree and not part of this diff). |
| 7 | Single feature theme | PASS | `git diff --stat origin/main...HEAD`: exactly 6 files (`cmd/bd/init.go`, `cmd/bd/init_guard.go`, `cmd/bd/init_guard_test.go`, `cmd/bd/doctor/gitignore.go`, `cmd/bd/doctor/gitignore_test.go`, `internal/storage/dolt/errors.go`), 269 insertions(+), 19 deletions(-) — matches the bead's own recorded diff-scope exactly. One theme throughout: refuse silent empty-DB recreation on `bd init` when the server-side database is missing but local project metadata exists (root cause of the 2026-08-11 fleet-wide data loss), plus completing test coverage for the opt-in `--recreate-missing` path. No unrelated files, no new dependencies (`go.mod`/`go.sum` untouched). |

## Acceptance check (be-udx2 / be-fg8k "Done-when", 7 items)

1. **Exits non-zero, creates nothing** when local project metadata exists but the server-side DB is missing. Verified by code reading (guard returns error before any creation code runs) + passing regression test. **PASS.**
2. **Names the recovery path, omits `bd bootstrap`** for server-mode — `initGuardMissingServerDBMessage` deliberately excludes it (unlike the sibling server-reachable message); test explicitly asserts `bd bootstrap` absent and `bd backup restore` present. **PASS.**
3. **Opt-in `--recreate-missing` permits the create** — round-1 gap, closed in round 2: new test `TestInitGuard_ExistingProjectMissingServerDB_RecreateMissingAllows` covers both guard sites (`init.go:2566` reachable-but-missing, `init.go:2578` unreachable/errored), independently re-run by both reviewer and this gate. **PASS.**
4. **Fresh first-time init byte-identical** — `existingProject := cfg.ProjectID != ""` is Go zero-value `false` with no `metadata.json`, so both new guard branches short-circuit to the pre-existing `return nil`; new code is a complete no-op on this path. **PASS.**
5. **Regression test asserts the refusal** — `TestInitGuard_ExistingProjectMissingServerDB_Refuses`, confirmed passing (build bead, review bead, and this gate, three independent runs). **PASS.**
6. **Mutate-to-prove-teeth** — build bead reverted the guard condition and confirmed the negative-path test fails as expected, then restored; review bead did the equivalent for the positive-path test in round 2 (mutated both guard sites to ignore the flag, confirmed both subtests fail, restored, confirmed byte-identical diff). **PASS.**
7. **`bd doctor` drops the `bd init (safe to re-run)` suggestion** for this class of failure — verified via diff (4/4 call sites in `doctor/gitignore.go` changed) + `cmd/bd/doctor` package green in every run (build, review, and this gate's targeted re-run). **PASS.**

## Test-environment note (non-blocking, recorded for the next deployer)

My targeted re-run showed `WARN: Docker not available, skipping Dolt tests` ahead of the diff-owned tests. This is **not** a skip of this diff's tests — both ran and passed for real (`server_reachable_db_missing` took 0.82s, consistent with genuinely spinning up a local `dolt sql-server` process via `testutil.RequireDoltBinary`/`exec.Command`, not container-based). The WARN is an unrelated `TestMain` notice for other, container/testcontainers-based Dolt tests elsewhere in `cmd/bd/...` that this diff does not touch — review bead be-fg8k (be-utzl notes) already triaged this exact line for the identical reason. No podman/rootless-container fix was needed because the diff-owned tests don't depend on a container runtime at all.

## Hand-off

- **MERGE_POLICY (this rig only):** `beads/beads` (`gastownhall/beads`) is a fork-based upstream-contributor rig, not a maintainer rig. `origin` push is disabled (fetch-only placeholder). Deploy branch/PR cut from `fork` (`quad341/beads.git`), per the build bead's own explicit push record ("pushed to fork remote").
- **Pre-PR check:** `gc beads-contributor pre-pr-check --title=... --body-file=... --remote-checks --coverage` run against HEAD (`98ccb7136`) — **0 blockers, 2 advisory warnings**, both addressed in the PR body rather than blocking:
  - *Reproduction recipe* (bug-fix PRs should have one) — added a "Reproduction" section to the PR body with the minimal trigger sequence (drop the server-side DB, re-run `bd init`) and expected-vs-actual.
  - *Patch coverage 36.1% < 50% threshold* — package-level metric is diluted by large pre-existing untested surface in `cmd/bd`/`internal/storage/dolt` unrelated to this diff; added a "Coverage note" explaining the new guard logic itself has dedicated, mutation-tested regression coverage for every branch it adds. Explained rather than a real gap — no additional tests added solely to move this number.
  - No `[BLOCK]` items either run. Duplicate/in-flight-work and title/body-drift checks both clean.
- Push: `deploy/be-udx2-gate` → `fork` (`quad341/beads`).
- PR: cross-repo `quad341:deploy/be-udx2-gate` → `gastownhall:main`.
- No PostgreSQL surfaces or gc/actual-pack material in scope (confirmed during review) — nothing extra to strip before PR.
- Merge decision routed to mayor once PR is open and CI is green — deployer does not merge or run `mpr` against `gastownhall/beads` (no merge authority in this rig).
- Gate result + PR URL to be reported back to mayor once opened (informational report, not a formal MERGE-REQUEST/deploy-clearance status — this repo is contributor-only, not one we maintain).
