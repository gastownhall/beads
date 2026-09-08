# Staged-for-Removal Manifest

Reviewed: 2026-05-08

This directory preserves docs that no longer meet the active-doc evidence bar.
Do not restore a file wholesale. Rescue only the specific paragraphs that are
verified against current CLI behaviour, tests, or used code, and move them into
the canonical doc named in the rescue criteria.

| Original path | Staged path | Reason staged | Evidence gap | Rescue criteria |
|---|---|---|---|---|
| `docs/DOLT-BACKEND.md` | `staged-for-removal/DOLT-BACKEND.md` | Duplicates `docs/DOLT.md` and contains stale reference details. | Env/config examples include unsupported or superseded names such as `BEADS_DOLT_SERVER_PASS`; backend overview duplicated the canonical guide. | Move verified backend details into `docs/DOLT.md`; keep this path as a pointer only. |
| `docs/README_TESTING.md` | `staged-for-removal/README_TESTING.md` | Overlaps `docs/TESTING.md` and `docs/TESTING_PHILOSOPHY.md`. | Direct `go test` workflow and fast/integration split are not the current canonical agent workflow. | Move any verified strategy details into `docs/TESTING.md` or `docs/TESTING_PHILOSOPHY.md`. |
| `docs/RELEASING.md` | `staged-for-removal/RELEASING.md` | Duplicates root `RELEASING.md` and had diverged. | Release process is maintained at the repository root; duplicated steps drift. | Move any missing verified step into root `RELEASING.md`; keep `docs/RELEASING.md` as a pointer. |
| `docs/GETTING_STARTED_ANALYSIS.md` | `staged-for-removal/GETTING_STARTED_ANALYSIS.md` | Time-stamped one-shot analysis report, not active user guidance. | Claims describe a 2026-04-05 audit state rather than current product behaviour. | Convert still-relevant findings into active docs or beads issues. |
| `docs/audit-sync-mode-complexity.md` | `staged-for-removal/audit-sync-mode-complexity.md` | Time-stamped audit report, not active architecture or behaviour guidance. | No freshness mechanism; describes a point-in-time complexity audit. | Move verified durable architecture decisions into `docs/ARCHITECTURE.md`, `docs/DOLT.md`, or an ADR. |
| `docs/pr-752-chaos-testing-review.md` | `staged-for-removal/pr-752-chaos-testing-review.md` | PR-specific review notes. | Tied to a historical PR, not current testing policy. | Move any still-current testing policy into `docs/TESTING.md` or `docs/TESTING_PHILOSOPHY.md`. |
| `docs/dev-notes/ERROR_HANDLING_AUDIT.md` | `staged-for-removal/dev-notes/ERROR_HANDLING_AUDIT.md` | Historical audit note. | File/line-specific audit state is stale by default. | Move verified current patterns into `docs/ERROR_HANDLING.md`. |
| `docs/dev-notes/MAIN_TEST_CLEANUP_PLAN.md` | `staged-for-removal/dev-notes/MAIN_TEST_CLEANUP_PLAN.md` | Resolved cleanup plan. | Tracks completed cleanup rather than active guidance. | Move durable testing policy into `docs/TESTING.md`. |
| `docs/dev-notes/MAIN_TEST_REFACTOR_NOTES.md` | `staged-for-removal/dev-notes/MAIN_TEST_REFACTOR_NOTES.md` | Resolved refactor notes. | The file marks itself resolved and no longer owns a current seam. | Move durable lessons into `docs/TESTING_PHILOSOPHY.md`. |
| `docs/dev-notes/MANUAL_GITHUB_GIT_REMOTE_TEST.md` | `staged-for-removal/dev-notes/MANUAL_GITHUB_GIT_REMOTE_TEST.md` | Manual test log. | Time-bounded manual procedure without a current release gate. | Move repeatable steps into `docs/TESTING.md` or an automated test issue. |
| `docs/dev-notes/TEST_SUITE_AUDIT.md` | `staged-for-removal/dev-notes/TEST_SUITE_AUDIT.md` | Historical test-suite audit. | Snapshot of audit progress rather than current test guidance. | Move durable test principles into `docs/TESTING_PHILOSOPHY.md`. |
