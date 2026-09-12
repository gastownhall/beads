# Release gate — be-9b8 (Dolt testcontainer Ryuk-reaper CI flake fix)

**Date:** 2026-08-14
**Deployer:** beads/deployer
**Bead (deploy):** be-9b8 — needs-deploy: Review: CI flake: parallel package test binaries share one Ryuk reaper; a failed handshake reaps a live Dolt container mid-suite (from:be-vwb)
**Review bead:** be-vwb — closed, review verdict PASS
**Build bead:** be-2on
**Commit:** `03dfbdfb6d628939bf81276e2dbf4cdd3c22de6e` (tdd_green; tdd_red `87e538862f0ab998b177794f858bb9d325f3a984`)
**Source branch:** `builder/be-2on` — provenance only, never pushed to or used as a PR head
**Branch:** `deploy/be-9b8-gate` (isolated, cut fresh at the reviewed SHA)
**Base:** `origin/main` @ `185b339be` ("fix(wisp): purgeClosed must not cascade into live molecule steps (#5735)")
**Merge-base:** `d1e725d9f` ("fix(reclaim): correct the heartbeat re-home invariant's scope in the new docs (wy-sp2l4)")
**Merge-tree simulation:** `git merge-tree d1e725d9f origin/main 03dfbdfb6d628939bf81276e2dbf4cdd3c22de6e` → no conflict markers, **zero conflicts**

## Verdict: PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-vwb closed, verdict: pass. Zero blocker/major/minor findings across style, security (9-point OWASP-style checklist), and spec. |
| 2 | Acceptance criteria met | PASS | All 3 be-2on "done-when" items verified (see Acceptance check below). |
| 3 | Tests pass | PASS | Independently reran the CI-faithful command on `deploy/be-9b8-gate` (commit `03dfbdfb6d6`): `GOFLAGS=-tags=gms_pure_go CGO_ENABLED=1 go test -race -short -skip '^TestEmbedded' -v ./scripts/...` → 0 FAIL, 1 SKIP, 126/126 leaf results (RUN count matches PASS+SKIP exactly). Diff-owned `TestDoltTestcontainerStepsDisableRyuk` and all 5 named subtests PASS. Matches reviewer's reported 60 PASS / 0 FAIL / 1 SKIP at top-level-test granularity. |
| 4 | No open HIGH findings | PASS | Reviewer notes record zero HIGH/blocker findings (security_findings: none; style_findings: none). |
| 5 | Final branch is clean | PASS | `deploy/be-9b8-gate` cut fresh from the single reviewed commit; only addition is this gate file. |
| 6 | Branch diverges cleanly from main | PASS | Zero file-path overlap between the reviewed commit's changes (`.github/workflows/main.yml`, `.github/workflows/pr.yml`, `scripts/ci_workflow_test.go`) and origin/main's commits since merge-base (`cmd/bd/list*.go`, `cmd/bd/wisp*.go`). `git merge-tree` three-way check: no conflicts. |
| 7 | Single feature theme | PASS | One theme: pin `TESTCONTAINERS_RYUK_DISABLED: "true"` on the 5 CI step-instances that start Dolt testcontainers from a package `TestMain`, so testcontainers-go's single shared Ryuk reaper can't reap a sibling process's live container when multiple such binaries run concurrently in one step. |

## Acceptance check (be-2on "done-when")

1. **`TESTCONTAINERS_RYUK_DISABLED` set on every Dolt-testcontainer CI step in both workflow files.**
   - Independently confirmed against the reviewed commit's tree (not just the diff): `git show 03dfbdfb6d:.github/workflows/main.yml | grep -n TESTCONTAINERS_RYUK_DISABLED` → lines 557, 675, 687. Same for `pr.yml` → lines 783, 795. 5/5, matching the reviewer's claimed locations exactly.
   - **PASS.**
2. **Diff-owned test proves every container-using step is covered.**
   - `scripts/ci_workflow_test.go`'s `TestDoltTestcontainerStepsDisableRyuk` (added by the red commit, table-driven, reuses existing `readCIWorkflow`/job/`assertStepEnvValue` helpers) — all 5 named subtests independently reran and PASS: `pr.yml/test-domain-uow` x2, `main.yml/test-domain-uow` x2, `main.yml/test` (coverage leg) x1.
   - **PASS.**
3. **Live PR CI run shows the job green with no Reaper-related failure line.**
   - Builder's own exit_contract correctly discloses this can't be proven from an isolated local worktree and defers it to post-push/deploy-gate verification — not silently skipped. This deploy's PR open is exactly that verification point; noted in Hand-off below as the one item that completes after push.
   - **Appropriately deferred, not a blocker.**

## Security note (carried from review, independently spot-checked)

The one item warranting scrutiny — disabling Ryuk's cleanup guarantee — only holds if the touched steps run on ephemeral, non-self-hosted runners (otherwise orphaned containers could accumulate on a shared host). Spot-checked: `main.yml` job `test` (`runs-on: ${{ matrix.os }}`, matrix `[ubuntu-latest, macos-latest]`) and both files' `test-domain-uow` (`runs-on: ubuntu-latest`) are all GitHub-hosted. No self-hosted runner in scope. Matches the reviewer's finding.

## Hand-off

- Push: `deploy/be-9b8-gate` → `fork` (`quad341/beads.git`) — re-verified current precedent via `git push --dry-run origin HEAD` (origin push URL is the deliberately disabled `DISABLED-upstream-is-fetch-only-push-to-fork-and-PR` placeholder in this workspace) and `git push --dry-run fork HEAD` (succeeds).
- PR: cross-repo `quad341:deploy/be-9b8-gate` → `gastownhall:main`.
- `gastownhall/beads` is a repo we contribute to, not maintain: job ends at PR open. No merge-request to mayor, no deploy-clearance commit status, no waiting on a reply. PR body will note that item 3 above (live CI green, no Reaper failure) completes once GitHub Actions runs on the opened PR.
