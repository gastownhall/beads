# Linting Policy

Last reviewed: 2026-08-02

Freshness source: `.golangci.yml`, `scripts/ci/pr-lint.sh`, `Makefile`,
`.github/workflows/pr.yml`, and `.github/workflows/main.yml`.

This document explains the required Go lint gate for this codebase.

## Current Status

Lint is a required CI gate. The PR and main workflows install the pinned
golangci-lint v2.10.1 release and invoke the repository-owned `ci-pr-lint`
wrapper, which must pass with zero issues.

Run the same required contract locally with:

```bash
make ci-pr-lint
```

The wrapper runs:

- `make fmt-check`;
- golangci-lint with `.golangci.yml`, readonly module downloads, a five-minute
  timeout, and the `gms_pure_go` build tag; and
- a second non-CGO Windows cross-lint pass when the native host does not already
  cover that target.

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
