# Release Gate: be-79jh — Fix: dolt test suites open stores on the ambient shared Dolt server, not their testcontainer

**Deploy bead:** be-79jh
**Review bead:** be-ytec (verdict: pass)
**Build bead:** be-33se
**Deploy commit:** `8a3039af1459e083e71094b60251a84111203af8`
**Provenance branch:** `builder/be-33se` (NOT a push target)
**Base ref:** `origin/main` @ `7505e173f2659ba6e1f955b86d81a4f9e21810ca`
**Repo:** gastownhall/beads (contributor-only — no push/maintain/admin; gate ends at PR, no merge-request to mayor)
**Evaluated by:** beads/deployer, 2026-08-17

## Verdict: PASS — proceeding to isolated deploy branch + PR

## Criterion 6 — Clean divergence from base ref (evaluated first)
PASS. Fresh `git fetch origin main` at evaluation time: origin/main tip
`7505e173f2659ba6e1f955b86d81a4f9e21810ca` == `git merge-base origin/main <deploySHA>`.
The deploy SHA's history already contains the current origin/main tip; zero
divergence risk, no self-rebase needed.

Pre-flight already-merged check: `gh api repos/gastownhall/beads/commits/<deploySHA>/pulls`
→ `[]` (checked twice — once before, once after the evaluation below). No PR
exists yet for this SHA; normal flow applies.

## Criterion 1 — Review PASS present
PASS. be-ytec closed `close_reason: pass`; notes contain `verdict: pass` (x2),
`deploy_bead: be-79jh`, `deploy_commit: 8a3039af1...`. be-79jh's own
description states "Status: Reviewed and PASSED by beads/reviewer."

## Criterion 2 — Acceptance criteria met
PASS. be-33se's Done-when checklist (5 items), each independently checked
against the actual diff and/or a live re-run — not just the reviewer's word:

1. Both testutil start sites set BEADS_DOLT_SERVER_PORT and BEADS_DOLT_PORT —
   confirmed by direct diff read: `testdoltserver.go` sets both keys at both
   `StartIsolatedDoltContainer` (t.Setenv) and `ensureSharedContainer`
   (os.Setenv) sites.
2. SetupSharedTestDB refuses a port that disagrees with ambient
   BEADS_DOLT_SERVER_PORT — confirmed by diff read (`testdoltbranch.go`
   firewall, fires before any query) and a live run of
   `TestSetupSharedTestDB_RefusesAmbientPortMismatch` → PASS.
3. TestMain fails loudly when the resolved port isn't the container port —
   confirmed by diff read (`testmain_test.go`: builds a `Config{ServerPort:0}`,
   runs `applyConfigDefaults`, fatals if it disagrees with the container port).
4. Both new tests pass — independently run, not just trusted:
   `TestSetupSharedTestDB_RefusesAmbientPortMismatch` PASS (0.00s);
   `TestDoltContainerStartSites_SetBeadsServerPortEnv` PASS (44.76s) with both
   subtests (`StartIsolatedDoltContainer`, `EnsureDoltContainerForTestMain`)
   PASS. (Did not literally revert each fix line to re-confirm the tests
   would then fail; accepted on direct code reading — both tests assert the
   fixed behavior non-tautologically, matching the reviewer's own
   verification.)
5. Decoy-server repro creates NO database on the decoy — independently
   reproduced TWICE (not merely re-run of the reviewer's evidence) with a
   real, separate local `dolt sql-server` standing in for the ambient shared
   server: exported `BEADS_DOLT_SERVER_PORT=<decoy port>`, ran
   `go test ./internal/storage/dolt/ -run TestDoltNew_SmartRemoteMigrateGate_RealDolt`
   directly (bypassing `scripts/test.sh`'s hermetic wrapper, which would
   strip the exact env var this bug is about). Both runs: test PASS (10.10s,
   27.97s), and an `ls` diff of the decoy's data directory before/after is
   byte-for-byte identical — no new database directory appeared.

## Criterion 3 — Tests pass
PASS.

- `go build ./...` (CGO_ENABLED=1, -tags gms_pure_go): clean, exit 0.
- `go vet ./internal/storage/dolt/... ./internal/testutil/...`: clean, exit 0.
- Full `./internal/testutil/...` package sweep (not just the 2 named
  diff-owned tests — every test in the package, including
  `TestPinDockerHostFromContext_*` and `TestTempDirInMemory*`): all PASS, 0
  FAIL, 0 unexpected SKIP. `ok  .../internal/testutil  44.833s`.
- Targeted `TestDoltNew_SmartRemoteMigrateGate_RealDolt` (the specific
  end-to-end exercise of the fix against `internal/storage/dolt`): PASS x2
  under the decoy-server protocol above.

**3a — pre-existing-failure attribution (broader internal/storage/dolt sweep):**
I did not personally re-run the full ~80+-test `./internal/storage/dolt/...`
regression sweep a fifth time. The reviewer already ran it 4 times (1 scoped
+ 3 full-package attempts) and, across all of them, every failure was
root-caused to one of two causes, neither diff-owned: (a) reviewer-shell
environment gaps (ambient `BEADS_DOLT_SERVER_PORT` / no `DOCKER_HOST`) that
disappear once the shell is configured correctly — the same class of gap I
independently controlled for in my own runs — and (b) a distinct shared-
single-server capacity-exhaustion flake under heavy sequential store opens,
hitting a different test each attempt (`TestCloudAuthCLIRouting*`,
`TestCrossProject_*`), confirmed pre-existing and diff-untouched via
`git diff --stat` against the files it lands in, and filed separately as
be-1o1c (does not block this review). I independently re-verified the
specific test that exercises this diff's fix end-to-end
(`TestDoltNew_SmartRemoteMigrateGate_RealDolt`, twice, clean PASS both times)
plus a full clean sweep of the actual diff-owned package (`internal/testutil`,
0 failures). No diff-owned test skipped or failed in any run, mine or the
reviewer's. `waiver_ref`: not needed — no diff-owned SKIP or FAIL occurred.

**3b — policy/lint lane:**
`make ci-pr-policy` run in this session's gc-managed worktree at the deploy
SHA: **FAILED** — `check-versions.sh` reported
`.githooks/commit-msg: no 'BEGIN BEADS INTEGRATION' marker found`.

Root-caused, not diff-related: `.githooks/commit-msg` is **not a git-tracked
path** at either origin/main or the deploy SHA (`git show <ref>:.githooks/commit-msg`
→ "exists on disk, but not in `<ref>`" for both). The diff touches zero
version/githook files. The physical file causing the failure is a gc-rig
shim (`# gc-commit-gate-shim — installed by worktree-setup.sh (be-xug
guardrail A)`) laid down on top of this specific worktree by the agent-rig's
own session setup — not part of the beads repository at all. Proof: re-ran
`make ci-pr-policy` against the exact deploy SHA in a **second, plain**
`git worktree add` checkout with no rig tooling installed on top → 100%
clean pass, exit 0, including the openapi drift/spec-test leg
(`go test ./internal/httpapi/... -count=1` → ok, 3.575s). Attributed to
worktree-local environmental pollution, not a repo or diff defect. PASS.

Also independently ran `make ci-pr-lint` (gofmt-check + golangci-lint native +
golangci-lint windows cross-compile, scoped with
`BD_LINT_NEW_FROM_MERGE_BASE=origin/main` matching the real PR CI lane) in
the same clean worktree: **0 issues**, both lanes, exit 0. Matches the
reviewer's own finding.

`ci-pr-core` (repo-wide `-race -short` sweep) was not independently re-run:
by design (`scripts/ci/lib/test-env.sh`'s `beads_test_env_enter`) it adds
`dolt` to the hermetic skip list unless `BEADS_TEST_ENV_RUN_DOLT=1`, so it
does not meaningfully exercise this diff's dolt-backed code — the raw
`go test` runs above are the faithful check for this specific diff. `go
build`/`go vet` already independently confirm the rest of the tree compiles
clean against this change.

## Criterion 4 — No open HIGH findings
PASS. Reviewer's `style_findings`: none (gofmt/go vet clean, properly-scoped
golangci-lint 0 issues both lanes — independently reconfirmed above).
`security_findings`: none blocking — one informational, non-blocking note
about this fix's independence from a sibling fix's regression (be-9tju),
reasoned through and confirmed not exploitable/blocking. No HIGH-severity
label on be-79jh or be-ytec.

## Criterion 5 — Final branch is clean
PASS. `git status --short` at the deploy SHA (detached HEAD) shows no
modified tracked files — only 4 pre-existing untracked files in this shared
gc worktree from unrelated prior sessions (three other beads' gate files,
one stray script), none belonging to this bead. The isolated deploy branch
will be cut directly from this exact SHA with no additional changes, so it
inherits this same clean state by construction.

## Criterion 7 — Single feature theme
PASS. Read the full diff directly (not just the reviewer's characterization).
All 5 changed files serve exactly one fix (ambient `BEADS_DOLT_SERVER_PORT`
redirecting dolt test suites off their testcontainer):
`internal/testutil/testdoltserver.go` (both container-start sites now also
set `BEADS_DOLT_SERVER_PORT`), `internal/testutil/testdoltbranch.go` (new
ambient-port firewall in `SetupSharedTestDB`),
`internal/storage/dolt/testmain_test.go` (loud-fail assertion that the
resolved port matches the container), and two new test files exercising
exactly those three changes. Clean TDD red→green commit pair (`1d2bda15c` →
`8a3039af1`), both referencing be-33se. No scope creep.

## Notes / discrepancy flagged for the record
be-79jh's own description text says to name the branch `deploy/be-ytec-gate`
(the *review* bead's ID). This contradicts both the general deploy-branch
naming convention (name after the *deploy* bead being processed) and the
review bead's own structured metadata, which self-identifies
`deploy_bead: be-79jh`. Proceeding with **`deploy/be-79jh-gate`** as the
isolated branch name, consistent with the deploy bead actually being
processed and with every other gate cycle's branch name visible in this
worktree (e.g. `deploy/be-pp7e-gate`, all named for the deploy bead, not a
review bead). Flagging here rather than silently picking one.

## Next step
Repo is contributor-only for this rig (no push/maintain/admin on
gastownhall/beads) — per the merge-authority carve-out, this deployer's job
ends at opening the PR. No merge-request will be routed to mayor and no
`release-gate/deploy-clearance` commit status will be published. Proceeding
to cut `deploy/be-79jh-gate` from `8a3039af1459e083e71094b60251a84111203af8`,
push to `fork`, and open the PR.
