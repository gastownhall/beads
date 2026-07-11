# CI Test Surface Audit

Last reviewed: 2026-05-26

Freshness source: `origin/main` at `4990c8309`, `Makefile`,
`scripts/test*.sh`, `.buildflags`, `.test-skip`, `.github/workflows/*.yml`,
and test-file inventory from `git ls-files`, `go list`, and `rg`.

This is an audit snapshot, not the final CI policy. Use it to reason from first
principles about what the repository can validate, what CI currently validates,
and what should be cleaned up next.

Accepted cleanup decisions and implementation order are tracked in
[`CI_CLEANUP_PLAN.md`](CI_CLEANUP_PLAN.md).

## Executive Summary

The repository has a large Go test surface and several non-Go package surfaces,
but CI is not organized around a single canonical test contract.

Current facts:

| Surface | Current size / command | CI status |
|---|---:|---|
| Go packages | 69 packages from `go list ./...` | Core PR/main CI runs direct `go test`, not `scripts/test.sh`. |
| Go test files | 610 `*_test.go` files | Mostly covered through Linux/macOS `./...`, with exclusions and tags. |
| Go test functions | 4362 `func Test...` declarations | PR/main CI uses `-short` and skips `^TestEmbedded`. |
| Go benchmarks | 46 `func Benchmark...` declarations | Local/manual only. |
| Embedded Dolt tests | 157 `TestEmbedded*` declarations | Conditional 20-shard CI matrix on risk paths, always on main push/merge queue. |
| Integration-tagged tests | 31 files with `integration` build tag | Nightly only as a broad sweep; selected Docker-backed suites run in PR CI. |
| MCP Python package | `uv run pytest`, `uv run ruff`, `uv run mypy` are documented/configured | Build/publish workflows do not run the checks. |
| npm package | `npm test`, `npm run test:integration`, `npm run test:all` | Release publish does not run package tests. |
| Website | `npm run build`, `npm run typecheck` | Deploy workflow builds on main pushes only; typecheck is unused in CI. |

Main conclusion: before changing CI mechanics, define named validation tiers
and make each tier map to one repository-owned command. Today, local docs,
Make targets, wrapper scripts, and Actions jobs describe overlapping but
different contracts.

## Local Test Surface

### Canonical Build Flags

`.buildflags` is the source for normal local shell scripts:

- Defaults `CGO_ENABLED=1` unless the caller already set it.
- Exports `BEADS_BUILD_TAGS=gms_pure_go`.
- Adds `-tags=gms_pure_go` to `GOFLAGS`.

The normal shipped path is CGO-enabled with pure-Go regex support. The ICU path
exists only as an opt-in maintainer path.

### Make Targets

| Target | Command path | Purpose |
|---|---|---|
| `make build` | `go build -tags "$(BUILD_TAGS)" ./cmd/bd` | Build local `bd` binary with `gms_pure_go`. |
| `make test` | `TEST_COVER=1 ./scripts/test.sh` | Local default suite with coverage and `.test-skip` handling. |
| `make test-icu-path` | `./scripts/test-icu-path.sh ./...` | Opt-in ICU regex path, not normal validation. |
| `make test-full-cgo` | Alias to `make test-icu-path` | Deprecated compatibility target. |
| `make test-regression` | `go test -tags=regression,$(BUILD_TAGS) ./tests/regression/...` | Differential regression suite against baseline release. |
| `make test-upgrade` | Build then `scripts/upgrade-smoke-test.sh` | Previous-release upgrade smoke gate. |
| `make test-cross-version` | Build then `scripts/cross-version-smoke-test.sh` | Cross-version upgrade smoke coverage. |
| `make test-migration` | Build then `scripts/migration-test/run.sh` | Migration fidelity harness across storage eras. |
| `make bench` | `go test -bench=. ./internal/storage/dolt/` | Full Dolt benchmark suite. |
| `make bench-quick` | Shorter benchmark run | Local performance iteration. |
| `make fmt-check` | `gofmt -l .` | Formatting gate. |
| `make check-docs` | Build no-CGO binary, then `scripts/check-doc-flags.sh` | CLI-doc flag freshness. |

### Go Test Runner

`scripts/test.sh` is the local wrapper recommended by `engdocs/TESTING.md`.

It:

- Sources `.buildflags`.
- Reads `.test-skip` and passes a composed `-skip` regex to `go test`.
- Defaults to `go test -timeout 3m ./...`.
- Supports `-v`, `-timeout`, `-run`, package arguments, and extra `-skip`.
- Enables coverage when `TEST_COVER=1`.
- Can start one shared Dolt test server when `BEADS_TEST_SHARED_SERVER=1`.

At this audit point, `.test-skip` contains only comments and no active skip
patterns.

### Build Tags and Special Test Modes

| Build tag / mode | Observed surface | Current command |
|---|---:|---|
| default CGO + `gms_pure_go` | Most Go tests | `./scripts/test.sh`, `make test`, PR Linux/macOS jobs. |
| `!cgo` | 3 test files | Only partially covered by the CI pure-Go cmd/bd job. |
| `cgo` | 186 test files | Covered when CGO is enabled, including normal Linux/macOS CI. |
| `integration` | 31 test files mention `integration` | Nightly broad run; selected PR jobs. |
| `regression` | 2 test files | `make test-regression`, regression workflow. |
| `e2e` | 1 test file | No routine CI gate observed. |
| `scripttests` | 1 test file | No routine CI gate observed. |
| `chaos` | 1 test file | No routine CI gate observed. |
| `regression && discovery` | 1 test file | Manual/specialized only. |

`testing.Short()` appears in 25 test files. The main PR Linux/macOS matrix runs
with `-short`, so some slow or external-path tests are intentionally skipped
there even when their build tags are selected.

### Embedded Dolt Surface

Embedded Dolt tests are split out from the PR/core matrix:

- CI core Linux/macOS uses `-skip '^TestEmbedded'`.
- `.github/scripts/ci-embedded-tier.sh` decides whether to run full embedded
  coverage.
- `build-embedded` prebuilds `/tmp/bd-embedded-test`,
  `/tmp/embeddeddolt-test`, and `/tmp/bd-cmd-test`.
- `test-embedded-storage` runs the embedded storage test binary with
  `BEADS_TEST_EMBEDDED_DOLT=1`.
- `test-embedded-cmd` shards `cmd/bd` `TestEmbedded*` across 20 jobs using
  `.github/scripts/embedded-test-shard.sh`.

The tier detector runs full embedded coverage on:

- Push to `main`.
- Merge queue.
- PRs touching `cmd/`, `internal/`, `tests/`, `scripts/`, `.github/scripts/`,
  `.github/workflows/`, Go module/build inputs, root agent/release docs, or any
  `*.go` file.

It skips the embedded matrix on docs/metadata-only PRs.

### Release and Migration Surface

The release-related local scripts are separate from the normal Go test runner:

| Script / target | Scope |
|---|---|
| `scripts/upgrade-smoke-test.sh` | Previous-release upgrade scenarios covering data, mode, role, doctor, and mutations. |
| `scripts/cross-version-smoke-test.sh` | Candidate readability after data creation by older releases. |
| `scripts/migration-test/run.sh` | Rich migration datasets, snapshots, fidelity checks, and recipe discovery. |
| `tests/regression/...` | Differential CLI/storage behavior against `tests/regression/BASELINE_VERSION`. |

These are valuable but are not one unified release gate in CI today.

### Non-Go Package Surface

| Area | Available local commands | Current issue |
|---|---|---|
| `integrations/beads-mcp` | `uv run pytest`, `uv run pytest --cov=beads_mcp tests/`, `uv run ruff check src/beads_mcp`, `uv run mypy src/beads_mcp`, `uv build` | Tests/lint/typecheck are documented but not run before publish in current workflows. |
| `npm-package` | `npm test`, `npm run test:integration`, `npm run test:all` | Release workflow publishes without running package tests. |
| `website` | `npm run build`, `npm run typecheck` | Deploy workflow builds and link-checks on main pushes; typecheck is not run, and PRs do not get a website gate. |
| `plugins/beads` | Manifest files and generated plugin assets | Version consistency is checked, but there is no obvious plugin manifest/schema gate. |

## Current GitHub Actions Map

### Split PR/Main Workflows

The former monolithic `ci.yml` has been split by tier/domain:

- `pr.yml`: pull request and merge queue baseline. It owns the Linux build
  artifact stage, PR policy/core/lint wrappers, compatibility policy/lint jobs,
  package gates that consume the Linux artifact, storage domain/uow tests, and
  the aggregate check `PR / CI Gate / Required`.
- `pr-risk.yml`: pull request and merge queue risk jobs. It owns embedded Dolt
  risk detection, embedded build/storage/cmd shards, the Nix flake smoke, and
  the aggregate check `PR Risk / CI Gate / Required`.
- `main.yml`: push-to-`main` branch health. It reruns the baseline wrappers,
  package gates, Linux/macOS short coverage, Windows smoke, embedded Dolt, Nix,
  storage domain/uow, and promoted Linux no-short integration shards.

Key jobs preserved by display name:

- `Build Artifacts`: runs `make ci-pr-policy`, `make ci-pr-lint`, builds
  `bd-linux-gms-pure`, and uploads checksummed run-scoped artifacts.
- `Check build-tag policy`: runs `scripts/check-build-tags.sh` and
  `scripts/check-go-install-guidance.sh`.
- `Check cmd/bd pure-Go tests compile (CGO_ENABLED=0)`: CGO-disabled cmd/bd
  build, test-binary compile, and a focused pure-Go test subset.
- `Check version consistency`, `Check no duplicate migration versions`,
  `Check doc flags freshness`, and PR-only `Check for .beads changes`.
- `PR Policy (wrapper timing)`, `PR Core (wrapper timing)`, and
  `PR Lint (wrapper timing)`.
- `Package Gate (MCP)`, `Package Gate (npm)`, and `Package Gate (website)`.
- `Test (storage domain + uow)`.
- `Build (Embedded Dolt)`, `Test (Embedded Dolt Storage)`, and
  `Test (Embedded Dolt Cmd N/20)`.
- Aggregate required-check candidates: `PR / CI Gate / Required` and
  `PR Risk / CI Gate / Required`.
- Main-only platform and integration jobs: `Test (ubuntu-latest)`,
  `Test (macos-latest)`, `Test (Windows - smoke)`,
  `Main Linux integration packages (N/6)`, and
  `Main Linux integration cmd/bd (N/8)`.
- `Check formatting`, `Lint`, and `Test Nix Flake`.

### Other Workflows

| Workflow | Triggers | Main validation |
|---|---|---|
| `regression.yml` | Push to `main`, PR to `main`, manual | Detector runs regression on push/manual, PR label `run-regression`, or risky paths; test command is `go test -tags=regression,gms_pure_go -timeout=20m -v ./tests/regression/...`. |
| `cross-version-smoke.yml` | Tags, PRs, manual | PRs test latest 5 releases, tags test latest 30, via `scripts/upgrade-smoke-test.sh`. |
| `migration-test.yml` | Tags, manual | Builds candidate and runs `scripts/migration-test/run.sh`; not a PR/main gate. |
| `nightly.yml` | Daily schedule, manual | `go test -v -race -tags=integration,gms_pure_go -coverprofile=coverage.out -timeout=30m ./...` with `BEADS_TEST_SKIP=dolt`; checks coverage >= 30%. |
| `nix-build.yml` | PR/push paths for Nix or Go module files, manual | `nix build .#default --print-build-logs`. |
| `deploy-docs.yml` | Push to `main` paths `website/**` or `scripts/generate-llms-full.sh`, manual | `npm ci`, generate `llms-full.txt`, `npm run build`, internal link check, non-blocking external link check, deploy Pages. |
| `release.yml` | Tags, manual from tag | GoReleaser, native macOS builds, macOS embedded smoke, release attestations/SBOM, Homebrew formula update, PyPI build/publish, npm publish. |
| `test-pypi.yml` | Manual | Builds MCP package and publishes to TestPyPI. |
| `update-flake-lock.yml` | Weekly, manual | Updates `flake.lock`, scope-checks diff, opens PR. |
| `update-vendor-hash.yml` | Dependabot `pull_request_target` for `go.mod`/`go.sum` | Updates `default.nix` vendor hash and pushes to dependabot branch. |

## Gap Analysis

### P0: Define the CI Contract

There is no single source of truth for which checks are required for PRs, main
pushes, nightly, and releases. The workflows encode this implicitly.

Impact: agents and maintainers cannot reliably answer "what must pass before
merge" or "which local command reproduces this status check" without reading
multiple workflow files.

### P0: Local and CI Go Commands Diverge

Local docs recommend `make test` / `scripts/test.sh`; PR CI runs direct
`go test` invocations with `-race`, `-short`, and `-skip '^TestEmbedded'`.

Impact:

- `scripts/test.sh` skip, timeout, coverage, and shared-server behavior is not
  the PR contract.
- `make test` is not a faithful local reproduction of PR CI.
- The `testing.Short()` boundary is part of CI behavior but not prominent in
  the local test docs.

### P1: No-CGO Coverage Is Partial

CI has a focused CGO-disabled cmd/bd compile/run job, but `!cgo` tests outside
that subset are not obviously covered by a full no-CGO `./...` job.

Impact: regressions in server-mode/no-CGO behavior outside the focused subset
can escape the main PR gate.

### P1: Integration Coverage Is Fragmented

Some Docker-backed Dolt suites run in PR CI, the broad `integration` tag runs
nightly with Docker Dolt tests explicitly skipped, and several slow paths are
suppressed by `-short`.

Impact: this may be intentional, but the contract is undocumented. It is hard
to distinguish "not worth PR time" from "accidentally uncovered."

### P1: Release Gates Are Spread Across Workflows

Regression, cross-version smoke, migration fidelity, release builds, PyPI,
npm, and Homebrew publishing are separate workflows with different triggers.

Impact: tag-time validation exists, but the release-blocking order and rerun
strategy are not represented as one release gate document or workflow summary.

### P1: Non-Go Package Tests Are Not CI Gates

The MCP Python package, npm wrapper, and website each have local tests or
checks. Current workflows mostly build or publish these artifacts but do not
run their full local validation before publishing or on PRs touching those
paths.

Impact: package-specific regressions can reach release/publish workflows
without the package's own tests having run.

### P2: Windows Is Smoke-Only

Windows CI builds and runs `version` and `help`, but it does not run Go tests.

Impact: Windows-specific filesystem, path, shell, and CGO behavior depends on
limited coverage. This may be the right tradeoff, but it should be an explicit
tier with a known owner and escape hatch.

### P2: Benchmark and Performance Regression Checks Are Manual

Benchmarks and production-shaped repro tools are documented, but no workflow
captures benchmark artifacts or runs labeled performance checks.

Impact: performance-sensitive changes rely on human/agent discipline rather
than a repeatable CI path.

### P2: Existing Docs Have Drift

Examples:

- `engdocs/TESTING.md` says the wrapper script is consistent with CI, but PR CI
  uses direct `go test`.
- `engdocs/LINTING.md` says CI may not fail on known lint issues, while the
  workflow uses the standard `golangci-lint` action without an explicit
  non-failing issues exit code.
- Older staged audit docs contain obsolete test counts.

Impact: stale docs undermine the cleanup effort because they hide the actual
current contract.

## Roadmap

### Phase 1: Name the Tiers

Create a CI policy doc or convert this audit into one with explicit tiers:

| Tier | Purpose | Example trigger | Local command |
|---|---|---|---|
| `pr-core` | Required fast PR signal | Every PR/main push | To be defined. |
| `pr-risk` | Extra checks for risky paths | Go/storage/scripts/workflow changes | To be defined. |
| `nightly-full` | Expensive broad sweep | Schedule/manual | To be defined. |
| `release-gate` | Must pass before publishing | Tags/manual release | To be defined. |
| `package-gates` | Python/npm/website path checks | Path-gated PRs and release | To be defined. |
| `perf-manual` | Benchmark evidence | Manual or label | To be defined. |

Then make every status check name include its tier.

### Phase 2: Add Reproducible CI Wrapper Commands

Add repository-owned wrapper scripts or Make targets for each tier, for example:

- `make test-pr-core`
- `make test-pr-risk`
- `make test-nocgo`
- `make test-integration-nightly`
- `make test-release-gate`

Do not make workflow YAML be the only place where command policy lives.

### Phase 3: Align Current PR CI With the Wrappers

Once the tier commands exist, change the PR/main workflows to call those
commands. Preserve useful CI-only behavior such as gotestsum/JUnit output by
wrapping around the same underlying command rather than maintaining a separate
test definition.

### Phase 4: Fill Package-Specific Gaps

Add path-gated checks:

- MCP: `uv run ruff check`, `uv run mypy`, `uv run pytest`.
- npm package: `npm ci` or equivalent package install, then `npm run test:all`.
- Website: `npm ci`, `npm run typecheck`, `npm run build`, and possibly the
  internal link check on PRs that touch `website/**`.
- Plugins: validate manifest JSON and version consistency beyond the existing
  version script if schema tooling exists or can be added cheaply.

### Phase 5: Rationalize Expensive Coverage

Decide and document:

- Which integration-tagged tests must be PR-gated.
- Whether nightly should continue using `BEADS_TEST_SKIP=dolt`, or whether
  Docker-backed Dolt hangs are resolved enough to split and re-enable them.
- Which release migration paths must block tags versus run manually.
- Which Windows tests are worth adding without making the matrix unstable.

### Phase 6: Add CI Observability

For every tier, capture enough artifacts to debug failures without rerunning:

- JUnit for Go tests where practical.
- Coverage artifacts by tier.
- Embedded shard selected-test lists.
- Package test logs.
- Release-gate summary that links regression, cross-version, migration, and
  publish checks.

## Immediate Next Steps

1. Turn this audit into a shorter `docs/CI.md` policy once maintainers agree on
   the tier names.
2. Split `cmd/bd` by top-level test name for the no-short Linux integration
   lane; the first package-sharded run showed `cmd/bd` owns the wall-clock tail.
3. Promote the additive PR wrapper jobs after repeated measurements confirm
   they preserve the current required PR behavior.
4. Add a no-CGO all-package compile/test gate or explicitly document why the
   focused cmd/bd subset is enough.
5. Add path-gated MCP, npm package, and website checks before touching the main
   Go matrix.
6. Keep `engdocs/TESTING.md` aligned with the wrapper commands as direct workflow
   commands are replaced.
