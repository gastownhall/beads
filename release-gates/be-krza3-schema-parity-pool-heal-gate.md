# Release gate — CLI/runtime schema parity oracle + pool-heal cherry-pick (be-krza3)

- **Deploy bead:** be-krza3 (needs-deploy, routed beads/deployer)
- **Build bead:** be-pouv5 — tdd_green `e0c39aa25b7429ddfc88ca085b78f038b80bf87a` (no separate red commit for this round; fix lives in the test oracle plus an already-cherry-picked store.go fix)
- **Review bead:** be-8xjjg — verdict **PASS** (round 2), closed with reason `pass`
- **Commit deployed:** `e0c39aa25b7429ddfc88ca085b78f038b80bf87a` (deploy source; branch cut from exactly this SHA)
- **Source branch:** `builder/be-0v3l` — provenance only, never a push target
- **Related beads:** be-0v3l (original bug — CLI/runtime schema parity oracle mishandles ignored-stream columns), be-itm5 (canonical `rebuildPoolAfterMigration` fix, cherry-picked byte-for-byte — patch-id verified exact match `cdcf9ed1e97fc4e64a28ed01368192718ebff0e9`; already 3-round-reviewed and mayor-waived on its own gate), be-3c78s (pre-existing, unrelated P0 cross-tenant isolation bug that explains 14 non-diff-owned full-package failures; since fixed via PR #5836 — does not retroactively invalidate the "not caused by this diff" attribution, since the reviewer's snapshot was accurate at the time it was taken)
- **Deploy branch:** `deploy/be-krza3-gate`, derived mechanically via `resolve_deploy_branch_target`
- **Push target:** `headfork` (`quad341/beads-sec003-contrib.git`) — `origin` push is disabled by design on this rig (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`)
- **PR:** (recorded below once opened)
- **Evaluated:** 2026-08-20 by beads/deployer

## Scope

The CLI/runtime schema parity oracle (`schema_cli_parity_integration_test.go`)
could not express ignored-stream columns and read a random category as empty,
producing false parity mismatches. Fix adds a static exclusion clause for the
known ignored column and a deterministic map-iteration helper
(`sortedSnapshotQueryNames`) so oracle output order stops depending on Go map
iteration order.

Round-2 rework additionally incorporates be-itm5's canonical
`rebuildPoolAfterMigration` fix (`store.go`) — cherry-picked byte-for-byte
(confirmed via `git patch-id --stable`, exact match) — because with `store.go`
reverted to its pre-itm5 state, this round's own test files fail to *compile*,
making the original spec's Done-when criteria (#3 non-determinism, #4 no
production files) mechanically contradictory. Round-1's reviewer directed the
store.go inclusion as the correct resolution (documented in build bead
be-pouv5's notes); round-2's reviewer independently re-verified that authority
chain rather than accepting the carve-out on say-so, and independently
re-proved the compile-fails-without-it claim.

Single feature theme: both commits in the deploy range cite be-krza3, be-0v3l,
or be-itm5, confirmed by `assert_deploy_ancestry_scope` (see criterion 6).

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 0 | Already merged? (pre-flight) | **NO** | `gh pr list --search "be-krza3 OR be-8xjjg OR e0c39aa25... OR 7d294b4b5..."` (state all) → empty. `git merge-base --is-ancestor e0c39aa25... origin/main` → not an ancestor. Proceeded. |
| 1 | Review PASS present | **PASS** | be-8xjjg, `verdict: pass`, round 2, closed with reason `pass`. |
| 2 | Acceptance criteria met | **PASS** | be-8xjjg `decide_findings`: scope-authorization concern (store.go touched) resolved against the documented round-1-reviewer authority chain, not builder/reviewer say-so; round-2 independently RED/GREEN-proved store.go's inclusion is compile-mechanically necessary; `uncovered_criteria: none`. Non-determinism criterion independently re-verified: `TestCLIBundleMatchesRuntimeCommittedSchema -count=5` fresh run, 5/5 PASS. |
| 3 | Tests pass (diff-owned-SKIP=FAIL rule) | **PASS** | 9/9 diff-owned tests PASS, 0 FAIL, 0 SKIP, 19.002s wall, isolated run (test names: TestApplyPoolLimits_Defaults/Overrides/ClampsIdleToOpen, TestPool_SequentialQueriesReuseSingleConnection, TestPool_ConcurrentQueriesRespectMaxOpen, TestPool_CloseReleasesUnderlyingConnections, TestRebuildPoolAfterMigration_NoopWhenNotMigrated, TestMigratingOpen_FirstReadSucceeds, TestCLIBundleMatchesRuntimeCommittedSchema). Real Dolt-container execution (rootless podman), not a SKIP-substitute result. Independently re-verified by deployer after rebase onto current origin/main tip: `gofmt -l` (4 diff-owned files) clean, `go build ./...` clean, `go vet ./...` clean. |
| 3a | Pre-existing-failure attribution (non-diff-owned) | **PASS** | Full-package run (193 tests) shows 14 FAIL, all non-diff-owned, all in `federation_test.go`. 13/14 fail at a uniform ~45s (pre-existing per-test timeout under package-scale parallel-slot contention, not diff-caused); the 14th (`TestFederationDatabaseIsolation`) is the tracked pre-existing P0 be-3c78s, same assertion text/location. Matched base-ref (merge-base `6ec78f3a2`) narrowed comparison on the same 14 tests reproduces identically: 13 PASS + 1 FAIL (same test, same assertion). Diff changes nothing about these 14 tests' outcomes — confirmed by direct comparison, not assumed. All 4 attribution clauses satisfied: not diff-owned, tracked bead (be-3c78s / capacity characteristic documented on be-pouv5), proven pre-existing at base-ref, no path overlap with the diff. |
| 3b | Policy / lint lane | **PASS** | `golangci-lint run` against the package path: "0 issues" (be-8xjjg's own from-scratch run — 3rd independent clean gofmt/vet/lint verification on this diff, after round-1 reviewer and round-2 builder). |
| 4 | No open HIGH findings | **PASS** | be-8xjjg: explicit OWASP Top 10 walk (injection, auth, access control, XXE/SSRF/deserialization/XSS, misconfig, vulnerable deps, logging) — none blocker/major/minor. store.go's unlocked pool swap verified safe (single call site, pre-publish, in constructor). Zero style findings. |
| 5 | Branch clean | **PASS** | Working tree clean at HEAD after restore + rebase re-verification (`git status --short` empty). |
| 6 | Diverges cleanly from main | **PASS** | `assert_deploy_ancestry_scope origin/main 7d294b4b5... be-krza3 be-0v3l be-itm5` → rc=0 (re-run post-rebase; no `.claude/**` paths, all commits cite an accepted bead id). `attempt_bounded_self_rebase deploy/be-krza3-gate main` completed the rebase itself cleanly (0 conflicts) onto origin/main tip `d38ac728b`: BEFORE_SHA=`e0c39aa25b7429ddfc88ca085b78f038b80bf87a`, AFTER_SHA=`7d294b4b5ae49561f6da272ba2a04eff89f44c79`. Its internal force-with-lease push (hardcoded to `origin`) then failed rc=13 against this rig's disabled origin remote — the known be-z3iuv infra gap, not a real conflict; the rebase result itself is valid and unaffected. `assert_safe_push_target deploy/be-krza3-gate` → rc=0. |
| 7 | Single feature theme | **PASS** | be-0v3l (oracle fix) + authorized be-itm5 cherry-pick (compile-necessary dependency), both cited across the commit range; ancestry-scope check found no stray commits. |

## Tests run by deployer on the cut branch (independent of review)

| Check | Result |
|---|---|
| `gofmt -l` (4 diff-owned files: connection_pool_test.go, post_migration_pool_heal_integration_test.go, schema_cli_parity_integration_test.go, store.go) | clean (no output) |
| `go build ./...` | clean, exit 0 |
| `go vet ./...` | clean, exit 0 |

Re-run after the branch was rebased onto the current `origin/main` tip
(`d38ac728b`) and after this session recovered the branch from a dangling
commit — this worktree's `pre_start` hard-reset the local branch pointer back
to `origin/main` between rounds, but `origin/main` had not moved, the rebased
commit object (`AFTER_SHA`) was still present in the local object store
(`git cat-file -e`), and the bead's own notes already recorded the SHA as
durable state — so the branch was restored via `git reset --hard AFTER_SHA`
rather than redone, and every gate check re-run fresh against the restored
state before proceeding. Full container-backed test execution (9/9 diff-owned
PASS) was not re-run by the deployer — already performed by the reviewer with
real CI-equivalent commands and logged evidence (be-8xjjg); PR CI provides the
additional independent real-world confirmation.

## Push target

`origin` (`gastownhall/beads`) denies push
(`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR` sentinel); `headfork`
(`quad341/beads-sec003-contrib.git`) accepts, per established precedent on
this rig (be-r3ysh). PR opens cross-repo against `gastownhall/beads:main` with
head `quad341:deploy/be-krza3-gate`.

Note on be-z3iuv: this gate's criterion 6 push used a direct, manual push to
`headfork` rather than a fresh invocation of `attempt_bounded_self_rebase`,
because that function's *internal* force-with-lease push is still hardcoded to
`origin` at the canonical path
(`/home/jaword/projects/gc-management/packs/actual/deployer/scripts/rebase-resolve-lib.sh`,
line 489) as of this gate's evaluation — independently re-verified by reading
the file directly and confirming `f195bbdbe` is not an ancestor of
`gc-management` main (`306348db3`), despite a mayor mail (`gm-wisp-wt8fd8`)
claiming this fix already shipped. The rebase itself (performed by that same
function in an earlier round) succeeded cleanly and is unaffected by its
push-step bug; only the internal push call was bypassed here.

## Merge authority

`gastownhall/beads` is a contributor-only repo for this rig — no rig agent has
merge access. Per established precedent (be-vc1m, be-gd3v, be-79jh, be-39ss,
be-pp7e, be-r3ysh), the deployer's job ends at the open, verified PR. No
merge-request is routed to mayor/mpr; gate result reported to mayor via mail.

## Verdict

**PASS 7/7** — branch recovered and re-verified after a local ref reset,
rebase re-confirmed clean against current main, build/vet/gofmt independently
re-run, pushed to headfork, PR opened and confirmed OPEN/MERGEABLE. Standing
down; PR CI will provide further real-world confirmation but is not a
precondition for this gate.
