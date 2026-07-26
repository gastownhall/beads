# Linting Policy

Last reviewed: 2026-07-26

Freshness source: `.golangci.yml`, `.buildflags`, `go.mod`, `go.sum`,
`Makefile`, `scripts/ci/pr-lint-host.sh`, `scripts/ci/pr-lint.sh`,
`scripts/ci/pr-lint-routing-test.sh`, `scripts/pr_lint_ci_contract_test.go`,
`scripts/ci/lib/timing.sh`,
`.github/scripts/ci-gate.sh`, `.github/workflows/pr.yml`,
`.github/workflows/main.yml`, `.github/workflows/ci-measurements.yml`,
`CONTRIBUTING.md`, `AGENTS.md`, `AGENT_INSTRUCTIONS.md`,
`.github/copilot-instructions.md`, `scripts/README.md`,
`engdocs/CI_CLEANUP_PLAN.md`, `engdocs/CI_TEST_SURFACE_AUDIT.md`,
`engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md`, `engdocs/TESTING.md`,
`engdocs/DOC_INVENTORY.md`,
`examples/formulas/gh-pr-review.formula.toml`,
`examples/formulas/gh-issue-to-pr.formula.toml`,
`.pre-commit-config.yaml`, `.githooks/pre-commit`, `cmd/bd/preflight.go`,
`scripts/check-doc-freshness.sh`, and
`scripts/check_doc_freshness_test.go`.

This document explains the required Go lint gate for this codebase.

## Current Status

Lint contract v2 is a workflow-policy CI gate. The only public entry point is
`make ci-pr-lint`; it
enters `scripts/ci/pr-lint-host.sh`, binds the host and tools, and then runs the
same internal target as workflow-required CI. The internal target first runs the
target-routing regression and then the repository-owned wrapper. Every linter
pass names the exact repository `.golangci.yml`, disables workspace discovery,
uses readonly module mode, applies `--build-tags=gms_pure_go`, and must return
zero issues.

In this document, “required” means required by repository workflow policy and
the aggregate job dependency graph. It does not assert server-side merge
enforcement. As of 2026-07-26, active ruleset `15646382` has no
required-status-check rule; rollout of aggregate checks as server requirements
remains pending.

Run the complete required contract locally with:

```bash
make ci-pr-lint
```

To exercise the exact Windows/non-CGO skip case from a POSIX shell:

```bash
GOOS=windows GOARCH=amd64 make CGO_ENABLED=0 ci-pr-lint
```

## Target Snapshot and Passes

For local use, the public host makes one `GOENV=off GOWORK=off go env GOOS
GOARCH CGO_ENABLED` call and narrowly validates that complete caller tuple.
The outer Make invocation passes it into the host, and the host passes the same
three values as explicit command-line variables through the internal Make
boundary. In GitHub Actions, protected expected-target fields instead pin the
actual Go host tuple. The public Make boundary freezes caller state before the
Makefile's normal CGO default: ambient `GOOS`, `GOARCH`, or `CGO_ENABLED` may
be absent or exactly equal to the protected value, while every conflict is
refused. Those private snapshots use Make `override` assignments, so a caller
cannot forge the presence or value fields on the command line.

Before either lint starts, `scripts/ci/pr-lint.sh` makes one fail-closed `go env
GOOS GOARCH CGO_ENABLED` call. It requires an exact match with the host-selected
tuple, freezes the result, and never re-queries target state. This is the
end-to-end public-entrypoint guard: dropping or changing a caller value at
either Make boundary stops before formatting or lint.

Unless the frozen tuple is already exactly `windows` with `CGO_ENABLED=0`, the
wrapper runs a second lint for `GOOS=windows`, the frozen `GOARCH`, and
`CGO_ENABLED=0`. This keeps files guarded by `//go:build windows && !cgo` inside
the required gate without silently changing architecture. A Windows/non-CGO
caller runs only the normal pass. Failure or malformed output from the one
target query stops before either linter.

Formatting remains part of the wrapper through `make fmt-check`.

Every linter pass uses the exact regular file
`$REPO_ROOT/.golangci.yml`, `GOWORK=off`, and
`--modules-download-mode=readonly`. A sibling `.golangci.yaml`, a `go.work`,
or a `vendor` directory therefore cannot become an undeclared input. The
black-box contract includes explicit refusals for a substituted config,
workspace mode, vendor mode, and a changed public target.

## Host and Tool Authority

Workflow-required Linux callers invoke
`/usr/bin/make -f Makefile CI_BASH=/usr/bin/bash ci-pr-lint` and declare
Linux/amd64/CGO=1. Required macOS uses stock `/usr/bin/make` 3.81 plus
`/bin/bash` on explicit `macos-15` and requires Darwin/arm64/CGO=1. The stdin
makefile probe works on Make 3.81, and neither POSIX route relies on modern
`.SHELLFLAGS` behavior. Apple's 3.81 build may emit an empty `MAKE_HOST`; only
the exact `/usr/bin/make` invocation leaf with exact first-line version
`GNU Make 3.81` can use that narrow fallback after the outer Darwin host has
already been proved. Its canonical target must be executable, but the remaining
free-form build banner is deliberately not authority. Both PR and main macOS
jobs have a 30-minute outer bound, including cold private installation. The
host wrapper then:

- requires hosted callers to strip ambient `MAKE`, `MAKEFLAGS`, `MFLAGS`,
  `GNUMAKEFLAGS`, and `MAKEFILES`, proves `MAKE` retained its GNU Make
  `default` origin, and rejects command-line Make/control substitution. The
  fixed workflow argv always supplies `-f Makefile` and never supplies
  `MAKEFILES`; the missing-path falsifier
  checks its post-parse origin/value receipt, not containment of a malicious
  command-line makefile, which Make would load before the recipe;
- requires `MAKEFILE_LIST` to be exactly `Makefile`, clears `BASH_ENV` and
  `ENV` before every public recipe, and passes the exact bound Make executable
  to formatting recursion;
- confirms the actual Bash, `uname`, and Go host all report the declared
  Linux or macOS host;
- resolves Bash, GNU Make, Go, gofmt, Git, sed, env, the C/C++ compilers, and
  golangci-lint once to absolute executable paths;
- checks the exact Go version from protected `go.mod`;
- installs golangci-lint 2.10.1 into a new private runner directory under an
  empty environment carrying explicit host `GOOS`/`GOARCH`, `CGO_ENABLED=0`,
  `GOENV=off`, `GOWORK=off`, empty `GOFLAGS`/`GOEXPERIMENT`,
  `GOTOOLCHAIN=local`, and the applicable baseline architecture level, then
  checks the invoked binary reports exactly that version;
- exercises startup isolation, quoting, repository/path resolution, and GNU
  Make host semantics; and
- launches the internal Make target through a curated environment that carries
  those exact identities, the selected target, and no ambient Go configuration.

The version contract is field-specific. Go must match the exact three-component
version protected by `go.mod`, and every installed or claimed golangci-lint is
exactly 2.10.1. Linux GNU Make is deliberately version-independent: the host
binds one GNU Make executable and positively exercises every host/quoting
capability this gate uses instead of claiming an uninstalled version.
macOS instead binds the stock paths and exact GNU Make 3.81 expected by its
protected runner image.

`pr-lint.sh` refuses an unbound toolchain and uses only those absolute paths.
The same Bash identity starts both scripts, the same Make identity runs
`fmt-check`, the bound gofmt performs formatting discovery, and temporary
`GOOS`/`GOARCH`/`CGO_ENABLED` assignments call the bound linter directly rather
than going through `env` or another `PATH` lookup. The timing helper uses
Bash's built-in `SECONDS` counter rather than an external clock command.
When the public target was itself launched through an absolute GNU Make path,
it hands that identity to the host wrapper; a bare local `make` is resolved once
by the wrapper like the other tools.

Full Windows `make -f Makefile ci-pr-lint` host semantics are supported through native
Windows GNU Make only. That required lane separately asserts actual Windows and
a PowerShell 7+ Core process, resolves and reuses one absolute GNU Make 4.4.1
and one bootstrap Windows Git executable, and invokes the full public target.
That target is exact Go 1.26.5 Windows/amd64/CGO=1 using MinGW 16.1.0 with
both `gcc` and `g++` reporting `x86_64-w64-mingw32`, plus a private
golangci-lint 2.10.1, followed by Windows/amd64/CGO=0. The full workflow
boundary exposes GNU Make only as `mingw32-make.exe`, with no `make.exe` alias,
and proves formatting recursion retains that invoked executable. The bootstrap
Git exec path derives one common bundle root; resolved Git may be the distinct
`cmd` or `mingw64/bin` leaf, but Bash, cat, chmod, cp, diff, env, mkdir, mktemp,
readlink, rm, rmdir, sed, uname, and cygpath must be their exact leaves under
that same root. A
mixed-PATH positive case and wrong-root Git falsifier protect this rule.
MSYS2 and Cygwin packages are
separate shell/bootstrap compatibility boundaries only; they do not execute or
claim the full lint-host wrapper. Their mutable package versions are not
claimed as authority. Those rows bind GNU Make family plus the exact
family/path/Make/Git smoke they consume. Every matrix row must publish its own
success marker; an unknown, skipped, or zero-execution row fails an
unconditional final step. The complete Windows matrix has a 45-minute outer
bound, including cold Go, Make, MinGW, and private-linter installation.

Private linter installation is also a cleanup contract. `RUNNER_TEMP` is
canonical and non-symlinked; `mktemp` must return one regular direct child named
`beads-pr-lint-tools.<8 alphanumerics>`. Cleanup authority is granted only
after that proof. Immediately before deletion the handler re-proves canonical
path, directory type, and allowed contents; it does not claim a portable file
identity. This is safe under the explicit trusted-runner assumption that the
private temp root has one owner and no concurrent same-type replacement writer.
An outside, nested, malformed, symlink-replaced, or redirected result is
retained and fails closed. Cleanup enters the canonical directory, requires
exactly the one verified regular linter leaf, unlinks only that leaf, and
removes the now-empty directory with bound `rmdir`; production never uses
recursive deletion. Unexpected entries and partial installations are retained,
and successful cleanup requires the exact child to be absent. Ordinary failure
and handled INT, TERM, and HUP use this same path. SIGKILL, caller loss, and
power loss cannot run an in-process trap and rely on runner teardown.

The separate action-based `Lint` job is pinned to golangci-lint 2.10.1, but it
is not authority for the Windows-target claim. That authority belongs to the
bound wrapper jobs and their required aggregate.

## Workflow Callers and Runtime

Every maintained workflow caller invokes an absolute Make executable and the
public target, never `pr-lint-host.sh` directly. PR and main `Build Artifacts`
provide the full Linux route. PR and main `PR Lint (wrapper timing)` provide
the full Darwin arm64 route. PR `Windows Make shell (native)` provides the
full native Windows route; its MSYS2/Cygwin siblings remain smokes. The manual
`ci-measurements` workflow is an optional Linux timing caller. The public Make
boundary also carries its `CI_GIT` selection explicitly into the host; native
Windows proves that resolved leaf can differ from the bootstrap
`GIT_WINDOWS_EXE` leaf while remaining inside the same bundle.

Each full non-Windows route performs its native lint and Windows/non-CGO lint.
Native Windows performs Windows/CGO and Windows/non-CGO. This is intentional
independent host coverage rather than duplicate Linux timing.

## Policy

Treat new lint findings as defects to fix before merge. Do not add a tolerated
failing baseline, and do not configure CI with `--issues-exit-code=0`.
The staged-file `.githooks/pre-commit`, `.pre-commit-config.yaml`, and the
current `cmd/bd` preflight lint are narrower developer conveniences. They do
not share the v2 host/target/config contract and cannot satisfy this gate.

When a linter reports an intentional or false-positive pattern:

- Prefer a narrow `.golangci.yml` exclusion tied to a path, linter, and message.
- Use `//nolint:<linter>` only when the reason is local to a specific line and
  the comment explains why the warning is not actionable.
- Keep broad linter disables as a last resort.

The current configuration already encodes accepted exclusions for intentional
patterns such as deferred cleanup errors, controlled subprocess execution,
test-fixture file reads, and documented security false positives.

## Protected Dependency Surface

A review or required-check declaration for this contract must protect the exact
head plus all semantic routing and aggregation inputs:

- `.github/workflows/pr.yml` and `.github/scripts/ci-gate.sh`;
- `Makefile`, `.buildflags`, `.golangci.yml`, `go.mod`, and `go.sum`;
- `scripts/ci/pr-lint-host.sh`, `scripts/ci/pr-lint.sh`,
  `scripts/ci/pr-lint-routing-test.sh`,
  `scripts/pr_lint_ci_contract_test.go`, and `scripts/ci/lib/timing.sh`; and
- this document, `engdocs/CI_CLEANUP_PLAN.md`,
  `engdocs/CI_TEST_SURFACE_AUDIT.md`,
  `engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md`, `engdocs/TESTING.md`,
  `engdocs/DOC_INVENTORY.md`,
  `scripts/README.md`, `CONTRIBUTING.md`, `AGENTS.md`,
  `AGENT_INSTRUCTIONS.md`, `.github/copilot-instructions.md`,
  `examples/formulas/gh-pr-review.formula.toml`,
  `examples/formulas/gh-issue-to-pr.formula.toml`,
  `.pre-commit-config.yaml`, `.githooks/pre-commit`,
  `cmd/bd/preflight.go`,
  `scripts/check-doc-freshness.sh`, and
  `scripts/check_doc_freshness_test.go`.

The affected non-PR consumers are `.github/workflows/main.yml` and
`.github/workflows/ci-measurements.yml`; they use the same bound wrapper and
belong in the review corpus even though only PR's `CI Gate / Required` controls
pull-request integration.

## CI Cleanup Decision

`pr-lint` stays separate from `pr-policy` and `pr-core` so failures are
easy to identify and rerun. It should include:

- the strict eleven-case routing, one-query, mutable-state, both linter-pass
  failures, Make-failure propagation, and nonzero-execution regression, plus
  four inner authority refusals, a nonzero-gofmt end-to-end refusal, fifteen
  macOS Make/target/cleanup/producer refusals, four handled cleanup cases,
  ambient install-state poison, and the native Windows
  three-target/one-bundle/no-`make.exe` falsifiers;
- `make fmt-check`;
- a lint with the frozen caller tuple; and
- a Windows/non-CGO lint with the same architecture unless it would duplicate
  the frozen tuple.

See [`CI_CLEANUP_PLAN.md`](CI_CLEANUP_PLAN.md) for the full CI tier policy.

## Future Work

- Periodically audit `.golangci.yml` exclusions and remove entries that are no
  longer needed.
- Re-measure the two-pass wrapper before changing caller placement or
  concurrency.
