# Release gate — store-requiring path ignores BEADS_DB/BD_DB (be-34u9a)

- **Deploy bead:** be-34u9a (needs-deploy, routed beads/deployer)
- **Build bead:** be-git2o — tdd_red `f27d0da79855fb8860b3fa302bbceb4875d9d269`, tdd_green `c3e3a7f87536b2c630e10c594c427ff493e1c670`
- **Review bead:** be-i155n — verdict **PASS**, closed with reason `pass`
- **Commit deployed:** `43e2065da07fb6036a6d1ef33f407b63ff5e10b5` (branch tip; a CHANGELOG-only commit over the reviewed `tdd_green` `c3e3a7f87536b2c630e10c594c427ff493e1c670` — ancestry and delta confirmed, see criterion 6)
- **Source branch:** `builder/be-git2o` — provenance only, never a push target
- **Deploy branch:** `deploy/be-34u9a-gate`, cut fresh at exactly `43e2065da07fb6036a6d1ef33f407b63ff5e10b5`
- **Push target:** `headfork` (`quad341/beads-sec003-contrib.git`) — `origin` push is disabled by design on this rig (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`)
- **PR:** (recorded below once opened)
- **Evaluated:** 2026-09-03 by beads/deployer

## Scope

`bd`'s store-requiring command path (`list`, `show`, `create`, `update`, ...)
ignored an explicit `BEADS_DB`/`BD_DB` target and silently opened whichever
database ambient workspace discovery resolved to instead. The no-DB path
(`selectedNoDBBeadsDir`) already honored both variables; `beads.FindDatabasePath()`
takes its `BEADS_DIR` branch first and returns early once `BEADS_DIR` is set,
but the store-requiring path ran `prepareSelectedCommandContext` (which sets
`BEADS_DIR` as a side effect) before ever consulting `BEADS_DB`/`BD_DB` — so
`bd where` and `bd list` could disagree about which workspace was selected,
with no error or warning. The fix resolves `BEADS_DB`/`BD_DB` on the
store-requiring path the same way the no-DB path already does, before ambient
discovery runs. Single feature theme throughout: all 3 commits in the deploy
range cite be-git2o.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 0 | Already merged? (pre-flight) | **NO** | `gh pr list --repo gastownhall/beads --search "be-34u9a OR be-i155n OR be-git2o OR 43e2065da OR c3e3a7f87" --state all` → `[]`, empty. `git merge-base --is-ancestor 43e2065da... origin/main` → not an ancestor. Proceeded. |
| 1 | Review PASS present | **PASS** | be-i155n: `verdict: pass`, closed with reason `pass`. `style_findings: none` (gofmt/vet clean, idiomatic). `security_findings: none` — full 9-category OWASP-style walk, each category explicitly reasoned as N/A or clean rather than omitted. |
| 2 | Acceptance criteria met | **PASS** | be-i155n's `uncovered_criteria: none`, with an explicit per-done-when-criterion mapping against be-git2o's spec. `diff_tests_executed`: `TestStorePathHonorsEnvDBTargetOverAmbientWorkspace` PASS (7.17s) plus both subtests (`BEADS_DB`, `BD_DB`) PASS. |
| 3 | Tests pass (diff-owned) | **PASS** | Diff-owned test `TestStorePathHonorsEnvDBTargetOverAmbientWorkspace` independently re-verified by the deployer in isolation on this exact commit: PASS (38.53s), both subtests PASS (`BEADS_DB` 5.64s, `BD_DB` 0.69s). Independently re-verified a second time by the reviewer (be-i155n, 7.17s + subtests PASS). Two independent green runs, zero diff-owned SKIP or FAIL. |
| 3a | Pre-existing-failure attribution (non-diff-owned) | **PASS** | Deployer's own full-suite run (`make test`, Dolt-inclusive, fresh and independent) on this exact commit showed ~43 non-diff-owned failures across `cmd/bd`, `cmd/bd/doctor`, `internal/metrics`, `internal/storage/dolt`. Every failure carries an individually-verified mechanism proof (not population-level package inference): the `init`/`__complete`/doctor-family tests are structurally unreached — `skipsStoreInit` returns `nil` at `cmd/bd/main.go:1184`, before any of this diff's changed lines (1208-1222); `TestGetRoutingConfigValue_DBFallback`, `TestResolveCloseTargets`, `TestValidateCheck_AllClean` call their target functions directly and never reach `rootCmd` dispatch at all; `TestDoltRemoteAddPersistsSyncRemoteToSharedWorktreeConfig` does reach the changed lines via the `needsStoreDoltGrandchildren` fallthrough, but never sets `BEADS_DB`/`BD_DB`, making the added branch a guaranteed data-flow no-op for that test; the 10 `internal/storage/dolt` failures and 1 `internal/metrics` failure sit in packages with no import relationship to `cmd/bd`, and all 10 Dolt failures show a uniform 45.00s duration — the signature of a fixed per-test timeout hit under host contention, corroborated by live `pgrep` evidence of 12+ concurrent `dolt sql-server` processes from unrelated agents/worktrees on this host at the time of the run. Strongest corroboration: the reviewer's own independent full-suite run on this same commit (`test_counts` on be-i155n) came back **97 packages ok, 0 FAIL, 0 SKIP, exit 0, real 4m45.9s** — the identical diff, run independently at a different time, produced zero of these failures. Pre-existing tracker beads cited/filed for all 3 distinct root conditions per protocol clause 2: `internal/storage/dolt`/host-contention timeouts → be-52t59 (pre-existing consolidated tracker, sighting comment appended this gate); routing-config precedence mismatch (`TestGetRoutingConfigValue_DBFallback`) → be-5x0at (filed this gate, no prior tracker existed); metrics reap race (`TestStartDetachedReapsExitedChild`) → be-w5rdt (filed this gate, no prior tracker existed). |
| 3b | Policy / lint lane | **PASS** | `BD_LINT_NEW_FROM_MERGE_BASE=origin/main ./scripts/ci/pr-lint.sh`, run fresh by the deployer on this exact commit: gofmt clean ("All Go files are properly formatted"), `golangci-lint` 0 issues (native), `golangci-lint` 0 issues (windows cross-lint). First attempt hit a transient `"parallel golangci-lint is running"` lock-contention error (exit 3) from a concurrent agent's lint run on this host; retried to completion rather than treated as a finding. |
| 4 | No open HIGH findings | **PASS** | be-i155n `security_findings: none` across the full walk (injection, auth, access control, secrets, path traversal, deserialization, logging, dependency, config-exposure categories all explicitly reasoned N/A or clean for this diff, which only changes env-var precedence for an already-trusted local workspace path). |
| 5 | Branch clean | **PASS** | `git status --short` on `deploy/be-34u9a-gate` at `43e2065da0`: empty. |
| 6 | Diverges cleanly from main | **PASS** | `git merge-base origin/main HEAD` → `c0d8da42de5fd15c95adac85e342ba4a121da0fb`, identical to `git rev-parse origin/main` — 0 commits behind, clean fast-forward divergence. `git merge-tree --write-tree origin/main HEAD` → tree `31ec372eca8e15a13e73ac1e01c24e2f124fb8f3`, exit 0, zero conflicts. |
| 7 | Single feature theme | **PASS** | `git diff --stat origin/main...HEAD`: 3 files, `CHANGELOG.md` (+13), `cmd/bd/main.go` (+16), `cmd/bd/store_env_db_regression_test.go` (new, +116) — 145 insertions, 0 deletions. `git log --oneline origin/main..HEAD`: 3 commits, all cite be-git2o (`test(feat): red`, `feat: green`, `docs: add CHANGELOG entry`). No change to `internal/beads/` or the no-DB path. |

## Tests run by deployer on the cut branch (independent of review)

| Check | Result |
|---|---|
| `TestStorePathHonorsEnvDBTargetOverAmbientWorkspace` (isolated, both subtests) | PASS — 38.53s total (`BEADS_DB` 5.64s, `BD_DB` 0.69s) |
| Full suite (`make test`, Dolt-inclusive, fresh and independent) | 0 diff-owned FAIL/SKIP; ~43 non-diff-owned failures, each individually mechanism-attributed (criterion 3a) |
| `gofmt -l` / `go vet ./...` | clean, independently re-run by the deployer (not trusted from reviewer self-report) |
| `BD_LINT_NEW_FROM_MERGE_BASE=origin/main ./scripts/ci/pr-lint.sh` | 0 issues, native + windows cross-lint |

## Push target

`origin` (`gastownhall/beads`) denies push
(`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR` sentinel); `headfork`
(`quad341/beads-sec003-contrib.git`) accepts, per established precedent on
this rig (be-vc1m, be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3). The
`fork` remote (`quad341/beads.git`) is a stale, unrelated remote — confirmed
NOT the push target. PR opens cross-repo against `gastownhall/beads:main`
with head `quad341-sec003-contrib:deploy/be-34u9a-gate`.

## Merge authority

`gastownhall/beads` is a contributor-only repo for this rig — no rig agent
(including mayor) has merge access. Per established precedent (be-vc1m,
be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3), the deployer's job
ends at the open, verified PR. No merge-request is routed to mayor/mpr; gate
result reported to mayor via mail for visibility only.

## Verdict

**PASS 10/10** (criteria 0, 1, 2, 3, 3a, 3b, 4, 5, 6, 7) — clean branch cut at
the exact reviewed commit, zero diff-owned test failures across two
independent runs (deployer + reviewer), every non-diff-owned failure
individually mechanism-attributed and tracker-cited, lint/gofmt/vet clean,
zero merge-tree conflicts against current `origin/main`. Proceeding to push
`deploy/be-34u9a-gate` to `headfork` and open the PR.
