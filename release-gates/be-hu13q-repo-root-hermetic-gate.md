# Release gate — be-hu13q (bound findBeadsRepoRoot's ancestor walk to the git repo root)

**Date:** 2026-09-15
**Deployer:** beads/deployer
**Bead (deploy):** be-s4x0d — needs-deploy: "Deploy: Make TestFindBeadsRepoRoot_WorktreeFallback hermetic (from:be-hu13q)"
**Review bead:** be-hu13q — closed, review verdict PASS
**Build bead:** be-9x3y2 — closed, work outcome blocked (pending review/deploy at close time)
**Pre-existing-failure tracker:** be-ey0oy — open, "TestFindBeadsRepoRoot_WorktreeFallback" cluster; this PR is the permanent code fix (mayor's earlier removal of the leaked `/tmp/.beads` was an environmental band-aid, not a fix)
**RED commit:** `8612b80df5221fea411d952a4c2b1e1ccb1b3d7a`
**GREEN / reviewed / DEPLOY_SHA:** `ec18b592d75fb6bd7928b7c8ee805b31832bf2ac`
**Merge-base:** `f56632adcfabed7da6ed0aabe4e760066b472c46` ("perf(storage): stop burning two wasted MySQL sessions per bd invocation (wy-s8ytnw) (#6122)")
**Scope:** `cmd/bd/config.go` + `cmd/bd/config_worktree_test.go` only; no Postgres/pack material.

## Verdict: PASS — all 7 criteria (+3 sub-criteria) clear

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Reviewer PASS verdict present | PASS | be-hu13q closed, verdict PASS: "Full-suite make test green (97/97 ok, 0 FAIL, 0 skips), both diff-owned tests confirmed PASS by name, all 3 acceptance criteria independently verified, zero security/style blockers." Independently re-verified this session against be-hu13q's own notes, not taken at face value. |
| 2 | Acceptance criteria met | PASS | Build bead be-9x3y2's 3 ACs, independently re-verified against the diff and builder/reviewer notes this session: (a) hermetic test added for the leaked-ancestor `/tmp/.beads` case — `TestFindBeadsRepoRoot_IgnoresLeakedAncestorBeadsDir`; (b) the `HOME=/tmp` writer was searched exhaustively across every `HOME=`-setting call site in both the beads repo and the orchestrating gc-management repo's test harnesses, none found to set a bare `/tmp`, and this is stated explicitly per the bead's own fallback instruction rather than guessed further; (c) optional XDG telemetry-storage hardening considered and explicitly deferred as its own out-of-scope migration. |
| 3 | Tests pass (full-suite, documented command) | PASS | `make test` (→ `TEST_COVER=1 ./scripts/test.sh`, true full scope `./...`, 97 packages): 97/97 packages `ok`, 0 FAIL, 0 skips, per reviewer evidence. Independently spot-re-ran the 3 relevant tests by exact name this session at DEPLOY_SHA: `TestFindBeadsRepoRoot_WorktreeFallback`, `TestFindBeadsRepoRoot_IgnoresLeakedAncestorBeadsDir`, `TestBeadsPollutionCheck_WorktreeSkips` — all 3 PASS (`ok  	github.com/steveyegge/beads/cmd/bd	0.213s`). |
| 4 | No HIGH-severity findings open | PASS | be-hu13q notes: full 9-category OWASP walk plus a DoS/test-hygiene check against the diff — zero blocker/major/minor findings anywhere. |
| 5 | Feature branch clean | PASS | `git diff --stat` at DEPLOY_SHA against merge-base is exactly 2 files, +108/-0, matching the reviewed diff exactly; no uncommitted or extraneous changes. |
| 6 | Clean divergence from BASE_REF | PASS | `origin/main` unchanged at `f56632adc` since this bead was authored (re-fetched and re-checked this session). DEPLOY_SHA is not an ancestor of `origin/main` (pre-flight already-merged check: not yet merged — proceeding normally, no reconciliation needed). No rebase required. |
| 7 | Single feature theme | PASS | Exactly 2 commits (RED `8612b80df`, GREEN `ec18b592d`); one narrowly-scoped hermeticity fix plus its regression test; no `rollup-ship` label. |

### 3a — pre-existing-failure attribution: N/A
The full suite is clean (0 FAIL) at DEPLOY_SHA. Nothing needs attribution — this diff is itself the permanent fix for the previously-tracked flake (be-ey0oy), superseding the environmental band-aid applied for the earlier be-fghsd gate.

### 3b — policy/lint lane: PASS
be-hu13q notes: `gofmt -l cmd/bd/config.go cmd/bd/config_worktree_test.go` empty; `go vet ./cmd/bd/...` exit 0. One non-blocking nit (a bead-ID citation inside a code comment) — informational only, not a blocker.

### 3c — CI-config-diff-needs-own-run: N/A
No `.github/workflows/*` or other CI-config files are touched by this diff (confirmed via `git diff --stat`).

## Pre-flight already-merged check
DEPLOY_SHA `ec18b592d` is **not** an ancestor of `origin/main` (`git merge-base --is-ancestor` re-checked this session). No open/closed PR existed for this bead prior to this gate. Proceeding to open a fresh PR.

## Hand-off
Gate PASSES. `gastownhall/beads` is a contributor-only upstream (not a repo this rig maintains) — cutting `deploy/be-hu13q-gate` at DEPLOY_SHA, pushing to `headfork`, and opening a PR against `gastownhall/beads` `main`. Per precedent (be-fghsd / PR #6571) and the deployer role-prompt's contributor-only-repo step: the job ends at the open PR — no merge-request bead is routed to the mayor, no deploy-clearance status is posted. `be-s4x0d` will be closed noting the PR URL, `gc.work_outcome=blocked` (real work exists and is pushed/PR-opened, but nothing has landed on any ref this rig controls; merge is upstream maintainers' call).
