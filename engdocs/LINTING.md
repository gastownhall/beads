# Linting Policy

Last reviewed: 2026-08-10

Freshness source: `.golangci.yml`, `scripts/ci/pr-lint.sh`, `Makefile`,
`.github/workflows/pr.yml`, and `.github/workflows/main.yml`.

This document explains the required Go lint gate for this codebase.

## Current Status

Lint is a required CI gate, and it runs in TWO LANES over the same pinned
golangci-lint v2.10.1 release and the same `.golangci.yml`. Each must pass with
zero issues in its own scope.

- **The PR lane reports only what the PR introduces.** Both of its jobs are
  scoped to the diff against the merge base with `main`: the `lint` job passes
  `only-new-issues`, and `pr-lint-wrapper` runs the repository-owned
  `ci-pr-lint` wrapper with `BD_LINT_NEW_FROM_MERGE_BASE`. A finding in code the
  PR did not touch does not block it.
- **The main lane sweeps the whole tree.** Both jobs run unscoped on every push
  to `main`, so a finding that lands there reds main's own run rather than every
  open PR.

Run the wrapper locally with:

```bash
make ci-pr-lint
```

That is the MAIN lane's contract, not the PR lane's: with
`BD_LINT_NEW_FROM_MERGE_BASE` unset it sweeps the whole tree, so it is
deliberately STRICTER than the gate your PR has to clear and may report findings
you are not required to fix. Fix the ones that are yours; the PR gate is the
authority on what blocks a merge, and main's own run is where the rest is
answered.

The wrapper runs:

- `make fmt-check`;
- golangci-lint with `.golangci.yml`, readonly module downloads, a five-minute
  timeout, the `gms_pure_go` build tag, and `--new-from-merge-base` when
  `BD_LINT_NEW_FROM_MERGE_BASE` names a ref; and
- a second non-CGO Windows cross-lint pass, on the same scope, when the native
  host does not already cover that target.

## Policy

Treat new lint findings as defects to fix before merge. Do not add a tolerated
failing baseline, and do not configure CI with `--issues-exit-code=0`.

When a linter reports an intentional or false-positive pattern:

- Prefer a narrow `.golangci.yml` exclusion tied to a path, linter, and message.
- Use `//nolint:<linter>` only when the reason is local to a specific line and
  the comment explains why the warning is not actionable.
- Keep broad linter disables as a last resort.

The current configuration already encodes accepted exclusions for intentional
patterns such as deferred cleanup errors, controlled subprocess execution,
test-fixture file reads, and documented security false positives.

## CI Cleanup Decision

`pr-lint` stays separate from `pr-policy` and `pr-core` so failures are easy to
identify and rerun. Its repository-owned wrapper is
`scripts/ci/pr-lint.sh`, exposed as `make ci-pr-lint`.

See [`CI_CLEANUP_PLAN.md`](CI_CLEANUP_PLAN.md) for the full CI tier policy.

## Future Work

- Periodically audit `.golangci.yml` exclusions and remove entries that are no
  longer needed.
