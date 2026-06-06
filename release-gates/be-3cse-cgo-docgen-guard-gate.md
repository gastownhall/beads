# Release Gate: be-3cse — guard generate-cli-docs.sh against CGO-binary federation drift

**Bead:** be-2r2b (deploy) → source: be-3cse (feature) → review: be-zlk7 (PASS)
**Branch:** `fix/be-3cse-cgo-docgen-guard` (fork: `quad341/beads`)
**PR:** [#4312](https://github.com/gastownhall/beads/pull/4312)
**Gate commit:** f288253ad06cba99b999298050431ca56ef87a65
**Gate evaluated:** 2026-06-06

---

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | **PASS** | be-zlk7 notes: "REVIEW VERDICT: PASS" (beads/reviewer). All HIGH findings were confirmatory, none unresolved. |
| 2 | Acceptance criteria met | **PASS** | All 4 done-when criteria from be-3cse addressed (see below). |
| 3 | Tests pass | **PASS** | All 50+ CI checks green: 20/20 embedded Dolt shards, storage domain, Nix Flake, upgrade smokes (v1.0.1–v1.0.5), PR Policy, Build Artifacts, Check doc flags freshness. Run: `gh pr checks 4312 --repo gastownhall/beads`. |
| 4 | No high-severity review findings open | **PASS** | Reviewer listed 3 HIGH findings; all were PASS verdicts — no unresolved HIGH items. |
| 5 | Final branch is clean | **PASS** | Single commit `f288253ad` on top of `origin/main`. `git log origin/main..HEAD` = 1 commit. |
| 6 | Branch diverges cleanly from main | **PASS** | `git merge-tree` output: clean merge. Only `scripts/generate-cli-docs.sh` affected. |
| 7 | Single feature theme | **PASS** | One file changed (`scripts/generate-cli-docs.sh`), one subsystem (docgen tooling guard). |

**Overall gate: PASS**

---

## Acceptance Criteria Verification (be-3cse done-when)

1. **CGO-enabled bd no longer emits federation pages** — Guard added at `scripts/generate-cli-docs.sh:42–52`. After resolving `$BD`, probes `"$BD" federation --help`; if pure-go stub (`Federation commands require CGO`) is absent, rebuilds with `CGO_ENABLED=0 -tags gms_pure_go`. CI Build Artifacts PASS confirms no federation pages in regenerated docs. ✓

2. **`LC_ALL=C ./scripts/generate-cli-docs.sh <pure-go bd>` zero churn** — PR description: "LC_ALL=C ./scripts/generate-cli-docs.sh --check /tmp/bd-pure-test → PASS (zero churn, no rebuild triggered)". CI Check doc flags freshness PASS. ✓

3. **`check-doc-flags.sh` + `check-doc-freshness.sh` pass** — CI: `Check doc flags freshness` PASS, `PR Policy (wrapper timing)` PASS. ✓

4. **Script references CGO_ENABLED=0 CI oracle** — Line comment: "Guard against a CGO-enabled bd: it exposes `bd federation` subcommands that CI never produces (scripts/ci/pr-policy.sh build_docs_binary uses env CGO_ENABLED=0 go build)." ✓

---

## Change Summary

**File:** `scripts/generate-cli-docs.sh`
**Delta:** +12 lines, 0 deletions

After the `bd` binary is resolved (from explicit arg, `$PROJECT_ROOT/bd`, or internal build), a CGO-detection probe runs:
```sh
if [ -x "$BD" ] && ! "$BD" federation --help 2>&1 | grep -q "Federation commands require CGO"; then
    # CGO-enabled bd detected: rebuild pure-go for CI-consistent docs
```
This prevents the recurring docgen-cgo-binary-reintroduces-federation failure pattern (PRs #3710, #4055, #4153).
