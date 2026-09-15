# Release gate — be-fghsd (dep add cross-store external-ref fix)

**Date:** 2026-09-15
**Deployer:** beads/deployer
**Bead (deploy):** be-fghsd — needs-deploy: "dep add stores a cross-store target as a bare external ref, then readers drop it (from:gm-ri3xl0)"
**Review bead:** be-xxw1r — closed, review verdict PASS
**Pre-existing-failure tracker:** be-ey0oy — open, "TestFindBeadsRepoRoot_WorktreeFallback" cluster (root cause now identified and fixed by mayor — see below)
**RED commit:** `a5e89bc6cb900cb77c3e3718f471a58643b6ea21`
**GREEN / reviewed / DEPLOY_SHA:** `fc2f89c5f238ccb44df0d4912ffbf0df3a301895`
**Merge-base:** `f56632adcfabed7da6ed0aabe4e760066b472c46` ("perf(storage): stop burning two wasted MySQL sessions per bd invocation (wy-s8ytnw) (#6122)")
**Scope:** `cmd/bd/dep.go` + `cmd/bd/dep_add_target_test.go` only; no Postgres/pack material.

## Verdict: PASS — all 7 criteria clear

Criterion 3/3a was previously escalated to the mayor (see git history of this
file / be-fghsd notes for the original escalation writeup). Mayor traced the
sole failing test, `TestFindBeadsRepoRoot_WorktreeFallback`, to an
environmental cause — a leaked `/tmp/.beads` directory produced by stray `bd`
telemetry under `HOME=/tmp` — proved it, and removed it. Mayor's explicit
instruction (on be-fghsd's notes, independently corroborated via mail
`gm-wisp-rrjnd3`): *"If it is clean, criterion 3 PASSES outright with no
attribution, so the clause-4 question never arises; finish the gate as
normal."*

This session independently re-ran the full test suite twice at the exact
DEPLOY_SHA, from scratch, in the isolated-test-run wrapper (never touching the
shared live `/home/jaword/projects/beads/.beads` workspace). Result: clean.
Criterion 3 PASSES outright. The clause-4 added-test-load-guard analysis from
the original escalation is superseded, not overturned — it was a correct
analysis of a failure that, it turns out, was never attributable to the diff
in the first place; the environmental fix mooted the question before the
guard ever had to bind.

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from BASE_REF | PASS | Clean ancestry, no conflicts, confirmed against current origin/main tip. No self-rebase needed. |
| 1 | Reviewer PASS verdict present | PASS | be-xxw1r closed by beads/reviewer, verdict PASS. Independently re-verified this session, not taken at face value. |
| 2 | Acceptance criteria met | PASS | `resolveUnresolvedDepTarget` (cmd/bd/dep.go) now requires a target that fails local/routed resolution to be a well-formed `external:<project>:<capability>` ref, or refuses by name naming the unresolved target — closes both bug shapes (bare foreign bead IDs silently accepted; `type:id`-shaped positional args misread as a foreign-store prefix). Independently re-verified against the diff, not just the reviewer's claim. |
| 3 | Tests pass (full-suite, documented command) | **PASS** | See "Criterion 3/3a" below. |
| 4 | No HIGH-severity findings open | PASS | No HIGH/severity/finding keyword hits anywhere in be-xxw1r's review notes. be-gmdx5's self-review found only pre-existing, out-of-scope lint issues. |
| 5 | Feature branch clean | PASS | No uncommitted changes at DEPLOY_SHA. |
| 7 | Single feature theme | PASS | Exactly 2 commits (RED `a5e89bc6c`, GREEN `fc2f89c5f`); no `rollup-ship` label. |

## Criterion 3/3a — full detail (re-run this session at mayor's direction)

**Test command:** `make test` (→ `TEST_COVER=1 ./scripts/test.sh`, which
defaults `PACKAGES=("./...")` — the true full-suite scope: 97 packages,
including many `internal/*` packages not reachable by a `./cmd/bd`-only
invocation). Run via the mandated isolation wrapper,
`packs/actual/all/scripts/isolated-test-run.sh -- bash -c "make test"`, in a
fresh `git clone --shared` checkout of DEPLOY_SHA — never in the shared live
worktree-linked `.beads` store.

Corrects a scope mislabeling in the original escalation writeup, which had
cited `go test ... ./cmd/bd` (single-package) as `full-suite`. Confirmed via
`Makefile`'s `test:` target and `scripts/test.sh`'s own default that the true
full-suite command is `make test` / `./...`.

**Run 1 — non-verbose, fast confirmation:**
- 97/97 packages report `ok`; zero anywhere: `FAIL`, `SKIP`, `panic:`, `TRIPWIRE`.
- `cmd/bd` package: `ok  	github.com/steveyegge/beads/cmd/bd	360.288s	coverage: 33.4% of statements` (real, non-cached duration — `-covermode=atomic -coverprofile` is never cache-eligible).
- Footer: `Total coverage: 39.4%`.

**Run 2 — `TEST_VERBOSE=1`, for explicit by-name PASS evidence (decisive evidence):**
- Totals: `TOTAL_PASS=6362`, `TOTAL_FAIL=0`, `TOTAL_SKIP=2106`, `rc=0`.
- `cmd/bd` package: `ok  	github.com/steveyegge/beads/cmd/bd	341.257s	coverage: 33.4% of statements`.
- Neither the previously-failing test nor any diff-owned test appears in any SKIP line (`grep "SKIP" | grep -iE "ResolveUnresolvedDepTarget|FindBeadsRepoRoot_WorktreeFallback"` → no matches).

**Previously-blocking test, explicitly re-verified by name:**
```
=== RUN   TestFindBeadsRepoRoot_WorktreeFallback
--- PASS: TestFindBeadsRepoRoot_WorktreeFallback (0.01s)
```
Confirms mayor's environmental fix (leaked `/tmp/.beads` removed) is
effective — independently re-verified, not taken on trust.

**Diff-owned tests, explicit by-name evidence:**
```
--- PASS: TestResolveUnresolvedDepTarget (0.00s)
    --- PASS: TestResolveUnresolvedDepTarget/bare_foreign-looking_bd_ID_is_rejected,_not_silently_passed_through (0.00s)
    --- PASS: TestResolveUnresolvedDepTarget/type:id-shaped_positional_arg_is_rejected,_not_silently_misparsed (0.00s)
    --- PASS: TestResolveUnresolvedDepTarget/well-formed_external_ref_is_still_accepted (0.00s)
    --- PASS: TestResolveUnresolvedDepTarget/malformed_external-looking_ref_missing_capability_is_rejected (0.00s)
    --- PASS: TestResolveUnresolvedDepTarget/empty_target_is_rejected (0.00s)
```

```
test_cmd_scope: full-suite
diff_tests_executed: TestResolveUnresolvedDepTarget -> PASS, plus 5 subtests all PASS by name (listed above)
previously_blocking_test: TestFindBeadsRepoRoot_WorktreeFallback -> PASS (0.01s) — environmental fix by mayor confirmed effective
waiver_ref: none — no attribution needed, criterion 3 PASSES outright
```

**3a — pre-existing-failure attribution test: N/A.** The test no longer
fails at DEPLOY_SHA. No attribution analysis is needed or performed; the
clause-4 added-test-load-guard question from the original escalation is moot
(mayor's own ruling: "the clause-4 question never arises").

- **3b — policy/lint lane: PASS.** Full policy-lane log (build-tag policy, go-install guidance, version consistency across 15 files, doc-flags check with 1 pre-existing out-of-scope WARN, doc freshness markers, testing.Short boundaries, workapi frontend boundary, no `.beads/issues.jsonl` changes, openapi spec gate) — all PASS/clean, exit 0, 0 TRIPWIRE.
- **3c — CI-config lane: n/a.** No `.github/workflows/*.yml` files touched by this diff.

## Hand-off

Gate PASSES. `gastownhall/beads` is a contributor-only repo (not one we
maintain) — proceeding to cut `deploy/be-fghsd-gate` at DEPLOY_SHA, push, and
open a PR. Per role-prompt step 8, the job ends at the open PR on a
contributor-only repo: no merge-request is routed to the mayor, no
deploy-clearance status is posted. bd `be-fghsd` will be closed noting the PR
URL and that merge is upstream maintainers' call.
