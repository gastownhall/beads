# Release gate — claim+assignee-override leaves a live claim with no lease row (be-wgxq8)

- **Builder bead (CLOSED):** be-z4ows — root-cause investigation + TDD fix
- **Review bead (CLOSED, verdict PASS):** be-dovpk
- **Commit shipped:** `d2de76797f9ad007c3045073dd0422362503cbe6`, 4 files changed
  (+271/-11) over `origin/main` `d801ec43d` (zero staleness — merge-base of
  the reviewed commit with `origin/main` is exactly `origin/main`'s tip)
- **Branch:** `deploy/be-wgxq8-gate` on `fork` (`quad341/beads`), cut directly
  from the reviewed SHA
- **Evaluated:** 2026-08-04 by beads/deployer

## Scope

Fixes a lease/claim invariant violation in `internal/storage/issueops`:
`bd update --claim --assignee=X` armed a lease for the claiming actor and
then, in the same transaction, treated the assignee patch as an ownership
transfer and deleted that same lease — leaving `status=in_progress` with an
assignee but **no lease row**. This broke the documented `lease row ⇔ live
claim` invariant (`lease.go:151-167`) two ways: (1) `bd heartbeat` for the
true owner fell into the rows==0 self-heal branch and printed a misleading
"already claimed by X" refusal that names the wrong actor, and (2)
`ReclaimExpiredLeasesInTx`'s inner join on `leases` made a leaseless claim
permanently invisible to `bd reclaim`, so a crashed worker's claim could
never be reverted.

**Fix (`internal/storage/issueops/update.go`, `execution.go`):**

- `updateIssueInTx` gained an `isClaim bool` param. New
  `UpdateClaimedIssueInTx` wrapper calls it with `isClaim=true`; existing
  `UpdateIssueInTx`/`UpdateIssueWithoutEventInTx` pass `isClaim=false`
  (byte-identical behavior on the non-claim path).
- New helper `finalAssigneeIfStillClaimed(oldIssue, updates)` — deliberately
  does not share code with `ManageLeaseOnUpdate` (that function's pinned
  clear-only contract, bd-9hpgf/GH#4716, doesn't fit: its `sameClaim` check
  requires `newAssignee == oldIssue.Assignee`, never true for this transfer
  since `oldIssue.Assignee` is the claimant, not the override target).
- Rewritten `clearLease` consumption block: when `isClaim` is true and the
  patch still leaves the issue `in_progress` with a non-empty assignee, calls
  `UpsertLeaseInTx(ctx, tx, id, holder, now, leaseTTL(ctx))` — arming the
  lease for the final holder — instead of `DeleteLeaseInTx`. Every other
  `clearLease` path (non-claim updates, or claims that end up unclaimed)
  still deletes exactly as before.
- `execution.go`'s `ExecuteUpdate` dispatches to `UpdateClaimedIssueInTx`
  when `attempt.Claim` is true, `UpdateIssueInTx` otherwise.

Out of scope (per the builder bead, not gate failures): the unrelated
5-minute claim-revert reports (gm-j3zbd/gm-tck3h, actually caused by
`gascity`'s `releaseOrphanedPoolAssignments`, filed separately as ga-van9d5);
optional cosmetic improvement to the misleading error copy at
`lease.go:325-327`.

**Incidental file in the diff:** `.claude/settings.json` (+21/-? lines) —
pure key-reordering plus one new `PreToolUse` hook entry
(`mol-attach-guard.sh`, matcher `^Bash$`), bundled in via commit `337fa254f
witness: salvage uncommitted work`. Investigated: this is session/infra
hygiene from an auto-commit, not a second feature or a scope/security
concern — noted here, not treated as a gate failure (see criterion 7).

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 0 | Pre-flight: not already merged | **PASS** | `gh api` search for an existing PR against this commit: none found before this evaluation. |
| 1 | Review PASS present | **PASS** | be-dovpk recorded `verdict: pass`, closed with `Close reason: pass`. Style findings: none (gofmt clean, golangci-lint 0 issues). Security findings: none — explicit OWASP Top 10 walkthrough (injection, authz, coercion, logging/PII, dependencies) in be-dovpk's notes, concluding no new attack surface (parameterized SQL, no new authz boundary, backend-only internal bugfix). |
| 2 | Acceptance criteria met | **PASS** | All 4 done-when items from be-z4ows verified against code + tests (see "Acceptance" below). |
| 3 | Tests pass | **PASS** | Independently re-run by me, not just trusted from the reviewer. See "Tests run" below — full documented suite clean (0 FAIL), all 4 diff-owned tests independently confirmed PASS by name, supplementary embedded-Dolt conformance suite independently re-run and clean, container-Dolt pre-existing failure independently reproduced and confirmed unrelated. |
| 4 | No HIGH-severity findings open | **PASS** | Zero HIGH/blocker/major findings in be-dovpk. |
| 5 | Final branch is clean | **PASS** | `git status --short` on `deploy/be-wgxq8-gate` (cut from the reviewed SHA) is empty. |
| 6 | Branch diverges cleanly from main | **PASS** | `git merge-base origin/main HEAD` == `origin/main`'s current tip `d801ec43d` exactly — the reviewed commit already sits directly on top of latest `origin/main`; no staleness, no rebase needed. |
| 7 | Single feature theme | **PASS** | All substantive changes (execution.go, update.go, new test file) are one cohesive fix to the claim/assignee-override lease bug. The incidental `.claude/settings.json` change is non-functional session-hygiene noise from an auto-commit (see Scope), not a second feature. |

## Acceptance (per be-z4ows done-when list)

| Criterion | Status | Evidence |
|---|---|---|
| `bd update --claim --assignee=X` leaves a lease row with holder = X | ✓ | `TestClaimWithAssigneeOverrideArmsLeaseForFinalHolder` — independently confirmed PASS by name. |
| Bare-claim (arm C) and hand-doled (arm D) behaviors unchanged | ✓ | `TestPlainAssigneeUpdateWithoutClaimStillClearsLease` (arm D: no `--claim` ⇒ no lease row, unchanged) — independently confirmed PASS by name. Arm C (bare claim, no assignee) is unaffected by this diff since `ManageLeaseOnUpdate` still returns `false` when no assignee key is present in `updates`; covered by the pre-existing, untouched `TestManageLeaseOnUpdate` table (8 subtests, all still green per be-z4ows notes). |
| The claim is visible to `bd reclaim` once stale | ✓ | `TestReclaimSeesIssueAfterClaimAssigneeOverride` — independently confirmed PASS by name. |
| `bd heartbeat --actor <assignee>` takes the normal UPDATE path, not the rows==0 self-heal branch | ✓ | `TestHeartbeatAfterClaimAssigneeOverrideUsesNormalPath` — independently confirmed PASS by name. |
| Respect `lease.auto`/disarm semantics (built on 9123e0612) | ✓ | Reviewer confirmed by code read; no lease-auto/disarm config path is bypassed by the new `clearLease` branch — it only changes which lease-mutation function is called, not whether one is called. |

## Tests run (independently, by me, on `deploy/be-wgxq8-gate`)

| Test | Result | Notes |
|------|--------|-------|
| `TEST_COVER=1 ./scripts/test.sh` (documented Makefile `test:` target — same command the reviewer used) | **PASS** | Every package reports `ok`, zero `FAIL` lines, exit code 0, total coverage 39.3%. `.test-skip` confirmed present but empty (comment-only) — zero pattern-based skips active for this run. |
| `go test -v -run '^(TestClaimWithAssigneeOverrideArmsLeaseForFinalHolder\|TestHeartbeatAfterClaimAssigneeOverrideUsesNormalPath\|TestReclaimSeesIssueAfterClaimAssigneeOverride\|TestPlainAssigneeUpdateWithoutClaimStillClearsLease)$' ./internal/storage/issueops/...` (diff-owned tests, resolved by name) | **4 PASS, 0 FAIL, 0 SKIP** | All four map 1:1 to the bug's 4-point spec; run directly by me, not taken on the reviewer's word. |
| `BEADS_TEST_EMBEDDED_DOLT=1 go test -v -timeout 5m ./internal/storage/embeddeddolt/...` (supplementary real, non-mocked conformance suite) | **576 PASS, 0 FAIL, 0 SKIP** | Independently re-run by me (not just trusted from be-dovpk's report) — real in-process Dolt engine, 242.6s. Exact match to be-dovpk's own reported count (576/0/0), including `TestConformance/Claim`, `ClaimIdempotent`, `ClaimAlreadyClaimed`, `ClaimOpenForeignAssignee`, `ClaimNotClaimable`, `ClaimReadyIssue(+LabelFilters)`, `HeartbeatRenewsLease`, `ReclaimExpiredLease`, `ReclaimSkipsFreshLease`, `ReclaimScoped`. |
| `DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true go test -v -timeout 3m ./internal/storage/dolt/...` (container-backed suite) | **FAIL — pre-existing, unrelated, independently reproduced** | `TestMain` FATALs before any subtest runs: `schema migration: pending schema migrations alter pre-existing dirty tables ... (gastownhall/beads#4566)`. This is a local dev-DB dirty-schema-state issue, already tracked upstream. Confirmed unrelated to this diff: the two commits on this branch (`d2de76797f9`, `337fa254f`) touch only `update.go`/`execution.go`/the new test file and `.claude/settings.json` — no schema or migration code. I reproduced the identical FATAL myself (same error text, same container start sequence) rather than taking the reviewer's account on trust. Not a blocker: the documented CI-equivalent command (`./scripts/test.sh`) does not exercise this path in this environment at all (no container tests run without an explicit `DOCKER_HOST` override), so it isn't part of the primary pass/fail signal for criterion 3, and the diff-owned tests plus the embedded-engine conformance suite already provide real (non-mocked) coverage of the same claim/heartbeat/reclaim contract. |

## Findings from review (no action required)

From be-dovpk: none open. Zero style findings, zero security findings.

## Push target

`origin` (`gastownhall/beads`) push is explicitly disabled
(`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`); `fork`
(`quad341/beads`) accepts. PR opens cross-repo against
`gastownhall/beads:main` with head `quad341:deploy/be-wgxq8-gate`.

`gastownhall/beads` is an upstream, contributor-only repo — matching the
precedent set by be-1gd16 (PR #5343), this job ends at PR-open. No
merge-request is routed to mayor; merge authority for this upstream repo
belongs to its own maintainers, not this fleet.

## Verdict

**PASS (7/7 + pre-flight clear)** — push the gate-file commit to `fork`,
open the PR.
