# Release Gate: be-kh89i — bd close prints 'Newly unblocked' but never clears status='blocked' drift

**Deploy bead:** be-kh89i
**Review bead:** be-lapac (verdict: pass, closed)
**Build bead:** be-ntbxt
**Deploy commit:** `fa96ed82fb9783475be47d03391a5cd724925ea6`
**Provenance branch:** `builder/be-ntbxt` (NOT a push target — long-lived shared builder branch)
**Base ref:** `origin/main` @ `f56632adcfabed7da6ed0aabe4e760066b472c46`
**Repo:** gastownhall/beads (contributor-only — no push/maintain/admin; gate ends at PR, no merge-request to mayor)
**Evaluated by:** beads/deployer, 2026-09-15

## Verdict: PASS — proceeding to isolated deploy branch + PR

## Criterion 6 — Clean divergence from base ref (evaluated first)
PASS. Fresh `git rev-parse origin/main` at evaluation time: `f56632adcfabed7da6ed0aabe4e760066b472c46`.
`git merge-base origin/main fa96ed82fb9783475be47d03391a5cd724925ea6` returns the
same SHA — the deploy SHA's history already contains the current origin/main
tip exactly (fast-forward-able), zero divergence risk, no self-rebase needed.
This also matches be-lapac's own recorded `base_commit` metadata exactly,
confirming main has not advanced since review.

Pre-flight already-merged check: `gh api repos/gastownhall/beads/commits/<deploySHA>/pulls`
→ `[]`. No PR exists yet for this SHA; normal flow applies. (Will re-check
immediately before push, per protocol.)

## Criterion 1 — Review PASS present
PASS. be-lapac (status: closed) notes contain `=== REVIEW VERDICT: PASS ===`
for commit `fa96ed82fb9783475be47d03391a5cd724925ea6` on branch
`builder/be-ntbxt`. be-lapac's structured metadata independently confirms
`commit`, `branch`, `build_bead: be-ntbxt`, and `base_commit` matching
criterion 6 above exactly.

## Criterion 2 — Acceptance criteria met
PASS. be-lapac's notes: "Spec compliance (exit_contract): all 5 criteria
confirmed" (code read + code-path tracing for the CLI end-to-end criterion,
after a live repro was blocked by an ambient shared dolt server — no data
mutated). Code review: 13 changed files (+850/-10) read in full, no
correctness/security/style defects, NULL-poisoning guard correct, gosec
suppressions justified. Coverage: "5 new regression tests across both
storage backends... satisfies done-when exactly" — positive+negative cases
on both `internal/storage/dolt` and `internal/storage/embeddeddolt`.

## Criterion 3 — Tests pass
PASS.

- `go build ./...`, `go vet ./...`: clean per reviewer (be-lapac notes);
  not independently re-run this session (no source changes since review).
- **Fresh full-scope run, independent of the reviewer's own run**, on the
  exact reviewed SHA `fa96ed82fb9783475be47d03391a5cd724925ea6`
  (`TEST_COVER=1 BEADS_TEST_EMBEDDED_DOLT=1 BEADS_TEST_ENV_RUN_DOLT=1`,
  `-p 4` package concurrency, full `./...` scope — the only lane that counts
  as primary criterion-3 evidence): **7089 PASS, 15 FAIL, 473 SKIP**, overall
  exit 1.

**Diff-owned test mapping (by name, required — the hard gate):**
All 5 diff-owned tests confirmed clean PASS by name:
- `TestEmbeddedStatusBlockedDriftWiring` — PASS (0.52s), found directly in
  the fresh full-scope run's own output.
- `TestEmbeddedStatusBlockedDriftOpenBlockerNeverFlagged` — PASS (0.25s),
  found directly in the fresh full-scope run's own output.
- `TestStatusBlockedDrift_ClosedBlockerIsFlaggedAndFixed`,
  `TestStatusBlockedDrift_NoBlockerRecordedIsFlaggedAndFixed`,
  `TestStatusBlockedDrift_OpenBlockerIsNeverFlaggedOrTouched` — absent from
  the full-scope run's own output because `internal/storage/dolt` hit its
  package-level 25m timeout before reaching them (confirmed via
  `grep -n "^func Test" internal/storage/dolt/status_blocked_test.go`: exactly
  3 top-level test functions, no hidden subtests, ruling out a silent
  SKIP/rename). Closed via a supplementary isolated rerun (zero contention):
  `BEADS_TEST_ENV_RUN_DOLT=1 go test ./internal/storage/dolt/... -run '^TestStatusBlockedDrift' -v -timeout 10m`
  → all 3 PASS cleanly (0.26s/0.32s/0.44s), package `ok` in 9.669s, exit 0.
  This mirrors both be-hj76u's own established "isolated rerun for
  confirmatory non-reproduction" methodology and the reviewer's own
  two-track evidence pattern recorded in be-lapac (full-suite +
  separate targeted diff-owned rerun). Supplementary/strengthening
  evidence, not a substitute for the full-scope run as the primary record —
  used here only to close a by-name mapping gap the full-scope run itself
  could not fill due to an already-tracked environmental timeout.

No diff-owned test SKIPped or FAILed in any run this session. No waiver
needed or used.

**3a — pre-existing-failure attribution (all 15 non-diff-owned FAILs):**
File-location grep confirmed none of the 15 individual `--- FAIL:` lines,
nor the 3 package-level FAILs (`cmd/bd`, `cmd/bd/doctor`,
`internal/storage/dolt`), touch any of the 13 diff-owned files. Every
failure independently attributed via reproduction, not assertion:

| Failure | Attribution | Method |
|---|---|---|
| `cmd/bd`, `internal/storage/dolt` pkg-level 25m timeout | be-hj76u | Pre-existing; goroutine dumps cross-referenced against all diff-touched symbols, zero matches. Corroborated fresh this session (see below). |
| `TestFindBeadsRepoRoot_WorktreeFallback` | be-ey0oy | Already tracked (filed 2026-09-14 during be-dm8ug's review); merge-base-reproduced at the same SHA `f56632adc` this gate independently confirmed as current. |
| `TestCorruptMetadataDiagnosticsRunAndDataFailsLoud` | be-8gw4v | Already tracked; reproduced byte-for-byte identical at merge-base in a clean throwaway worktree; file unchanged by diff. |
| `TestEmbeddedContributorCreate` | be-oqmxz | Already tracked; non-deterministic filesystem race, file unchanged by diff. |
| `TestRunDoltHealthChecks_DoltBackendNoServer` | host contention | Isolated rerun this session: clean PASS (0.00s), zero contention. Not a real defect. |
| `TestCheckFreshClone_ServerModeUnreachable` | **be-9swck (new)** | Reproduced byte-for-byte identical (same wrong message content) both in isolation at the reviewed SHA and in a clean throwaway worktree at merge-base `f56632adc`. `cmd/bd/doctor.go`'s diff-owned change is purely additive (appends one independent check call); does not touch this test's file or fresh-clone-detection logic. Filed this session. |
| `TestCheckRepoFingerprint_UsesTargetRepoOutsideCWD` | **be-4zba2 (new)** | 45.00s FAIL under full-suite contention (host-contention signature); SKIPs identically ("Dolt server not available") under narrow `-run` selection at both the reviewed SHA and merge-base `f56632adc` — diff-unrelated either way. Filed this session. |
| `TestCheckTestPollution_NoTestIssues_EmptyDB` | **be-6qgxb (new)** | Reproduced byte-for-byte identical (same status/message mismatch) both in isolation at the reviewed SHA and at merge-base `f56632adc`. Filed this session. |
| 8× `TestCrossProject_*`, `TestGetIssue_WispLabelTableErrorPropagates`, `TestPullFrom*` (internal/storage/dolt) | be-hj76u (host contention) | Isolated rerun this session: all 8 PASS cleanly, zero contention, 104.887s total, exit 0. Corroborating note appended to be-hj76u. |

This exactly matches the same 3 pre-existing beads (be-hj76u, be-8gw4v,
be-oqmxz) the reviewer cited for this identical diff in be-lapac, plus 3
newly-filed trackers (be-9swck, be-4zba2, be-6qgxb) for failures the
reviewer's own full-suite run apparently did not individually enumerate by
name, and 1 already-tracked bead (be-ey0oy) filed by the reviewer pool one
day prior on an unrelated review. `waiver_ref`: not needed — no diff-owned
SKIP or FAIL occurred at any point.

**3b — policy/lint lane:**
PASS (carried forward from earlier this session, not re-run — no source
changes since). All 10 checks in `scripts/ci/pr-policy.sh`: 9 clean PASS, 1
(`check-versions.sh`) attributed to non-repo worktree-local pollution,
matching established be-79jh precedent (rig-installed commit-msg shim, not a
git-tracked path at either origin/main or the deploy SHA).

**3c — CI-config diff lane:**
N/A. Confirmed no `.github/workflows/**` files in the 13-file diff-owned
list.

## Criterion 4 — No open HIGH findings
PASS. be-lapac's OWASP-lens security review: "nothing flagged." Style
findings: 0 diff-owned lint issues (3 pre-existing findings elsewhere,
untouched by this diff). No HIGH-severity label present on be-kh89i,
be-lapac, or be-ntbxt.

## Criterion 5 — Final branch is clean
PASS. `git status --short --branch` on the detached-HEAD checkout at the
reviewed SHA showed a fully clean tree. The isolated deploy branch will be
cut directly from this exact SHA with no additional changes, inheriting this
same clean state by construction.

## Criterion 7 — Single feature theme
PASS. 13 changed files (+850/-10), confirmed via
`git diff --name-only f56632adc..fa96ed82` (the true current merge-base, not
a stale one) — matches be-lapac's own stated file count exactly. All files
serve exactly one fix (status='blocked' drift left behind after a bead's
last `blocks` dependency closes, or with none ever recorded — be-ntbxt):
`cmd/bd/doctor.go` (wires in the new check, purely additive),
`cmd/bd/doctor/status_blocked.go` + `cmd/bd/doctor/fix/status_blocked.go`
(check + fix implementation), `cmd/bd/close.go` +
`cmd/bd/close_proxied_server.go` + `cmd/bd/doctor_fix.go` +
`cmd/bd/recompute_blocked.go` (wiring into the close/recompute paths),
`internal/storage/issueops/status_blocked_drift.go` (core drift-detection
logic), `internal/storage/dolt/store.go` +
`internal/storage/embeddeddolt/version_control.go` + `internal/storage/storage.go`
(storage-backend plumbing), and 2 new test files
(`internal/storage/dolt/status_blocked_test.go`,
`internal/storage/embeddeddolt/status_blocked_drift_test.go`) exercising
exactly this fix across both backends. No scope creep.

## PR-open instructions check
be-kh89i's `notes` field: empty. No `PR-DESCRIPTION:BEGIN/END` block. No
`gc.pr_ping` metadata key (full metadata dump checked: only
`gc.deploy_branch`, `gc.deploy_commit`, `gc.routed_to`, `gc.session_name`,
`gc.work_dir`, `molecule_id`). No other instruction-shaped prose in the
description beyond the standard boilerplate Action text. Standard PR-open
flow applies with no notes-appending, no pinging, no mail-mayor fallback
needed.

## Next step
Repo is contributor-only for this rig (no push/maintain/admin on
gastownhall/beads) — per the merge-authority carve-out, this deployer's job
ends at opening the PR. No merge-request will be routed to mayor and no
`release-gate/deploy-clearance` commit status will be published, deliberately
deviating from be-kh89i's generic "route to mayor" Action text per the
confirmed contributor-only carve-out (established precedent: be-79jh,
be-km2kg, et al.). Proceeding to cut `deploy/be-kh89i-gate` from
`fa96ed82fb9783475be47d03391a5cd724925ea6`, push to `fork`, and open the PR.
