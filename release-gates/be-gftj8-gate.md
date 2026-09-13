# Release gate — be-gftj8 (Review: Fix: proxied/domain path deletes the lease it just armed on claim+assignee override, parity with PR 5349)

**Date:** 2026-09-12
**Deployer:** beads/deployer
**Bead (deploy):** be-gftj8
**Source bead:** be-193q1 — status closed, verdict `pass`. Reviewer notes confirm: gofmt/go vet/full-module build clean, golangci-lint on changed packages (`internal/storage/domain/...`) = 0 issues, full OWASP-Top-10-style security walk (9 categories, all none/n-a, one non-blocking minor: a doc-comment staleness nit outside the touched-file scope), and all 3 "Please verify" items independently re-verified from source rather than taken on the builder's word. `uncovered_criteria: none`.
**Source commit (as originally reviewed by be-193q1):** `1f018bc1b268e8b44c255090a3d1ddcc4882ef2d` — fix(domain): green — proxied/domain path deletes lease it just armed on claim+assignee override (refs be-plv)
  - Parent/RED: `9a50af9b91120cf24b6f0ee557125ab8c73acdbe` — test(domain): red — same title (refs be-plv). Confirmed compile-fails against pre-fix code.
  - Both SHAs independently re-verified via `git rev-parse --verify --quiet "<sha>^{commit}"` — both resolve.
  - Base (as originally reviewed): `origin/main` @ `a690b0a8c4d1ddc4f0bd9bf767499625dd71bc96`.

**Correction — post-review rebase (be-1shoq, PR #6501 review point 9, 2026-09-12):** bee-ghosttrack's review correctly flagged that `origin/main` had advanced since the assertions above were written; independently verified as **6 commits** (`git rev-list --count $(git merge-base <pre-rebase-HEAD> origin/main)..origin/main` = 6). The two SHAs above are now stale identities: rebasing `deploy/be-gftj8-gate` onto current `origin/main` rewrote them to `4cf77dcb5250ea67e4d4dd9d81b21c3b09749199` (RED) and `ce89b8213b21eb1e252bd78e4590dabd82867ee5` (GREEN) — same content, same `be-plv` authorship, new parents. Rebase re-verified clean (`git merge-tree` reported no conflicts; `origin/main`'s only touch to `internal/storage/domain/db/issue.go` since the old base was an unrelated `GetByIDs`-batching commit, `91aeeb054`, outside the `Update`/`clearLease` region this PR touches).
  - **New base:** `origin/main` @ `f56632adcfabed7da6ed0aabe4e760066b472c46` (merge-base(HEAD, origin/main) re-confirmed identical to this tip at time of writing).
  - **New tip, addressing the review's other 8 points (be-1shoq):** RED `7364d2141e5550d8b09d5783f75cafe62bd1d021` (test(domain): red — claim+assignee override must arm lease via ApplyUpdate) / GREEN `8e37018a4e72994a60d2807d6153ddd2ffd5f870` (fix(domain): green — address PR #6501 review points 1,3-8, refs be-1shoq).
**Branch:** `deploy/be-gftj8-gate`
**Push target:** `headfork` (`quad341/beads-sec003-contrib`) — pushed and independently re-verified: `git ls-remote headfork refs/heads/deploy/be-gftj8-gate` returns `517c6d1c267ee54a24be17e55c1bf09fd4f8bf4a`, matching local `HEAD` exactly.
**PR:** [gastownhall/beads#6501](https://github.com/gastownhall/beads/pull/6501) — `quad341:deploy/be-gftj8-gate` → `gastownhall:main`. Verified via `gh pr view 6501`: `state=OPEN`, `mergeable=MERGEABLE`, `author=quad341` (our own account — not an external contributor, no human-hold triggered).

## Verdict: 7/7 — PASS, no waivers

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review pass | PASS | be-193q1 closed, verdict `pass`, full first-hand reviewer verification (style/security/spec) plus independent deployer re-verification of the 3 "Please verify" items |
| 2 | Acceptance criteria met | PASS | All 8 diff-owned tests confirmed passing by exact name (see Criterion 3); reviewer's exit-contract walk independently corroborates |
| 3 | Full-suite tests | PASS | See Criterion 3 detail below — zero diff-owned or non-diff-owned failures this round; nothing to attribute |
| 3a | Pre-existing-failure attribution | PASS (n/a — no failures) | The 3 packages tracked by be-ek5o4 (cmd/bd, cmd/bd/doctor, internal/storage/dolt) all report clean `ok` in this round's independent full-suite run |
| 3b | Policy/lint lane (`ci-pr-policy` + `ci-pr-lint`) | PASS (attributed) | 2 pre-existing findings attributed to trackers; see Criterion 3 detail |
| 3c | CI-config-diff live-run | N/A | Diff touches no `.github/` or `scripts/ci/` files (`git diff --name-only origin/main...HEAD` confirms) |
| 4 | Zero open HIGH | PASS | Reviewer's OWASP walk: 9 categories, all none/n-a, one non-blocking minor (doc-comment staleness nit, out of touched-file scope), no HIGH findings |
| 5 | Clean git status | PASS | `git status --short` clean on `deploy/be-gftj8-gate` at SHA `1f018bc1b2` |
| 6 | No merge conflicts with BASE_REF | PASS | **Corrected post-rebase (be-1shoq, review point 9):** `origin/main` had in fact advanced 6 commits since this gate's original writing (review point 9, correctly flagged); branch rebased onto current `origin/main` @ `f56632adcfabed7da6ed0aabe4e760066b472c46`, re-verified clean — no conflicts. See Source-commit correction above for detail. |
| 7 | Single feature theme/ancestry scope | PASS | 4 files, +384/-13, all on-theme for the claim+assignee-override lease-deletion fix; `assert_deploy_ancestry_scope` confirmed both commits cite `be-plv` |

**Diffstat** (`git diff --stat origin/main...HEAD`):
```
 internal/storage/domain/db/issue.go                |  27 ++-
 internal/storage/domain/db/issue_test.go           | 152 +++++++++++++++++
 .../storage/domain/db/parity_claim_lease_test.go   | 188 +++++++++++++++++++++
 internal/storage/domain/issue.go                   |  30 ++--
 4 files changed, 384 insertions(+), 13 deletions(-)
```

## Criterion 3 — full-suite test evidence + policy/lint lane + CI-config-diff (3c)

**test_cmd:** `TEST_COVER=1 ./scripts/test.sh` (via `make test`; internally `go test -p 4 -parallel 4 -timeout 25m -coverprofile=... ./... -count=1`)
**test_cmd_scope:** full-suite, whole-repo, no `BEADS_TEST_SKIP`
**test_counts:** 120/120 packages accounted for (matches `go list ./...` exactly) — 97 `ok`, 1 `?` (`memoryops`, no test files), 22 unprefixed `coverage: 0.0% of statements` lines. The 22 are packages with no effective test files under `-coverprofile` mode — a standard, deterministic Go-toolchain formatting quirk (different from the plain `?   pkg  [no test files]` line used without `-coverprofile`), independently reproduced in isolation (`go test -coverprofile=... ./beadserrors/...` alone) and confirmed via direct filesystem check that these packages have zero `_test.go` files. Zero `FAIL`, zero `--- FAIL`, zero `--- SKIP`, zero panics, zero races.

First attempt reported `EXIT_CODE=2`; root-caused to a shared, unscoped `/tmp/beads.coverage.out` default path (`scripts/test.sh` line 52) colliding with a concurrent fleet session's own test run, corrupting the coverage profile and crashing the ancillary `go tool cover -func` summary step on a foreign/stale file reference — **not a real test failure** (`set -e` kills the script at that later step before it ever reaches `exit $status`, so the script's own exit code does not reflect the real go-test result in this case). Re-ran with `TEST_COVERPROFILE` pointed at a session-scoped scratchpad path; the retry completed the coverage-summary step cleanly (`Total coverage: 39.4%`), confirming the diagnosis. Log: `be-gftj8-criterion3-fullsuite-retry.log`.

**diff_tests_executed** (all PASS, independently re-run this round via `go test -v -run 'TestDomainDB' ./internal/storage/domain/db/... -count=1`, log `be-gftj8-diffowned-verbose.log`, package `ok ... 47.857s`):
- `TestDomainDB/TestIssueSQLRepository/UpdateClaimLease/IsClaimReArmsLeaseOnAssigneeOverride`
- `TestDomainDB/TestIssueSQLRepository/UpdateClaimLease/WithoutIsClaimOverrideStillDeletesLease`
- `TestDomainDB/TestIssueSQLRepository/UpdateClaimLease/IsClaimWithNoOverrideLeavesLeaseUntouched`
- `TestDomainDB/TestIssueSQLRepository/UpdateClaimLease/IsClaimStatusRevertStillDeletesLease`
- `TestDomainDB/TestIssueSQLRepository/UpdateClaimLease/IsClaimOnWispNeverGrantsLease`
- `TestDomainDB/TestParityPlainTransferDeletesLeaseOnBothBackends`
- `TestDomainDB/TestParityClaimNoOverridePreservesLeaseOnBothBackends`
- `TestDomainDB/TestParityClaimOverrideLeaseDivergesPendingPR5349` (characterization test pinning the known cross-backend divergence pending unmerged upstream PR #5349; asserts `NotEqual` on purpose, worded to fail loudly rather than silently pass once #5349 merges)

**failure_attribution:** none — zero failures this round, diff-owned or otherwise. The 3 packages tracked by be-ek5o4 (cmd/bd, cmd/bd/doctor, internal/storage/dolt) — flaky/failing in be-193q1's own review-time run (94 ok/3 FAIL/23 no-test-files) — all report clean `ok` in this deployer's independent re-run (97 ok/1 no-test/22 no-test-coverprofile-format, 0 FAIL). No attribution needed since nothing is currently failing.

**Policy/lint lane (3b):**

1. `ci-pr-policy` (`make ci-pr-policy`): "check version consistency" step fails on `.githooks/commit-msg: no 'BEGIN BEADS INTEGRATION'/'END BEADS INTEGRATION' marker found` -> `be-a0dxu` | clause 3: identical symptom reproduced verbatim, matches `be-a0dxu`'s tracked condition exactly — `.githooks/commit-msg` on disk is a local, untracked dev shim, not part of the repo's committed `.githooks/` fileset; confirmed not diff-owned (`git diff --name-only origin/main...HEAD -- .githooks/` empty). Log: `be-gftj8-criterion3b-policy.log`.
2. `ci-pr-lint` (`make ci-pr-lint` → `golangci-lint`): 3x `G602: slice index out of range (gosec)` in `backend/conformance/importer_contract.go:390,392` and `relations_contract.go:672` -> `be-w4qbu` | clause 3: byte-for-byte identical (same files, same lines, same finding type/count) to the be-dy66n-gate.md precedent's (2026-09-05) documented be-w4qbu sighting; fresh-verified this round rather than cited from memory alone — `gofmt` clean, `golangci-lint` fails after 13s with exactly these 3 issues; zero path overlap freshly confirmed (`git diff --stat origin/main...1f018bc1b -- backend/conformance/` empty — this diff touches only `internal/storage/domain/{,db}/`). Log: `be-gftj8-ci-lint.log`.

**attribution_evidence:** both citations posted as `bd comment` to their respective tracker beads this gate round (be-a0dxu, be-w4qbu), each including sighting date, bead/commit, exact finding text, and clause-3 proof.

**ci_lane_run (3c):** N/A — diff touches no `.github/` or `scripts/ci/` paths, so no CI-config live-run is required. `make ci-pr-policy` and `make ci-pr-lint` were still run in full as the standard 3b policy/lint lane (see above); both are attributed-PASS with zero diff-owned findings.

**waiver_ref:** none — no waiver needed; the two policy/lint findings are cleanly attributed to pre-existing, non-diff-owned conditions with clause-3 proof and zero path overlap, and the full-suite test lane itself has zero failures at all.

**uncovered_criteria:** none

## User-visible behavior change (be-1shoq, PR #6501 review point 2)

bee-ghosttrack's review flagged a real disclosure gap, accepted here (the proposed remedy — an `ActorMatches` gate — is declined; see the PR #6501 reply for why).

On the proxied/domain backend, `bd update <id> --claim --assignee=<other>` against an **OPEN** issue now leaves a `DefaultLeaseTTL` (5 minutes, `internal/storage/issueops/lease.go:26`) lease held by the override target (`<other>`), not the claiming actor. Previously this same command deleted the lease outright, leaving a live `in_progress` claim with no lease row.

- **Practical effect:** if `<other>` never heartbeats, `bd reclaim` now reverts the assignment after the lease TTL plus grace period elapses — where previously the hold was durable (no lease row to expire).
- **Bound:** this only affects claiming an *open* issue on another actor's behalf. On an already-claimed issue, the same command fails outright with "already claimed" (`cmd/bd/update_proxied_integration_test.go:110`) before any lease logic runs.
- **Trade-off, and why the old behavior was worse:** without a lease row, the same claim is instead *permanently* unreclaimable — `ReclaimExpiredLeasesInTx`'s candidate query inner-joins `leases` (`internal/storage/issueops/lease.go:~688-693`), so a claim with no lease row is invisible to reclaim forever, not just until TTL. A time-bounded reclaim window is the safer failure mode of the two.
- **Parity note:** this is the domain/proxied backend catching up to the invariant PR #5349 (unmerged, owner-approved) establishes for the classic/wisps backend — `TestClaimWithAssigneeOverrideArmsLeaseForFinalHolder` pins the identical re-arm-for-override-target behavior there. Adopting the reviewer's proposed `ActorMatches` gate on this PR alone would make the two backends diverge in the opposite direction instead of closing the gap.

## Merge authority

This rig is a **contributor-only** participant in `gastownhall/beads` (upstream `origin` is fetch-only by design; all push/PR traffic goes through `headfork`/`prhead`, both `quad341/beads-sec003-contrib`). No rig agent — builder, reviewer, or deployer — holds merge rights on `gastownhall/beads`, and no rig agent ever runs `gh pr merge`. Per standing policy, the deployer's job for a contributor-only rig ends at a verified open, mergeable PR; this gate stops there and reports to mayor for **visibility only**, with no merge-request routed.

This follows established precedent: be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3, be-vc1m (PR #5792), be-7q688 (PR #6003), be-6iglh/be-0l89e (PR #6082), be-c8kgv (PR #6221), be-1wwre (PR #6247), be-3vzut (PR #6262), be-kqg23 (PR #6271), be-dy66n (PR #6304), be-u9scu (PR #6481), be-8sfdy (PR #6482).

## Disposition

**PASS, 7/7, no waivers.** PR [gastownhall/beads#6501](https://github.com/gastownhall/beads/pull/6501) opened, verified OPEN and MERGEABLE, authored by our own account (no external-contributor human-hold triggered). Two pre-existing, non-diff-owned policy/lint findings encountered during gate evaluation, both attributed to their exact pre-existing tracker beads (be-a0dxu, be-w4qbu) with clause-3 proof and citation comments posted this round. Full-suite test lane clean: zero failures, all 8 diff-owned tests independently confirmed by name. Reporting to mayor for visibility only; no merge-request routed, per contributor-only merge-authority carve-out.
