# Release gate — be-gd3v (Fix: explicit Config.ServerPort must outrank ambient BEADS_DOLT_SERVER_PORT env var)

**Date:** 2026-08-18
**Deployer:** beads/deployer
**Bead (deploy):** be-gd3v — Deploy Review: Fix: explicit Config.ServerPort must outrank ambient BEADS_DOLT_SERVER_PORT env var
**Source bead:** be-v2hy — closed, review verdict: pass
**Source commit:** `2dbcbd7f2a8782527fe4b496d497970f21f414be` (provenance branch `builder/be-wf9a.1`, review bead be-v2hy)
**Branch:** `deploy/be-gd3v-gate` (isolated, cut fresh at the reviewed SHA — never the shared `builder/be-wf9a.1` branch)
**Base:** `origin/main` @ `7505e173f` ("chore(release): forward-port v1.2.2 to main (#5782)")
**Merge-base:** `7505e173f` — identical to origin/main tip; clean fast-forward, 0 self-rebase needed
**Merge-tree simulation:** `git merge-tree --write-tree origin/main 2dbcbd7f2` → tree `f102e1300`, exit 0, **zero conflicts**
**PR:** https://github.com/gastownhall/beads/pull/5835 (`quad341/beads-sec003-contrib:deploy/be-gd3v-gate` → `gastownhall:main`), state OPEN, `headRefOid` = `2dbcbd7f2a8782527fe4b496d497970f21f414be` (matched the gated SHA exactly, no drift) **as of this gate run** — see *Post-gate history* below, `mergeable: MERGEABLE`

## Verdict: PASS — all 7 criteria clear

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-v2hy: verdict `pass`, closed with reason `pass`. Style clean (gofmt, go vet, golangci-lint incl. Windows cross-lint: 0 issues on the round-2 diff). Security clean (no blocking findings — one non-blocking design observation on env-vs-config.yaml precedence, explicitly assessed and not treated as a defect). |
| 2 | Acceptance criteria met | PASS | be-wf9a's architecture decision (flip precedence: caller-explicit `ServerPort` outranks ambient env, paired with `PortSource` propagation so the fill-only-if-zero convention already used elsewhere in `applyConfigDefaults` extends to the env block) matches the shipped diff exactly — confirmed by reading both the decision doc and the reviewer's line-level trace of `open.go`/`store.go`, not by title alone. |
| 3 | Tests pass | PASS | `test_cmd`: `scripts/ci/pr-core.sh` (documented CI-equivalent), run fresh by the reviewer rather than trusted from builder self-report. Full suite: 93 packages ok, 0 FAIL, 0 SKIP, exit 0, 231s, with `-race`. All 4 diff-owned subtests under `TestApplyResolvedConfig` individually confirmed PASS (targeted `-v` rerun: 10 PASS, 0 FAIL, 0 SKIP). No diff-owned skip anywhere — criterion 3a's pre-existing-failure attribution path was not needed. |
| 3b | Policy/lint lane | PASS | `make ci-pr-lint` (`BD_LINT_NEW_FROM_MERGE_BASE=origin/main`): 0 issues, both native and `GOOS=windows/CGO_ENABLED=0` targets. |
| 4 | No HIGH-severity findings open | PASS | be-v2hy security_findings: "none blocking." The one recorded design observation (env no longer outranks `config.yaml`/`metadata.json`-sourced ports either, a real but intentional and defensible behavior change per the reviewer's own analysis) is explicitly flagged non-blocking, not a severity finding. |
| 5 | Final branch is clean | PASS | `git status --short` on `deploy/be-gd3v-gate` at `2dbcbd7f2`: no tracked-file changes. (Untracked leftovers — `release-gates/be-hi97*`, `release-gates/be-k9js*`, `release-gates/be-uoat*`, `scripts/rebase-resolve-lib.sh` — are per-session provisioned tooling from prior closed work, not part of this branch's history, and excluded from this commit.) |
| 6 | Branch diverges cleanly from main | PASS | Merge-base equals `origin/main` tip exactly (0 commits behind). `git merge-tree --write-tree origin/main 2dbcbd7f2` succeeds with a single merged tree, exit 0, no conflict markers. No self-rebase needed. |
| 7 | Single feature theme | PASS | `git diff --stat origin/main...2dbcbd7f2`: 5 files, all within `internal/doltserver` and `internal/storage/dolt` — `doltserver.go`, `open.go`, `open_test.go`, `store.go`, `store_unit_test.go`. 234 insertions(+), 19 deletions(-). One theme throughout: `ServerPort` precedence resolution and its test coverage (including the round-2 fix for the propagation gap round-1 review caught, `be-9tju`, which shares the same theme and is cited directly in two of the four commits).

## Disposition

- **PR opened, not merged.** `gastownhall/beads` is a contributor-only repo (no push/merge rights); the deployer's job ends at the open PR regardless of gate outcome — no merge-request routed, no waiting on mayor.
- Gate is a clean PASS with no waivers, no substitutes, and no open blockers — unlike the sibling `be-vc1m` deploy currently parked on this rig, this diff's criterion 3 has a real, fresh, fully-green test result.

## Post-gate history

This gate's verdict describes `2dbcbd7f2` and nothing after it. The branch head
has advanced since, so the `headRefOid` line above is a snapshot rather than a
standing claim — the commit that added *this file* was itself the first to move
the head past the gated SHA, which makes the unqualified "no drift" reading
self-contradictory. Recorded rather than silently corrected, since the PASS is
still accurate about what it examined:

| After the gate | What it is |
|---|---|
| `b15b80077` | `docs(release-gates)`: added this gate document. Docs only; no code in the gated diff changed. |
| round-3 review response | Addresses PR #5835's `CHANGES_REQUESTED` review: `PortSource` propagation at four previously unthreaded `dolt.Config` sites, the CHANGELOG entry criterion 2's non-blocking observation foreshadowed, and this note. Code changes here are **not** covered by the criteria walk above. |

Re-gating is the deployer's call, not the investigator's; this section exists so
that call is made against an accurate record.
