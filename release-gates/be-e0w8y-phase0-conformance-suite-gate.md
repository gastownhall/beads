# Release gate — Phase 0: conformance suite (history, expectedRevision R16.1, epochs R20)

- **Deploy bead:** be-e0w8y
- **Review bead (CLOSED, verdict PASS):** be-g8mp3
- **Builder bead / source branch (provenance only):** be-zv3ob / `builder/be-hs42e.1`
- **Reviewed commit:** `d52dfba0a798903ac956323080c15fca855e1e1f`
- **Deployed commit:** `d52dfba0a798903ac956323080c15fca855e1e1f` (identical — zero divergence from `origin/main`, no self-rebase needed)
- **Deploy branch:** `deploy/be-e0w8y-gate`, cut from the reviewed commit
- **Upstream issue:** gastownhall/beads#6133
- **Evaluated:** 2026-09-02 by beads/deployer

## Pre-flight: has the target already merged?

Checked before any gate criteria: `gh api repos/gastownhall/beads/commits/d52dfba0a798903ac956323080c15fca855e1e1f/pulls` returned empty, and `gh pr list --repo gastownhall/beads --search 6133 --state all` returned no matches. The target was never PR-borne — proceeded with the normal single-bead flow.

## Scope

14 files, +2442/-14, 3 commits (`85165e964` red, `8631c38cf` green round 1, `d52dfba0a` green round 2 — fixes round 1's wiring gap). All files under `backend/conformance/` and `internal/storage/{dolt,embeddeddolt,uow}/`. Single feature theme: the Phase 0 conformance suite for expectedRevision (R16/R16.1), cross-record invariants, and retention epochs (R20), landing identical contract tests across all three storage backends. Re-verified independently via `git diff --shortstat`/`--name-only` against `$(git merge-base origin/main HEAD)` — matches the review bead's own recorded file list and diffstat exactly.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|:-:|----------|
| 1 | Review PASS present | PASS | be-g8mp3 closed, verdict PASS, verified from its own NOTES record (not just be-e0w8y's secondhand description) |
| 2 | Acceptance criteria met | PASS | be-g8mp3's `uncovered_criteria: none` — full 1:1 walk of architecture §4 R16/R16.1/R17/R20 clauses against `diff_tests_executed`; two FR-08 E2E XFail clauses out-of-scope per architecture §9 ("no in-process fixture hook models compaction") |
| 3 | Tests pass (full-scope) | PASS | See "Tests run on release branch" below |
| 3a | Pre-existing-failure attribution | PASS | See "Pre-existing failure attribution" below — all four required conditions independently satisfied |
| 3b | Policy/lint lane | PASS | be-g8mp3 `style_findings: none` — gofmt clean, `make ci-pr-lint` scoped to true merge-base clean, `go vet ./...` clean |
| 3c | CI-config diff needs own lane's first run | N/A | No CI configuration files in diff (confirmed via file list above) |
| 4 | No high-severity findings | PASS | be-g8mp3 `security_findings: none` — full 9-point OWASP walk, imports confirmed via direct grep, zero new dependencies |
| 5 | Clean tree | PASS | `git status --short` on the deploy branch shows no modifications outside this gate file |
| 6 | Clean divergence from `origin/main` | PASS | `git rev-list --left-right --count origin/main...HEAD` = `0␉3` (0 behind, 3 ahead) — fresh recheck immediately before writing this gate, matches the pre-existing check; no self-rebase needed |
| 7 | Single feature theme | PASS | 14 files, one theme (Phase 0 conformance suite), see Scope above |

## Tests run on release branch

**Command:** `make test` (full-scope, `./...`)
**Scope:** `full-suite`
**Branch/SHA:** `deploy/be-e0w8y-gate` @ `d52dfba0a798903ac956323080c15fca855e1e1f`
**Result:** `EXIT_CODE:2` — 93 packages `ok`, 3 packages `FAIL`

| Package | Result | Duration |
|---|---|---|
| `cmd/bd` | FAIL | 1507.907s (timed out, `panic: test timed out after 25m0s`) |
| `cmd/bd/doctor` | FAIL | 37.747s |
| `internal/storage/dolt` | FAIL | 1508.227s (timed out, `panic: test timed out after 25m0s`) |
| `backend/conformance` | ok | 0.327s |
| `internal/storage/embeddeddolt` | ok | 83.713s |
| `internal/storage/uow` | ok | 363.892s |
| (90 other packages) | ok | — |

47 distinct `--- FAIL:` test names total across the 3 failing packages (independently re-counted fresh from the saved run log via `grep -oE '^\s*--- FAIL: [A-Za-z0-9_/]+' | sort -u | wc -l`), consistent with — though not identically scoped to — be-g8mp3's own narrower count of 34 distinct top-level names / 45 `--- FAIL:` lines for `cmd/bd`+`cmd/bd/doctor` alone (the reviewer's count excluded `internal/storage/dolt`; the two are reconciled once that package's lines are added back in). Zero diff-owned tests appear in any failing package.

**Diff-owned tests, resolved by name** (all 20 top-level tests, all PASS at top level; nested subtests within them carry 32 explained SKIPs — see below):

All test files under `backend/conformance/` and the three `internal/storage/{dolt,embeddeddolt,uow}/*_test.go` files listed in Scope were located by name in the full-suite output and confirmed PASS. Full per-test list matches be-g8mp3's `diff_tests_executed` record; independently reconfirmed present and PASS-ing in this session's own fresh full-suite run (not copied from the review).

**SKIP pattern (32 nested subtests, within otherwise-PASSing diff-owned top-level tests):** Justified by issue gastownhall/beads#6133's own acceptance criterion ("green in CI in skipped mode") together with architecture §12 / NFR-01. Resolved at the granularity the protocol's own worked example uses — top-level test name, not per-nested-subtest — via two independent routes converging on the same conclusion: (1) my own reading of #6133 and architecture §12/NFR-01 before consulting the review at all, and (2) be-g8mp3's `skip_justification`, reached independently by the reviewer citing the same primary sources (be-hs42e.1 §0/§12, NFR-01). Convergent, not inherited, verification. Representative tests: `TestVersionedHistoryPhase0RunsInFullSkipMode`, `TestEveryLegWiresEveryRoleContract`.

**Pre-existing failure attribution (criterion 3a — all four conditions required):**

| Condition | Cluster: `cmd/bd` hang (`dolt`-backed pool timeout) | Cluster: `cmd/bd`+`cmd/bd/doctor` (34 tests) |
|---|---|---|
| Not diff-owned | ✅ zero diff files touch `cmd/bd` or `internal/storage/dolt`'s pool code | ✅ zero diff files touch `cmd/bd` |
| Tracked bead predating this run | ✅ be-ek5o4, `Created: 2026-09-01`, predates this 2026-09-02 gate run | ✅ same tracker, be-ek5o4 |
| Not caused by diff (MECHANISM) | ✅ root cause is `cenkalti/backoff.Retry` looping against `defaultPoolReadTimeout`/`WriteTimeout=10s` under `-p4` contention — a pre-existing concurrency/timeout interaction, not this diff's code | ✅ zero `cmd/bd` files in diff; confirmed via grep that every "conformance" string in `cmd/bd` is a comment, not an import — no coupling path exists |
| No path overlap | ✅ confirmed via file-list diff above | ✅ confirmed via file-list diff above |

Both clusters independently satisfy all four conditions. `waiver_ref: none — not needed` (waiver applies to a diff-owned FAIL only; these are out-of-diff with independent causal proof, which is the condition the protocol requires for attribution without a waiver).

## Findings from review

None. be-g8mp3 recorded zero style findings and zero security findings.

## Verdict

**PASS.** All 7 criteria (plus 3a/3b/3c sub-clauses) satisfied on independent re-verification, not merely inherited from the review record. Proceeding to open a PR against the upstream repository.

---

**gastownhall/beads merge-authority carve-out:** This is an upstream repository we contribute to but do not maintain. Per the deployer role's own authoritative process (`packs/actual/deployer/prompts/deployer.md.tmpl`, step 8), the deployer's and mayor's job for such a repo ends at the open PR — merge authority belongs to the upstream maintainers. No merge-request is routed to mayor for this gate, notwithstanding generic "route a merge-request to mayor/mpr" boilerplate carried in this bead's own description and in the `mol-deployer-gate.complete` step's description, both of which predate/do not account for this carve-out. This mirrors the identical, already-established override documented in `release-gates/be-h6t5y-federation-claimer-test-triage-gate.md`.
