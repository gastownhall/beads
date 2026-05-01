# Release gate — be-ripy (be-avn `staleDatabasePrefixes` canonical sync)

**Date:** 2026-04-30
**Deployer:** beads/deployer (deployer-1, fresh evaluation post-builder-rebase)
**Bead (review):** be-ripy — Review: be-avn staleDatabasePrefixes canonical sync (AD-04 follow-up)
**Feature bead:** be-avn (closed; source bead)
**Reviewed commit:** `0f76453f` (rebased equivalent: `ee67d5f9` on `be-vzu-rebase-fix`)
**Final branch:** `release/be-ripy` @ `1014c759` (cherry-pick of `ee67d5f9` onto `origin/main`)
**Base:** `origin/main` @ `8694c535` ("doctor: detect AGENTS.md / CLAUDE.md user-authored divergence (#3600)")

## Verdict: PASS

## What this ships

`bd dolt clean-databases` cleanup-side prefix list converged with the
firewall-side `testDatabasePrefixes`. After this change:

- `staleDatabasePrefixes` becomes `[testdb_, beads_test, beads_pt, beads_vr,
  doctest_, doctortest_, benchdb_]` — the architect-canonical 7-prefix list.
- Net behavioral change vs. prior list:
  - **+** `benchdb_` now cleaned (closes the AD-04-documented bench-DB leaker)
  - **+** `beads_test` now cleaned (matches firewall scope when be-c5p lands)
  - **−** `beads_t<hex>` protocol-test prefix no longer matched by cleanup;
    those workspaces clean themselves up in tearDown per AD-04 problem statement
- The doc-block on `staleDatabasePrefixes` is restructured to mirror
  `testDatabasePrefixes` (per-prefix origin notes plus a cross-reference
  to the two sibling lists). The `Long:` help string is updated.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | reviewer-gm-po6pox3 PASS verdict in be-ripy notes — 0 blockers, 0 high, 0 medium, 1 advisory low (out-of-scope doctor copy follow-up). |
| 2 | Acceptance criteria met | PASS | All 4 ACs walked by reviewer (canonical list match, sibling list already matches, cross-reference doc-block, build/lint clean) — re-verified below. |
| 3 | Tests pass | PASS | Build + vet clean on full repo; targeted tests PASS (see below). |
| 4 | No HIGH-severity review findings open | PASS | 0 HIGH findings. The 1 LOW advisory (doctor's separate `staleDatabasePrefixes` is not converged) is explicitly out-of-scope per the bead's "Files in scope". |
| 5 | Final branch is clean | PASS | `git status` clean on `release/be-ripy` (only untracked items are `.gc/` and `.gitkeep` from worktree harness, unrelated to this change). |
| 6 | Branch diverges cleanly from main | PASS | `git cherry-pick ee67d5f9` onto `origin/main@8694c535` applied without conflict. Only one file changed (`cmd/bd/dolt.go`); no path overlap with the 32 commits on `origin/main` since the prior rebase base `f4c46d91`. |

## Test evidence (criterion 3)

Run from `release/be-ripy` @ `1014c759`:

- `go build -tags gms_pure_go ./...` — clean (exit 0).
- `go vet -tags gms_pure_go ./...` — clean (exit 0).
- `go test -tags gms_pure_go -count=1 -run 'TestStaleCommand|TestDoltClean' ./cmd/bd/` — PASS (0.089s). Includes `TestStaleCommandInit` (Docker-gated `TestStaleSuite` skipped — Docker not available in deployer harness; runs on CI).
- `go test -tags gms_pure_go -count=1 -run 'TestStaleDatabasePrefixes' ./cmd/bd/doctor/` — PASS (0.073s, 12 sub-tests). The doctor package has its own `staleDatabasePrefixes` list (out-of-scope per AD-04 bead), confirmed unaffected.

The reviewer's targeted suite (`go build`, `golangci-lint run`) was independently re-verified at the source SHA before rebase. No new code paths in this commit — the change is a constant-list edit plus a doc-block rewrite, so existing coverage applies.

## Cherry-pick mechanics (criterion 6)

This deploy follows the precedent of PR #3562 / `release/be-l9q`: the bead
covers a single commit, so the deployer cherry-picks just that commit onto
fresh `origin/main` rather than shipping the full source branch. The
reviewed SHA `0f76453f` was rebased by the builder to `ee67d5f9` after the
gate-FAIL on the prior `be-l9q` deploy attempt; the builder confirmed
content equivalence (`git diff ee67d5f9 0f76453f` shows only an unrelated
`.gitattributes` upstream-noise hunk).

Cherry-pick on top of current `origin/main` (8694c535) was conflict-free.
Touched file (`cmd/bd/dolt.go`) was not modified by any of the 32 commits
on `origin/main` since the prior rebase base, so the rebased commit's tree
applies textually.

## Cross-list convergence note (informational)

The new doc-block declares the cleanup list, the firewall list
(`internal/storage/dolt/store.go:testDatabasePrefixes`), and the city
formula list "must converge". On current `origin/main`, the firewall
list does not yet contain `benchdb_` (that addition lands with be-c5p,
not yet shipped). After this deploy:

- Cleanup list (this PR): `[testdb_, beads_test, beads_pt, beads_vr,
  doctest_, doctortest_, benchdb_]` — 7 prefixes
- Firewall list (`origin/main`): `[testdb_, beads_test, beads_pt,
  beads_vr, doctest_, doctortest_]` — 6 prefixes (missing `benchdb_`)

The two lists drift on `benchdb_` until be-c5p ships. This is a
defense-in-depth gap (firewall doesn't reject `benchdb_*` at creation
time on the production server), not a runtime regression — the cleanup
job still drops them. Not a gate blocker.

## Hand-off

- Final branch `release/be-ripy` @ `1014c759` ready to push.
- PR target: `gastownhall/beads:main` (origin), pushed via `quad341/beads`
  (fork) per the rig's push-target protocol.
