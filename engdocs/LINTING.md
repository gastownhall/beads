# Linting Policy

Last reviewed: 2026-10-03

Freshness source: `.golangci.yml`, `scripts/ci/pr-lint.sh`, `scripts/pr-lint/`,
`Makefile`, `.github/workflows/pr.yml`, and `.github/workflows/main.yml`.

This document explains the required Go lint gate for this codebase.

## Current Status

Lint is a required CI gate: ONE job per lane (`pr-lint-wrapper`), over the same
pinned golangci-lint v2.10.1 release and the same `.golangci.yml`. Each lane
must pass with zero issues in its own scope.

- **The PR lane reports only what the PR introduces.** `pr-lint-wrapper` runs
  the repository-owned `ci-pr-lint` wrapper with `BD_LINT_NEW_FROM_MERGE_BASE`
  set to the diff against the merge base with `main`. A finding in code the PR
  did not touch does not block it. "New" is measured off the diff, so MOVED
  CODE READS AS NEW: a pre-existing violation carried into a PR by a move or a
  rename is reported against that PR and blocks it. Fix it or `//nolint` it
  there — that is the accepted cost of the diff-scoped trade.
- **The main lane sweeps the whole tree.** The same job runs on every push to
  `main` with `BD_LINT_NEW_FROM_MERGE_BASE` unset, so a finding that lands
  there reds main's own run rather than every open PR.

`pr-lint-wrapper` is a 3-leg matrix, `matrix.target: [native, windows, darwin]`
(`fail-fast: false`), one leg per `scripts/pr-lint` pass (see below); the gate
requires the matrix's aggregate result under the same `PR_LINT_WRAPPER` id as
before. A former separate `Lint` job (the `golangci-lint-action` with
`only-new-issues`) was functionally identical to this matrix's native leg
(same binary, config, flags and diff scope) and was folded into it; see #5629.

Each leg installs golangci-lint from the pinned release binary
(`scripts/ci/install-golangci-lint.sh`, sha256-verified) instead of
`go install`, and restores a per-leg cache
(`~/.cache/golangci-lint` plus a leg-specific `GOCACHE`) keyed on
`.golangci.yml`, `go.sum` and the leg. A miss on the exact content key falls
back to the leg's most recent entry; golangci-lint revalidates it against the
current config and inputs, so a stale restore cannot mask an issue. pr.yml
only ever restores; main.yml's matrix is the only saver, on an exact-key miss.

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

- the shared `scripts/ci/fmt-check.sh` formatting check; and
- the checkout-owned `scripts/pr-lint` Go driver, which runs golangci-lint with
  `.golangci.yml`, readonly module downloads, a five-minute timeout, the
  `gms_pure_go` build tag, and `--new-from-merge-base` when
  `BD_LINT_NEW_FROM_MERGE_BASE` names a ref, then runs the same scope for the
  non-CGO Windows target and the non-CGO darwin (arm64) target when the native
  host does not already cover them, so `//go:build windows && !cgo` and
  `//go:build darwin` files are linted from the Linux runner.

The shell wrapper records one aggregate timing for the Go lint driver. The
driver prints a heading and result for each native or cross-target pass so
failures remain attributable without duplicating target-selection policy in
Bash.

The shared driver owns the lint passes used by `scripts/ci/pr-lint.sh` and
Beads-source `bd preflight`. The pre-commit hook does not call it: the hook
uses changed-lines scope, adds `--fix`, and omits the Windows cross-lint pass.
The driver honors `BD_LINT_TARGETS` (a comma list of `native`, `windows` and
`darwin`; default all three) so each CI matrix leg can run just its own pass;
`make ci-pr-lint` and `bd preflight` both leave it unset and keep running all
three.

The driver matches `.buildflags` when preparing `GOFLAGS`: appending
`-tags=gms_pure_go` can override an inherited bare-Go tags value; it does not
merge tag lists. Each lint process has a six-minute context and a five-minute
golangci-lint timeout. Cancelling preflight's outer `go run` command does not
guarantee immediate descendant cleanup.

On Windows, the driver checks each pass's selected Go SDK and prefers its
`bin` directory only in that linter child's PATH. This avoids an extra Go
auto-selection process surviving cancellation. Discovery shares the existing
30-second probe budget; custom SDKs that cannot be verified retain the original
PATH. Toolchain requests, formatting, and lint deadlines are unchanged. This
does not guarantee cleanup of compiler descendants or fix slow package loading.

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
`scripts/ci/pr-lint.sh`, exposed as `make ci-pr-lint`; its lint policy is owned
by the shell-free `scripts/pr-lint` Go driver.

See [`CI_CLEANUP_PLAN.md`](CI_CLEANUP_PLAN.md) for the full CI tier policy.

## Future Work

- Periodically audit `.golangci.yml` exclusions and remove entries that are no
  longer needed.
