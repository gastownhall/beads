# Release gate: be-iprk — bd show --json count-only + iter flags (PR #4010)

- **Bead:** be-iprk (Review: be-ijck6q bd show --json count-only + iter flags, PR #4010)
- **Branch:** `feat/be-ijck6q-show-json-iter` (HEAD: `0a19244ca`)
- **Evaluated:** 2026-05-19 by beads/deployer

## What ships

- `bd show --json` default now emits `dependent_count`/`dependency_count`/`comment_count` (O(1) COUNT queries) instead of full slices
- `--include-dependents`: streams via `IterDependentsWithMetadata`, shallow-copies each item
- `--include-comments`: streams via `IterIssueComments`
- Storage layer: `CountDependents`, `CountDependencies`, `CountIssueComments`, `CountIssues`, `CountEvents` + Dolt/EmbeddedDolt implementations; PG stubs; OTel telemetry wrappers

## Gate criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | **PASS** | be-iprk notes: "VERDICT: pass... DECISION: PASS — label needs-deploy." 2 LOW non-blocking findings documented; no HIGH findings. |
| 2 | Acceptance criteria met | **PASS** | Default JSON output uses COUNT(*) queries; `--include-dependents` and `--include-comments` flags stream via Iter; storage layer additions complete across Dolt/EmbeddedDolt/PG stubs |
| 3 | Tests pass | **PASS** | CI 40/40 SUCCESS on PR #4010 |
| 4 | No high-severity review findings open | **PASS** | 0 HIGH findings; 2 LOW findings in be-iprk: (1) shallowDependentsForJSON defined but not called in production path; (2) defer iter.Close() function-scoped accumulation — both non-blocking |
| 5 | Final branch is clean | **PASS** | `git status` clean |
| 6 | Branch diverges cleanly from main | **PASS** | No conflicts against origin/main |

## Verdict: PASS
