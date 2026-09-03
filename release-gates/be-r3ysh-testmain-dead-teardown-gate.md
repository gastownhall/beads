# Release gate — TestMain teardown was dead code after `os.Exit` (be-r3ysh)

- **Deploy bead:** be-r3ysh (needs-deploy, routed beads/deployer)
- **Build bead:** be-5kkk6 — tdd_red `85e9093599629a7262e613e8e223b04c564a5cf8`, tdd_green `9e4037916a9a0adae423e5a807c8f22c946e90f1`
- **Review bead:** be-43oyc — verdict **PASS**, closed 2026-08-20T06:12:56Z
- **Commit deployed:** `9e4037916a9a0adae423e5a807c8f22c946e90f1` (deploy source; branch cut from exactly this SHA)
- **Source branch:** `builder/be-5kkk6` — provenance only, never a push target
- **Deploy branch:** `deploy/be-r3ysh-gate`, derived mechanically via `resolve_deploy_branch_target` (the bead's own prose named `deploy/be-cqmfa-gate`, a stale/mismatched id from a different bead — not used)
- **Push target:** `headfork` (`quad341/beads-sec003-contrib.git`) — `origin` push is disabled by design on this rig (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`)
- **PR:** https://github.com/gastownhall/beads/pull/5876 (OPEN, base `main`, head `quad341:deploy/be-r3ysh-gate`, mergeable=MERGEABLE)
- **Evaluated:** 2026-08-20 by beads/deployer

## Scope

Three `TestMain` functions (`beads_test.go`, `cmd/bd/doctor/fix/testmain_cgo_test.go`,
`tests/regression/regression_test.go`) called `os.Exit(N)` directly inside
their own body, after `defer TerminateDoltContainer(...)`. A deferred call
never runs when the enclosing function exits via `os.Exit`, so container
teardown was dead code on every exit path. Over 6 days this leaked 101 dolt
containers and exhausted swap (be-43oyc).

Fix introduces a `testMainInner` pattern in all three files: every
`os.Exit(N)` call site (all 4 in `regression_test.go`, not just the ones on
the leak path) becomes `return N` inside `testMainInner`; the outer
`TestMain` calls `os.Exit(testMainInner(m))`, so `defer` registered before
`m.Run()` now executes on every path. No behavior change beyond the function
split (env vars, error messages, exit codes, control-flow order preserved —
confirmed by diff read).

Adds a new regression guard, `test/testmainconvention/testmain_convention_test.go`:
an AST walk that fails if any `TestMain` calls `TerminateDoltContainer` via
`defer` and then exits the same function without routing through an inner
function. Matches by method name (alias-proof); correctly excludes nested
closures so an unrelated defer/exit can't false-positive.

Single feature theme, single build bead (be-5kkk6) — both commits in the
deploy range cite it, confirmed by `assert_deploy_ancestry_scope`.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 0 | Already merged? (pre-flight) | **NO** | `git merge-base --is-ancestor 9e40379 origin/main` → not an ancestor; `gh pr list --search "be-r3ysh OR be-43oyc OR 9e4037916"` → no existing PR. Proceeded. |
| 1 | Review PASS present | **PASS** | be-43oyc, `verdict: pass`, closed 2026-08-20T06:12:56Z, close_reason `pass`. |
| 2 | Acceptance criteria met | **PASS** | All 3 exit_contract criteria from be-5kkk6 independently verified in be-43oyc: (1) pattern applied correctly across all 3 files including all 4 exit sites in `regression_test.go`; (2) no behavior change beyond the split, confirmed by diff read; (3) RED commit independently checked out to a throwaway worktree and confirmed to fail citing exactly the 3 affected files with no false positives on 8 other pre-existing-correct `TestMain` files. |
| 3 | Tests pass (diff-owned-SKIP=FAIL rule) | **PASS** | 259 PASS / 0 FAIL / 18 SKIP across all 4 diff-owned files, executed on real Dolt-container infrastructure (rootless podman, container lifecycle create→start→stop→terminate logged cleanly — direct behavioral proof the defer now runs). All 18 SKIPs are pre-existing and individually attributed: named by test, cited with a GH#/bd- issue ref or explicit "intentional change" rationale, confirmed unrelated to `TestMain`/defer/`os.Exit`, confirmed present before this diff. Zero diff-owned SKIPs. Independently re-verified by deployer on the cut branch: `gofmt -l` (4 diff-owned files) clean, `go build ./...` clean, `go vet ./...` clean, `go vet -tags=regression ./tests/regression/...` clean, `go vet -tags=cgo ./cmd/bd/doctor/fix/...` clean. |
| 4 | Policy / lint lane | **PASS** | `golangci-lint run --new-from-merge-base=origin/main` clean (default tags, `regression` tag, `cgo` tag) — 0 issues across all 3 scopes, per be-43oyc. |
| 5 | No open HIGH findings | **PASS** | be-43oyc: zero security findings (explicit OWASP Top 10 walk — test-only diff, zero production files touched, no injection/auth/data-exposure/config surface). Zero style findings; nothing blocking. |
| 6 | Clean branch status / clean divergence from main | **PASS** | `git merge-tree --write-tree origin/main 9e40379` exits 0 with a single resulting tree, no conflict markers — clean merge despite the reviewed SHA's base sitting 14 commits behind current `origin/main` tip (2 commits ahead of merge-base). No self-rebase needed; bounded self-rebase exception not invoked. `assert_deploy_ancestry_scope origin/main 9e40379 be-r3ysh be-5kkk6` → rc=0 (no `.claude/**` paths introduced; both commits in range cite `be-5kkk6`). `assert_safe_push_target deploy/be-r3ysh-gate` → rc=0 (not a shared worktree branch). |
| 7 | Single feature theme | **PASS** | One coherent fix (TestMain dead-teardown bug) across 3 files + 1 new guard test; both commits (red/green) cite the same build bead be-5kkk6; ancestry-scope check found no stray commits. |

## Tests run by deployer on the cut branch (independent of review)

| Check | Result |
|---|---|
| `gofmt -l beads_test.go cmd/bd/doctor/fix/testmain_cgo_test.go tests/regression/regression_test.go test/testmainconvention/testmain_convention_test.go` | clean (no output) |
| `go build ./...` | clean, exit 0 |
| `go vet ./...` | clean, exit 0 |
| `go vet -tags=regression ./tests/regression/...` | clean, exit 0 |
| `go vet -tags=cgo ./cmd/bd/doctor/fix/...` | clean, exit 0 |

Full container-backed test execution (259 PASS / 0 FAIL / 18 pre-existing
SKIP) was not re-run by the deployer — already performed by the reviewer
with real CI-equivalent commands and logged evidence (be-43oyc); PR CI on
#5876 provides the additional independent real-world confirmation.

## Push target

`origin` (`gastownhall/beads`) denies push (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`
sentinel); `headfork` (`quad341/beads-sec003-contrib.git`) accepts, per
established precedent on this rig. PR opens cross-repo against
`gastownhall/beads:main` with head `quad341:deploy/be-r3ysh-gate`.

## Merge authority

`gastownhall/beads` is a contributor-only repo for this rig — no rig agent
has merge access. Per established precedent (be-vc1m, be-gd3v, be-79jh,
be-39ss, be-pp7e), the deployer's job ends at the open, verified PR. No
merge-request is routed to mayor/mpr.

## Verdict

**PASS 7/7** — branch cut, build verified independently, PR opened and
confirmed OPEN/MERGEABLE. Standing down; PR CI will provide further
real-world confirmation but is not a precondition for this gate (unlike
be-vc1m, this bead carries no hard-precondition blocking criterion 3 on a
fresh CI result — the review's evidence was obtained by real execution, not
a SKIP).
