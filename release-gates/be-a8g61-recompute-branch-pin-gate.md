# Release Gate: be-a8g61 — recomputeAllBlocked/recomputeBlockedTx repaired the wrong branch on a fresh connection

- **Deploy bead:** be-a8g61
- **Review bead:** be-ahog (closed, verdict: pass)
- **Build bead:** be-b0am
- **Repo:** gastownhall/beads
- **Deploy commit:** `929a5c347e2a730835bfb790e00f151f2a81b0dd`
- **Deploy branch:** `deploy/be-a8g61-gate` (cut from the commit above, pushed to `headfork` = quad341/beads-sec003-contrib)
- **Base:** origin/main @ `ed382cbdb89cf7ba42b020e4927575dbf27e102e`

## Pre-flight: already-merged check

`gh api repos/gastownhall/beads/commits/929a5c347.../pulls` — no PR references this commit. Clean.

## Ancestry scope (assert_deploy_ancestry_scope)

Base→deploy range: 2 commits, both cite be-b0am, neither touches `.claude/**`. rc=0.

## Gate criteria

1. **Review PASS present** — PASS. be-ahog status=closed, close_reason=pass, verdict:pass in notes; deploy_bead/deploy_commit metadata match be-a8g61 exactly.
2. **Acceptance criteria met** — PASS. All 6 of be-b0am's Done-when items individually cross-checked by the reviewer against the diff; no uncovered criteria recorded.
3. **Tests pass** — PASS. Reviewer's documented command, independently re-run by me on a detached checkout of the reviewed SHA (fresh testcontainers lifecycle, distinct container/session id from the reviewer's run):

   ```
   DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true \
   go test -tags=integration,gms_pure_go -run '^(TestMergeRecomputesIsBlocked|TestRecomputeAllBlocked_RefusesDirtyIssues|TestRecomputeAllBlocked_RefusesDirtyDependencies|TestRecomputeAllBlocked_AllowsDirtyWisps|TestRecomputeAllBlocked_RepairsAndCommitsWhenClean|TestPinStoreBranch_ReproducesStoreActiveBranch)$' \
   -v ./internal/storage/dolt/...
   ```
   Result: 6 PASS, 0 FAIL, 0 SKIP in 18.919s. Diff-owned test (`TestPinStoreBranch_ReproducesStoreActiveBranch`, the only test file touched by this diff) confirmed genuinely red pre-fix (compile-red) by the reviewer. `waiver_ref: none`.
3a. **Pre-existing-failure attribution** — N/A for the test lane (no failures to attribute); used for 3b below.
3b. **Policy/lint lane** — PASS, with one attributed pre-existing failure.
    - `make ci-pr-policy` on the reviewed SHA as checked out initially failed:
      `.githooks/commit-msg: no 'BEGIN/END BEADS INTEGRATION' marker found` + a resulting version-mismatch report from `scripts/check-versions.sh`.
    - Attribution, all 4 clauses of `non-diff-owned-gate-failure`:
      - *Not diff-owned*: `git diff --name-only origin/main...929a5c347e2a730835bfb790e00f151f2a81b0dd -- .githooks/` is empty — the diff never touches this path.
      - *No path overlap*: `.githooks/` vs. the diff's `internal/storage/dolt/**` — disjoint.
      - *Tracked bead id*: be-jygq (closed) root-caused this: `.githooks/commit-msg` is not tracked in git anywhere (`git log --all -- .githooks/commit-msg` and `git ls-tree origin/main -- .githooks/commit-msg` both empty) and is listed in this worktree's `.git/info/exclude`. It is a local gc-rig session shim rewritten by `worktree-setup.sh` on every session start; `scripts/check-versions.sh` globs `.githooks/*` on the raw filesystem rather than via `git ls-tree`, so it only surfaces inside a gc-managed worktree. Independently re-confirmed this round (`git log --all` / `git ls-tree` / `.git/info/exclude` all checked directly, not just cited from the bead). be-2q84 (open) and be-jy56 (open) are later duplicate filings of the same symptom that predate/don't incorporate be-jygq's correction.
      - *Proven pre-existing at BASE_REF*: stronger than the usual case — this artifact isn't part of any commit's tree at all, so it reproduces identically regardless of which SHA is checked out in a gc-managed worktree. Verified directly: moved the untracked shim aside (reversible; git-ignored, so this is not a working-tree mutation of anything tracked) and re-ran `make ci-pr-policy` end-to-end — exit 0, every one of the 10 sub-checks (build-tag policy, go-install guidance, version consistency, docs binary build, doc-flags, doc-freshness, testing.Short boundaries, workapi frontend boundary, no-beads-jsonl-changes, openapi spec gate) passed cleanly. Shim restored immediately after.
    - `BD_LINT_NEW_FROM_MERGE_BASE=origin/main make ci-pr-lint`: gofmt clean, golangci-lint 0 issues, golangci-lint (windows) 0 issues.
4. **No open HIGH findings** — PASS. be-ahog's security_findings and style_findings are both "none" — zero findings of any severity, not just zero HIGH.
5. **Clean working tree** — PASS. `git status --porcelain=v1` on the deploy branch shows only pre-existing, already-triaged harmless untracked leftovers from unrelated closed beads (release-gates/be-hi97, be-k9js, be-uoat gate files; scripts/rebase-resolve-lib.sh — see memory `deployer-worktree-stray-rebase-lib-copy`), none of which this deploy branch's commits touch.
6. **Clean divergence from base** (checked first) — PASS. `git log origin/main..929a5c347e2a730835bfb790e00f151f2a81b0dd --oneline`: exactly 2 commits. `git log 929a5c347e2a730835bfb790e00f151f2a81b0dd..origin/main --oneline`: 0 commits. origin/main is a direct ancestor; clean fast-forward extension, no rebase needed.
7. **Single-feature theme** — PASS. Two commits (red test, green fix), two files (`internal/storage/dolt/federation.go` +7, `internal/storage/dolt/store.go` +104/-24), one coherent fix: a `pinStoreBranch` helper shared by `recomputeAllBlocked`/`recomputeBlockedTx`/the original caller, so all three reproduce the store's actual branch checkout instead of silently repairing whichever branch a fresh connection happens to default to. The build bead audited other fresh-connection call sites, found one further latent hazard (`pullWithAutoResolveUnchecked`, federation.go:126), and correctly deferred it to its own bead (be-5ybd) rather than bundling it in.

## Result: PASS

Merge authority: gastownhall/beads is a contributor-only repo (we do not maintain it). Deployer's job ends at the opened PR; no merge-request routed to mayor. Upstream maintainers own the merge.
