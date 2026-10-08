# Linting Policy

Last reviewed: 2026-10-07

Freshness source: `.golangci.yml`, `tools/nogo/`, `scripts/ci/pr-lint.sh`,
`scripts/pr-lint/`, `Makefile`, `.github/workflows/pr.yml`, and
`.github/workflows/main.yml`.

This document explains the required Go lint and vet gate for this codebase.

## Current Status

Lint and vet run as **nogo** (`//tools/nogo`) inside the Bazel build. rules_go
runs the analyzers beside every first-party Go compile, so any `bazel build`
or `bazel test`, locally or on rbe-west, fails on a finding. The analyzers are:

- **go test's vet checks**: the passes cmd/go's `defaultVetFlags` give
  `go test` (`atomic`, `bools`, `buildtag`, `directive`, `errorsas`,
  `ifaceassert`, `nilfunc`, `printf`, `slog`, `stringintconv`, `tests`). They
  see every file, tests included, as the former `go vet ./...` did.
- **The golangci-lint linters `.golangci.yml` enables** (depguard, errcheck,
  forbidigo, gosec, misspell, sloglint, unconvert, unparam), each a thin
  wrapper in `tools/nogo/analyzers/` over the same library version
  golangci-lint v2.10.1 uses.

`.golangci.yml` is still the one place lint policy lives. Bazel embeds it into
the analyzers, and `tools/nogo/internal/golangci` applies it as golangci-lint
did: which linters are enabled, their settings, `run.tests: false` (only the
library compile unit of a package is linted, never its test unit),
generated-file handling, and the path/text exclusion rules. Decoding is
strict: a key the wrappers do not implement fails the build instead of being
ignored. `//nolint:<linter>` directives keep working; a rules_go patch
(`third_party/patches/rules_go_nogo_golangci_nolint.patch`, shared with
gascity) gives them golangci-lint's line ranges.

Parity with golangci-lint was proved on the whole tree: with the exclusion
rules removed, golangci-lint and nogo report the same 3,916 findings (file,
line, linter and text) natively and the same 3,859 for windows/amd64 and
darwin/arm64; with `.golangci.yml` as committed, both report none.

Where it gates:

- **bazel.yml's `test` lane** (required through `CI Gate / Required`):
  `bazel test //... --config=ci` validates every package natively.
- **bazel.yml's `release cross-compile` lane** (`bazel-release-cross`,
  required): `scripts/ci/bazel-release-cross-compile.sh` builds
  `//tools/bazel:release_cross`, every `go_library` and `go_binary`
  split-transitioned to each release platform without cgo, and nogo
  validates each of those compiles. So `//go:build windows`, `darwin`,
  `!linux` (and freebsd, android, arm64) files are analyzed from Linux: a
  superset of golangci-lint's former `GOOS=windows`/`GOOS=darwin` legs.
- **Every other Bazel lane** (integration, embedded, dolt-server) validates
  what it compiles, including files only its build tags select.

Every PR is checked against the whole tree, not only its diff: the tree has
no findings, so there is no baseline to scope against.

Run the gate locally with:

```bash
make ci-pr-lint      # native + windows + darwin (also: make lint, make vet)
make lint-changed    # native, only the Bazel packages of changed Go files
```

Both need `bazel` (or `bazelisk`) on PATH. With remote execution configured
(`.bazelrc.local` or `user.bazelrc`), a warm run takes seconds: the native
pass uses the race configuration of the CI test lane, so it reuses CI's
remote cache. `--config=nogo` asks for the analysis output group alone, so
nothing is linked.

`make ci-pr-lint` runs `scripts/ci/pr-lint.sh`, which times the checkout-owned
`scripts/pr-lint` Go driver: `bazel build --config=nogo //...`, then
`//tools/bazel:release_cross` for windows/amd64 and darwin/arm64
(`--config=nogo-cross`). The driver honors `BD_LINT_TARGETS` (a comma list of
`native`, `windows` and `darwin`; default all three), prints a heading and
result per pass, and is what Beads-source `bd preflight` runs. The pre-commit
hook (and `.pre-commit-config.yaml`) runs `make lint-changed
LINT_CHANGED_SCOPE=staged`. Formatting is not part of lint:
`//scripts/repochecks:fmt_test` gates gofmt, and the hook formats staged files
before linting them.

## Policy

Treat lint findings as defects to fix before merge. Do not add a tolerated
failing baseline.

When a linter reports an intentional or false-positive pattern:

- Prefer a narrow `.golangci.yml` exclusion tied to a path, linter, and message.
- Use `//nolint:<linter>` only when the reason is local to a specific line and
  the comment explains why the warning is not actionable.
- Keep broad linter disables as a last resort.

To enable another golangci-lint linter, add it to `.golangci.yml`, add a
wrapper under `tools/nogo/analyzers/` (with its settings in
`tools/nogo/internal/golangci`), and list it in `tools/nogo/analyzers.bzl`;
`//tools/nogo:nogo_test` fails until the enabled list and the analyzer list
agree.

## CI Cleanup Decision

The former `PR Lint (native|windows|darwin)` and `Go checks (vet)` jobs, and
main.yml's lint and vet cache seeders, are retired: nogo runs the same checks
in the Bazel lanes on rbe-west. When the farm is switched off (bazel.yml mode
`skip`), no lane runs, so neither does lint.

See [`CI_CLEANUP_PLAN.md`](CI_CLEANUP_PLAN.md) for the full CI tier policy.

## Future Work

- Periodically audit `.golangci.yml` exclusions and remove entries that are no
  longer needed.
