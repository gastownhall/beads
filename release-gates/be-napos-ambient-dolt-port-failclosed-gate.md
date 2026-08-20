# Release gate — Test suites create testdb_* on the PRODUCTION dolt server: harness fails open on the ambient port when Docker is absent (be-napos)

- **Deploy bead:** be-napos (needs-deploy, routed beads/deployer; from review be-7fym1)
- **Build bead:** be-h2h25 — round 1 tdd_green `9634e6f2c6c6fdf7f4d80e9abea6c909364eb45a` (verdict: request-changes); round 2 tdd_green / reviewed HEAD `f28e8ac3e04b6dede54173fa4902631e1329696f` (verdict: pass)
- **Review bead:** be-7fym1 — verdict **PASS** (round 2), closed with reason `pass`
- **Commit deployed:** `c65801ce4d4880dc9ace38ca1c8477f069f7b4f3` — corrected from the stale reviewed SHA `f28e8ac3e04b6dede54173fa4902631e1329696f` (see SHA correction below and criterion 6)
- **Source branch:** `builder/be-h2h25` — provenance only, never a push target
- **Related beads:** be-rl6tm (P2, needs-architecture — residual gap split out of be-h2h25 by architect-6a to keep this fix scoped to its actual root cause; the 2 diff-owned SKIPs are waived against this tracker via `mayor-2026-08-20-be-h2h25-c3`), be-ckoic (pre-existing gosec G602 false positives in `backend/conformance/*.go`, blocking clean `make ci-pr-lint` — unrelated path, attributed per criterion 3a)
- **Deploy branch:** `deploy/be-napos-gate`, derived mechanically via `resolve_deploy_branch_target`
- **Push target:** `headfork` (`quad341/beads-sec003-contrib.git`) — `origin` push is disabled by design on this rig (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`)
- **PR:** [gastownhall/beads#5885](https://github.com/gastownhall/beads/pull/5885) — OPEN, MERGEABLE
- **Evaluated:** 2026-08-20 by beads/deployer

## SHA correction (recorded on be-napos)

`builder/be-h2h25` was rebased onto the current `origin/main` tip
(`d871b5c54aed638c8be32ae23a8f5abba20b6df1`) after review passed, moving the
branch tip from the reviewed `f28e8ac3e04b6dede54173fa4902631e1329696f` to
`c65801ce4d4880dc9ace38ca1c8477f069f7b4f3`. Content-identity verified before
trusting this as equivalent to the reviewed diff, not merely assumed from the
rebase:

- `git diff f28e8ac3e04b6dede54173fa4902631e1329696f c65801ce4d -- <3 diff-owned files>` → empty
- `git range-diff d38ac728b5..f28e8ac3e d871b5c54ae..c65801ce4d` → both commits marked `=` (patch-equivalent); the only delta is 9 unrelated origin/main commits absorbed into the new base
- `git merge-base --is-ancestor d871b5c54ae c65801ce4d` → confirmed; `c65801ce4d` already contains the current origin/main tip

Review verdict (`pass`, be-7fym1) and its findings are treated as still valid
for the corrected SHA. All gate criteria below were independently evaluated
against `c65801ce4d4880dc9ace38ca1c8477f069f7b4f3`, not re-derived from the
stale SHA.

## Scope

`internal/testutil/testdoltserver.go`'s `EnsureDoltContainerForTestMain` now
calls a new `neutralizeAmbientDoltPort()` helper (`os.Unsetenv` on both
`BEADS_DOLT_SERVER_PORT` and `BEADS_DOLT_PORT`) on both of its failure paths,
so a Docker-less host fails **closed** instead of leaving test suites pointed
at the ambient (production) Dolt port. Root cause of P1 gm-2g3g5r/gm-u8bf4o —
202 `testdb_*` databases had accumulated live on the production dolt server
(127.0.0.1:28231) because Docker is unavailable on this host, `checkDolt()`
returns `doltNoDocker`, and the only code path that overwrites the ambient
port variables was never reached.

Three files, one root cause: `internal/testutil/testdoltserver.go` (fix),
`internal/storage/dolt/gm2g3g5r_leak_repro_test.go` and
`internal/testutil/gm2g3g5r_failopen_test.go` (new repro/regression tests).
Single feature theme confirmed by `assert_deploy_ancestry_scope` (criterion 6/7).

A related but architecturally separate gap — `beads.Open` has no
production-port detection when `BeadsDir` is unset — was deliberately split
out to be-rl6tm on architect-6a's recommendation, to keep this fix scoped to
the `EnsureDoltContainerForTestMain` path. The 2 diff-owned tests that touch
that gap are intentionally SKIPped here, covered by mayor waiver
`mayor-2026-08-20-be-h2h25-c3`.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 0 | Already merged? (pre-flight) | **NO** | Commit never pushed to `origin`; `gh api repos/gastownhall/beads/commits/<deploySHA>/pulls` → 422 (no such commit on origin). Re-confirmed for the corrected SHA before proceeding. Proceeded. |
| 1 | Review PASS present | **PASS** | be-7fym1, `close_reason: pass`. Round 1 (`tdd_green 9634e6f2c...`) was `verdict: request-changes`; round 2 (`HEAD f28e8ac3e...`) is `verdict: pass`, `deploy_commit: f28e8ac3e04b6dede54173fa4902631e1329696f`. be-napos's own description states "Reviewed and PASSED by beads/reviewer (review bead be-7fym1)." |
| 2 | Acceptance criteria met | **PASS** | be-h2h25's 5-item Done-when checklist, each independently checked: (1) `EnsureDoltContainerForTestMain` clears both env vars on both failure paths — confirmed by direct diff read. (2) `TestEnsureDoltContainerForTestMain_NeutralizesAmbientPortOnFailure` passes — independently re-run, PASS. (3) The three `internal/storage/dolt` repro tests are landed and the RED ones converted to assert fixed behavior — confirmed by diff read and test outcomes below. (4) Pre-existing guard tests still pass — confirmed, 0 regressions in the full package re-run. (5) A full `go test ./...` run on a Docker-less host creates zero new `testdb_*` — the literal end-to-end form against the real ambient production port (28231) was deliberately **not** re-attempted for safety; reviewer substituted a safe injected-port proxy test exercising the same code path without touching production. Independently reviewed this substitution and agree it is sound: the proxy test exercises the exact `neutralizeAmbientDoltPort()` call sites that would otherwise leave the ambient port live. |
| 3 | Tests pass (diff-owned-SKIP=FAIL rule) | **PASS** | Independently re-ran `go test ./internal/testutil/... ./internal/storage/dolt/... -v -count=1` on the corrected deploy SHA (real container-backed execution via podman socket, `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock`, matching the reviewer's own setup; ambient `BEADS_DOLT_SERVER_PORT=28231` confirmed present — the exact hazard this fix targets). Result: **312 PASS / 0 FAIL / 783 SKIP**, an exact match to review's reported count. All 4 diff-owned tests matched by name and outcome: 2 PASS outright, 2 SKIP. Waiver `mayor-2026-08-20-be-h2h25-c3` independently confirmed genuine (full mayor reasoning text present in be-7fym1 notes) and correctly scoped to exactly the 2 SKIPped tests — both cite `be-rl6tm` in their skip messages, confirmed via log grep. Full log: `be-napos-test-run1.log` (scratchpad). |
| 3a | Pre-existing-failure attribution | **N/A** (test suite) / **APPLIED** (lint lane) | No diff-owned FAIL occurred, so 3a does not apply to the test suite itself. It is applied and satisfied for the `ci-pr-lint` gosec finding — see 3b. |
| 3b | Policy / lint lane | **PASS** | `ci-pr-policy`: my own worktree showed a version-consistency failure citing `.githooks/commit-msg`; root-caused to a **local, non-git-tracked session artifact** (`worktree-setup.sh`'s gc-commit-gate-shim, rewritten every session start, pointing outside the beads repo) — not a repo defect. Proven by re-running `make ci-pr-policy` against the exact deploy SHA in a second, plain `git worktree add` checkout with no rig tooling installed on top: clean pass, exit 0. `ci-pr-lint`: one finding, gosec G602 in `backend/conformance/*.go`, verified genuinely pre-existing by triple confirmation (origin/main baseline scratch worktree, deploy SHA in this worktree, deploy SHA in a second clean scratch worktree — all three byte-identical findings at the same file:line). Satisfies all 4 criterion-3a clauses: not diff-owned (different files entirely — `backend/conformance/*` vs. the 3 diff-owned files under `internal/testutil/` and `internal/storage/dolt/`), tracked bead id (be-ckoic, open), proven pre-existing at `origin/main` directly, no path overlap with the diff. |
| 4 | No open HIGH findings | **PASS** | be-7fym1 `security_findings`: none. Diff is test-only (`internal/testutil/testdoltserver.go` confirmed 0 non-test importers via repo-wide grep — never ships in the production `bd` binary) plus 2 new test files. No injection surface, no auth/session/PII changes, no new deps. `neutralizeAmbientDoltPort()` unsets exactly 2 named env vars, no wildcard/loop blast radius. Explicitly a net security improvement (fail-open → fail-closed). `style_findings`: none (gofmt -l clean, go vet clean) — independently reconfirmed below. |
| 5 | Branch clean | **PASS** | `git status --short --branch` on the deployer worktree at the deploy SHA: `## deploy/be-napos-gate`, no modified tracked files. |
| 6 | Diverges cleanly from main | **PASS** | Branch was already rebased onto the current `origin/main` tip by another process before this evaluation began (see SHA correction above); zero divergence risk, no self-rebase needed. `assert_deploy_ancestry_scope origin/main c65801ce4d4880dc9ace38ca1c8477f069f7b4f3 be-h2h25 be-napos` → rc=0 (no `.claude/**` paths introduced, every commit in range cites an accepted bead id). |
| 7 | Single feature theme | **PASS** | One fix, one root cause, 3 files (1 production test-harness file + 2 new test files). `assert_deploy_ancestry_scope` found zero stray commits — every commit in the deploy range cites be-h2h25 or be-napos. |

## Tests run by deployer on the cut branch (independent of review)

| Check | Result |
|---|---|
| `go test ./internal/testutil/... ./internal/storage/dolt/... -v -count=1` (real container-backed execution, podman socket, ambient `BEADS_DOLT_SERVER_PORT=28231` present) | **312 PASS / 0 FAIL / 783 SKIP** — exact match to review's reported count; all 4 diff-owned tests matched by name/outcome |
| `gofmt -l` (3 diff-owned files: testdoltserver.go, gm2g3g5r_leak_repro_test.go, gm2g3g5r_failopen_test.go) | clean (no output) |
| `go build ./...` | clean, exit 0 |
| `go vet ./...` | clean, exit 0 |
| `make ci-pr-policy` (second, plain, shim-free `git worktree add` checkout of the deploy SHA) | clean, exit 0 |
| `make ci-pr-lint` | 1 finding: gosec G602 in `backend/conformance/*.go` — pre-existing, attributed to open tracker be-ckoic, no path overlap with the diff (criterion 3a/3b) |

Triple-confirmed the gosec finding is pre-existing rather than diff-caused:
identical file:line finding reproduced on an `origin/main` baseline scratch
worktree, on the deploy SHA in this deployer worktree, and on the deploy SHA
in a second clean scratch worktree.

## Push target

`origin` (`gastownhall/beads`) denies push
(`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR` sentinel); `headfork`
(`quad341/beads-sec003-contrib.git`) accepts, per established precedent on
this rig (be-krza3, be-79jh, and others). PR opens cross-repo against
`gastownhall/beads:main` with head `quad341:deploy/be-napos-gate`.

## Merge authority

`gastownhall/beads` is a contributor-only repo for this rig — no rig agent
has push/maintain/admin access, confirmed structurally by the disabled
`origin` push sentinel above. Per established, repeated precedent on this
exact repo (be-vc1m, be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3),
the deployer's job ends at the open, verified PR; only upstream maintainers
of `gastownhall/beads` can merge it.

**Discrepancy flagged for the record:** be-napos's own description text says
"Route a merge-request to mayor/mpr. Merge authority is operator/mayor/mpr
only -- no rig agent runs `gh pr merge`." This most plausibly reflects a
generic deployer-prompt template shared across repos where mayor/mpr does
hold real merge rights (e.g. internal gc-management repos) — it does not
match this specific repo's structural reality, independently confirmed
across 7 prior gates on this same repo plus the disabled-origin-push
sentinel above. No rig agent, including mayor, has been shown to hold merge
rights on this external, contributor-only repo. Reconciling both instead of
silently picking one: no rig agent (including this one) will run
`gh pr merge` — consistent with both framings — and, honoring be-napos's
explicit instruction to *route* a merge-request rather than silently
downgrading it to a bare FYI, I will mail mayor/mpr an explicit
merge-request/deploy-clearance report for be-napos once the PR is open, so
mayor can decide whether any further coordination with upstream maintainers
is warranted.

## Verdict

**PASS 7/7** — pushed `deploy/be-napos-gate` to `headfork`, opened
[gastownhall/beads#5885](https://github.com/gastownhall/beads/pull/5885)
against `main`, independently confirmed OPEN/MERGEABLE via `gh pr view`.
Standing down; merge belongs to upstream maintainers.
