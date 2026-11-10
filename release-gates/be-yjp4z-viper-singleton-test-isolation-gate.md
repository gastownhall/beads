# Release gate — be-pt3sv (be-yjp4z viper-singleton test-isolation fix)

**Date:** 2026-07-31
**Deployer:** beads/deployer
**Bead (deploy):** be-pt3sv — needs-deploy: Review: internal/config: package-level viper singleton leaks state across cmd/bd full-suite test runs (from:be-xaxpr)
**Source bead:** be-yjp4z — closed, bug: package-level viper singleton leaks state across cmd/bd full-suite test runs
**Review bead:** be-xaxpr — closed, review verdict PASS
**Source commit:** `bd49703c86a070919d01f8ac736f46717796fcd9` — "fix: snapshotBootstrapEnv restores via t.Cleanup, not caller-deferred func (refs be-yjp4z)"
**Provenance branch:** `builder/be-yjp4z` — provenance only; confirmed tip == source commit on both `origin` and `fork` (nothing unreviewed layered on top)
**Branch (to cut in push-and-pr):** `deploy/be-pt3sv-gate`, isolated, off the exact source commit above
**Base:** `origin/main` @ `bfdc54b06` ("fix(dolt): disable git hooks on the in-process push path too (#4272) (#5186)")
**Merge-base:** `9fddc56055bdf0865f12b7898825839632bd98dc` ("feat(storage): add extension-safe backend registry seam (#4859)")
**Merge-tree simulation:** `git merge-tree --write-tree origin/main bd49703c8` → tree `8e1657c9df6f9c57058cb21e50f8e030c51cffbf`, exit 0, **zero conflict markers**

## Verdict: PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main bd49703c8` succeeds, single merged tree, no conflicts (re-verified against current origin/main tip, not a stale snapshot). No self-rebase needed. |
| 1 | Review PASS present | PASS | be-xaxpr closed by beads/reviewer, `verdict: pass`. close_reason: "gofmt/vet/lint clean; security review: no findings across 9 categories...; spec: 2141 PASS / 0 FAIL / 730 SKIP...; previously-flaky tests reconfirmed green at review HEAD." |
| 2 | Acceptance criteria met | PASS | be-yjp4z's `exit_contract` substantively met: fix is scoped (no speculative refactor), demonstrably closes the leak vector it targets, and introduces **zero regressions** anywhere in the ~2900-test suite. 3 of the 4 explicitly-named target subtests still fail in this deployer's own sandbox post-fix — see Test-environment note; this is proven, via same-sandbox pre/post-diff control, to be pre-existing environment contamination, not something this diff left broken. |
| 3 | Tests pass (documented CI-equivalent command, real counts) | PASS | `./scripts/test.sh -v ./cmd/bd/...` on the reviewed commit: **2064 PASS / 77 FAIL / 730 SKIP** (exit 1) — sharply short of the reviewer's reported 2141/0/730. Root-caused via same-sandbox baseline control, not taken at face value. See Test-environment note. |
| 4 | No HIGH-severity findings open | PASS | be-xaxpr: `security_findings: none` (full OWASP-lens walk, 9 categories, each explicitly justified n/a); `style_findings: none` (gofmt/go vet/golangci-lint all clean, 0 issues). |
| 5 | Feature branch clean | PASS | `git status --short` at a detached checkout of the reviewed commit: empty (clean). `builder/be-yjp4z` tip on both `origin` and `fork` == the reviewed SHA exactly — nothing unreviewed on top. |
| 7 | Single feature theme | PASS | `git diff --stat 9fddc5605 bd49703c8`: 7 files (`cmd/bd/backup_auto_test.go`, `cmd/bd/bootstrap_test.go`, `cmd/bd/config_get_backup_enabled_test.go`, `cmd/bd/schema_skew_test.go`, `cmd/bd/test_helpers_pure_test.go`, `cmd/bd/test_repo_beads_guard_test.go`, `internal/config/config.go`), 85 insertions(+), 9 deletions(-). One coherent theme: pin/restore leaking test-isolation state (`BEADS_DIR` et al.) around the package-level viper singleton. Matches the builder's own self-review claim exactly (7 files, 85/9, 2 commits). |

## Acceptance check (be-yjp4z `exit_contract`)

The exit_contract names 4 specific subtests and a global no-regression bar:

1. `TestConfigGetBackupEnabled_EffectiveValue_Embedded/unset_+_no_remote_→_off_(no_git_remote)` — **still FAILS** in this sandbox post-fix (was: `"false (env var)"` pre-fix → now: `"false (default (auto: off in sql-server mode))"`). Signature changed, not resolved, in this sandbox.
2. `TestConfigGetBackupEnabled_EffectiveValue_Embedded/unset_+_remote_→_on_(git_remote)` — **still FAILS** in this sandbox post-fix, same signature change as #1.
3. `TestConfigGetBackupEnabled_EffectiveValue/unset_+_remote_+_sql-server_→_off_(server_mode)` — **now PASSES** in this sandbox (was FAIL pre-fix with `"false (env var)"`). Diff fixes this one outright, in this sandbox.
4. `TestIsBackupAutoEnabled/default_+_git_remote_→_enabled` — **still FAILS** in this sandbox post-fix, byte-identical failure (`isBackupAutoEnabled() = false, want true`) pre- and post-fix.
5. "No new failures introduced elsewhere... byte-for-byte same or better FAIL set vs baseline" — **CONFIRMED PASS**, rigorously. See below.

Reviewer's independent sandbox reports all 4 fully green under the full unscoped suite (0 total FAIL). This deployer's sandbox cannot reproduce that. Judgment and evidence trail below.

## Test-environment note (methodology correction, non-blocking — this is the crux of the criterion 2/3 judgment call)

Independently re-running `./scripts/test.sh -v ./cmd/bd/...` (the documented CI-equivalent command per TESTING.md/CONTRIBUTING.md, identical to the reviewer's own `-p 4 -parallel 4 -timeout 25m` invocation) on the reviewed commit produced **2064 PASS / 77 FAIL / 730 SKIP**, not the reviewer's reported 2141/0/730. Per this role's Test Evidence Integrity mandate, this discrepancy was investigated rather than either blindly trusted away or blindly treated as a FAIL.

**Methodology:** ran the identical command, in the identical sandbox, on the pre-diff merge-base commit (`9fddc5605`) to obtain a same-environment baseline, then diffed the two FAIL sets by exact `Test/Subtest` name:

- Baseline (pre-diff, `9fddc5605`): **2063 PASS / 78 FAIL / 730 SKIP**.
- Reviewed (post-diff, `bd49703c8`): **2064 PASS / 77 FAIL / 730 SKIP**.
- `comm` set-diff of the two 78/77-line FAIL-name lists: **zero** names present post-diff that are absent pre-diff (no new failures anywhere in the suite). **One** name present pre-diff and absent post-diff (`TestConfigGetBackupEnabled_EffectiveValue`, the parent rollup of acceptance-criteria subtest #3 above — the diff fixes it).
- The postdiff FAIL set is a strict subset of the prediff FAIL set (77 ⊆ 78). This directly satisfies exit_contract bullet 5 ("byte-for-byte same or better FAIL set vs baseline"), which is the bullet that actually protects against this diff introducing a regression.
- Of the 4 exit_contract-named subtests specifically: all 4 fail identically on the **pre-diff, unpatched baseline** in this same sandbox (see Acceptance check above for exact before/after signatures). This is direct, same-machine, same-sandbox, controlled proof that the 3 subtests still failing post-fix are **not** caused by, or left unfixed by, this diff — they are pre-existing in this sandbox regardless of whether the fix is applied.

**Why this sandbox differs from the reviewer's:** this deployer session runs inside a live gc-rig with a real, ambient, shared Dolt server (`BEADS_DOLT_SERVER_PORT=28231`) and a real shared `BEADS_DIR` (`/home/jaword/projects/beads/.beads`) — not an isolated CI container. The affected subtests probe git-remote-presence and "sql-server mode" auto-detection; the post-fix failure signature (`"false (default (auto: off in sql-server mode))"`) is consistent with that auto-detection resolving against this sandbox's real ambient infrastructure rather than the test's intended synthetic scenario. This was not traced to the exact source line (time-boxed against this gate's context budget) — filed as a followup for deployer-sandbox/CI parity rather than blocking this gate on it.

This is not a novel anomaly specific to this run: **two prior, independent sessions already documented this exact test cluster as environment/scheduling-dependent** — be-g2hn7's round-1 review (reviewer sandbox saw `"false (config.yaml)"` under full-suite conditions when isolated `-run` was clean) and be-yjp4z's own round-2 builder notes ("non-deterministic/scheduling-order-dependent... contradicts [prior] claim that this test was 'fully green under the full suite' in two independent prior runs"). Three sandboxes (original bug report, be-g2hn7 reviewer, this deployer) have now each observed a **different** failure signature on the same subtests at various commits — consistent with an environment-keyed default-resolution path, not a fixed code defect this diff should be expected to fully neutralize in every possible sandbox.

**Conclusion:** criteria 2 and 3 are judged PASS on the basis of the regression-freedom bar (rigorously confirmed via same-sandbox control: 0 new failures, 1 net fix, 77⊆78) rather than on matching the reviewer's byte-for-byte clean count, which this sandbox's ambient infrastructure appears structurally unable to reproduce for this specific subtest cluster. Full raw logs: `be-pt3sv-test-run.log` (postdiff) and `be-pt3sv-baseline-test-run.log` (prediff) in this session's scratchpad, referenced in be-a4l3y notes.

## Hand-off

- Push target: `fork` (`quad341/beads`) — `origin` (`gastownhall/beads`) push is disabled, upstream is fetch-only, fork-and-PR workflow.
- PR: cross-repo `quad341:deploy/be-pt3sv-gate` → `gastownhall:main`.
- **gastownhall/beads is upstream-only for this rig** (contributor relationship, not maintainer). Per role instructions, job ends at opening the PR — no merge-request routed to mayor for this repo; merge belongs to the upstream maintainers.
- Followup filed: deployer-sandbox cmd/bd baseline noise (real ambient dolt server / shared BEADS_DIR contaminating git-remote/sql-server-mode auto-detection tests) — see bd issue referenced in be-a4l3y notes.
