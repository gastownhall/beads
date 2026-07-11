# CI Cleanup Plan

Last reviewed: 2026-05-29

Freshness source: `engdocs/CI_TEST_SURFACE_AUDIT.md`, `.github/workflows/*.yml`,
`.buildflags`, `.golangci.yml`, package test manifests, and maintainer decision
review on 2026-05-28.

This document records the agreed target shape for CI cleanup. It is the policy
and roadmap layer; the current inventory remains in
[`CI_TEST_SURFACE_AUDIT.md`](CI_TEST_SURFACE_AUDIT.md).

## Goals

- Make every important CI tier reproducible through a repository-owned command.
- Keep PR checks fast, required, and Linux-only unless risk justifies more.
- Run expensive platform and integration coverage on `main`, manual dispatch, or
  scheduled background jobs after measuring wall-clock cost.
- Make release/package checks rerun release-critical validation before
  publishing, independent of earlier `main` success.
- Preserve current behavior first, then measure and promote additional suites.

## Non-Goals

- Do not make `.test-skip` part of CI. It is a local human optimization file.
- Do not run macOS or Windows checks on PRs by default.
- Do not make Codecov/upload success block PRs or `main`.
- Do not broaden `pull_request_target` usage for package validation.

## Tier Model

| Tier | Trigger | Required | Platform | Purpose |
|---|---|---:|---|---|
| `pr-core` | Every PR and merge queue run | Yes | Linux | Fast baseline Go validation for the shipped default path. |
| `pr-policy` | Every PR and merge queue run | Yes | Linux | Repository policy checks that should fail before expensive tests matter. |
| `pr-lint` | Every PR and merge queue run | Yes | Linux | Required `gofmt` and `golangci-lint` gate. |
| `pr-risk-*` | PRs matching risky paths or maintainer labels | Yes when applicable | Linux | Descriptive risk checks such as embedded, regression, Nix, packages, and release paths. |
| `main-*` | Every push to `main` | Yes for branch health | Linux plus selected macOS/Windows | Detect after-merge issues from direct pushes and platform-specific behavior. |
| `measure-*` | Manual dispatch | No | Per suite | Collect wall-clock and sharding data before promoting suites. |
| `nightly-*` | Scheduled/manual | No, but failures require triage | Linux unless measured otherwise | Expensive background coverage not ready for every `main` push. |
| `release-*` | Tags/manual release | Yes before publish | Per artifact | Re-run release-critical checks and publish only after package gates pass. |

`merge_group` means GitHub Merge Queue. Treat it like a PR event: run Linux
`pr-core`, `pr-policy`, `pr-lint`, and the same risk checks; do not add macOS or
Windows there.

## Required PR Checks

Every PR, including docs-only PRs, should run the required Linux baseline:
`pr-core`, `pr-policy`, and `pr-lint`.

## Wrapper Conventions

Shell scripts under `scripts/ci/` are the source of truth. Make targets should
be aliases for discoverability, not a second implementation of command policy.

Wrapper rules:

- Auto-detect the repository root from the script path.
- Source `.buildflags` except for explicitly special modes such as no-CGO or
  unsupported-install checks.
- Do not require a clean worktree.
- Clean up temporary files that the wrapper creates.
- Use `scripts/ci/lib/timing.sh` for new measured command blocks.
- Keep CI-specific reporting such as `gotestsum`, JUnit, and artifacts in the
  workflow layer unless the wrapper needs to own the behavior.

## Build Artifact Stage

Target CI topology should start with a required Linux `build` stage that owns
static validation and expensive reusable compilation. Test, package, smoke, and
risk jobs should depend on that stage and consume its artifacts instead of
rebuilding equivalent binaries independently.

The first-stage job should include:

- Static/policy gates: build-tag policy, unsupported install guidance, version
  consistency, generated CLI docs freshness, `.beads/issues.jsonl` protection,
  `gofmt`, and `golangci-lint`.
- A Linux `bd` subprocess binary built with the repository default build tags
  from `.buildflags`.
- Artifact checksums and a short build manifest recording commit, Go version,
  build tags, and artifact names.
- Shard manifests for any measured stable shards so downstream jobs test the
  same commit-derived assignment.

Artifacts should use short retention and immutable names scoped to the run, not
branch-global names. Downstream jobs should verify the checksum when practical
and pass the downloaded `bd` path through `BEADS_TEST_BD_BINARY` for
subprocess-style tests.

Initial artifact set:

| Artifact | Producer | Consumers |
|---|---|---|
| `bd-linux-gms-pure` | Linux build stage | PR/main smoke, package checks, cross-version smoke candidate, `cmd/bd` subprocess tests through `BEADS_TEST_BD_BINARY`. |
| `linux-integration-packages-*` | Linux build stage or integration setup | No-short integration package shards. |
| `linux-integration-cmd-bd-tests-*` | Linux build stage or integration setup | `cmd/bd` integration shards. |
| `embedded-test-binaries` | Embedded build stage when applicable | Embedded storage and `cmd/bd` shards. Existing workflow already follows this pattern. |
| `bd-cmd-test-linux-integration-race` | Future measured build stage | Candidate replacement for `go test` compilation in `cmd/bd` integration shards. |

Do not share Linux artifacts with macOS or Windows jobs. Platform-specific
main/release jobs need their own build stages if they should reuse artifacts.
Do not reuse untrusted PR artifacts in privileged release workflows; release
jobs must build or verify release artifacts in the release context.

For PR and merge queue, the target shape is:

1. `build`: required static validation plus reusable Linux artifacts.
2. `pr-core`: required Linux short tests, consuming the prebuilt `bd` binary for
   subprocess tests.
3. Optional required-when-applicable risk jobs, also consuming build artifacts.

For `main`, keep the same first-stage pattern but broaden consumers to the
measured main lanes: Linux no-short integration, embedded, regression, coverage,
macOS short, and Windows smoke. Main can tolerate more jobs than PRs, but should
still avoid rebuilding the same Linux candidate binary in each consumer.

Initial implementation on branch `ci/bd-am3.1-wrapper-commands` adds the
`Build Artifacts` CI job. It runs `make ci-pr-policy`, `make ci-pr-lint`, builds
`bd-linux-gms-pure`, writes `SHA256SUMS` and `build-manifest.txt`, then uploads
the run-scoped `ci-build-artifacts` artifact. The first consumers are
`PR Core (wrapper timing)`, `Test (ubuntu-latest)`, and
`Test (storage domain + uow)`, all of which verify `SHA256SUMS`; the Linux test
consumers pass the binary through `BEADS_TEST_BD_BINARY` for subprocess tests.
The legacy cross-platform `Test` matrix and Windows smoke job are gated to
push-to-main only, so PR and merge queue events do not run macOS, Windows, or
coverage collection.

### `pr-core`

Initial wrapper behavior must preserve the current Linux PR command exactly:

```bash
source ./.buildflags
go test -race -short -skip '^TestEmbedded' ./...
```

CI may wrap that command with `gotestsum` for logs and JUnit, but the underlying
test contract must remain identical during the first migration.

Additional rules:

- Use `.buildflags` so `gms_pure_go` remains the default shipped path.
- Keep `-race`.
- Keep `-short` initially only to avoid behavior drift.
- Do not generate coverage in PRs.
- Do not read `.test-skip`.

### `pr-policy`

`pr-policy` should be a separate wrapper from `pr-core`. It should include:

- Build tag policy: `scripts/check-build-tags.sh`.
- Unsupported `go install` guidance: `scripts/check-go-install-guidance.sh`.
- Version consistency: `scripts/check-versions.sh`.
- CLI flag freshness: `make check-docs` or the underlying doc flag script.
- PR guard for `.beads/issues.jsonl` changes.

### `pr-lint`

`pr-lint` is required. It should stay separate from policy so lint failures are
easy to identify and rerun. It includes:

- `make fmt-check`.
- `golangci-lint run --timeout=5m --build-tags=gms_pure_go ./...`.

Known false positives must be handled in `.golangci.yml` or with targeted
`//nolint` comments. CI should not use a tolerated failing lint baseline.

## Risk Checks

Use separate, descriptive jobs rather than one broad "extra tests" job:

- `pr-risk-embedded`
- `pr-risk-regression`
- `pr-risk-nix`
- `pr-risk-packages`
- `pr-risk-release`

Use the robust path-gated required-check pattern:

1. A detector job always runs and decides which risk checks are applicable.
2. Each required risk check reports success when it is not applicable.
3. Applicable checks fail normally on command failure.

Embedded Dolt coverage is risk-gated on PRs and always runs on `main`. Add a
maintainer `run-embedded` label and a rare maintainer-only `skip-embedded`
override. Regression coverage follows the same pattern with `run-regression`
and `skip-regression`, while still running on every `main` push.

## Main Branch Checks

`main` should run as much as practical after measurement. Direct pushes to
`main` are allowed in this repository, so after-merge detection matters.

Initial main policy:

- Re-run Linux `pr-core`.
- Run Linux coverage collection on `main`, not on PRs or merge queue.
- Run current macOS Go test shape without coverage upload:
  `go test -tags gms_pure_go -v -race -short -skip '^TestEmbedded' ./...`.
- Run Windows smoke only for now: build, `version`, and `help`.
- Run embedded Dolt every `main` push.
- Run regression every `main` push.

Candidate promotions for every `main` push must be measured first. The working
wall-clock target is about 25 minutes total. Suites that exceed that target or
create too much queue pressure should stay manual/scheduled until sharded.

No-short integration is an intended every-main candidate, not nightly-only by
policy; promote it after measurement if wall-clock data supports it.

Coverage collection should block on local coverage generation/test failures, not
on upload service failures. Do not introduce a coverage threshold during the
first promotion step.

The `main` branch may fail after merge as a cost tradeoff, but failures should
be fixed forward or reverted promptly.

## Measurement Workflow

Add a manual-dispatch workflow before changing tier breadth.

Initial implementation lives in `.github/workflows/ci-measurements.yml` on
branch `ci/bd-am3.1-wrapper-commands`. It is manual-only for human operators:
direct `workflow_dispatch` when available, or `workflow_call` from the existing
nightly workflow while the new workflow file is still branch-local. It measures
one selected suite per dispatch so maintainers can control macOS, package, and
integration cost.

Until `.github/workflows/ci-measurements.yml` exists on `main`, dispatch it from
the branch through the existing `Nightly Full Tests` workflow by selecting any
suite other than `full-test`. After the measurement workflow is on `main`, it
can be dispatched directly.

Measurement requirements:

- One sample per suite is enough initially.
- Measure per command, not only per job, so future sharding decisions have data.
- Use a shared `scripts/ci/lib/timing.sh` helper in new wrappers.
- Print timing summaries to logs and `$GITHUB_STEP_SUMMARY`.
- Preserve command exit codes; measurement jobs should fail visibly when the
  measured command fails.
- Retain artifacts for seven days.
- Pin `gotestsum`; replace current `gotestsum@latest` opportunistically when
  touching nearby workflow code.
- Use `gotestsum` for Linux Go measurement outputs. Install it one-off in the
  workflow rather than making wrappers depend on it.

Selectable measurement suites:

- `pr-linux`: PR policy, core, and lint command timings on Linux with JUnit for
  the core Go test command.
- `macos-short`: current macOS short Go test shape.
- `macos-candidates`: macOS no-short and integration-tag candidates.
- `linux-integration`: current nightly-style integration run with
  `BEADS_TEST_SKIP=dolt`.
- `linux-integration-sharded`: same Linux integration command split across six
  stable Go package shards. Each shard uploads the package list and JUnit
  output so wall-clock tails can drive the promotion shard count.
- `linux-integration-hybrid-sharded`: the measured next iteration after
  package sharding exposed `cmd/bd` as the tail. It keeps six package shards for
  everything except `cmd/bd`, then runs `cmd/bd` across eight top-level test
  name shards.
- `linux-integration-hybrid-16-sharded`: follow-up measurement for the same
  hybrid shape, but with `cmd/bd` split across sixteen top-level test name
  shards.
- `linux-integration-hybrid-prebuilt-sharded`: follow-up measurement that
  prebuilds one `bd` subprocess binary, then runs the hybrid shape with six
  package shards and eight `cmd/bd` test-name shards.
- `linux-integration-coverage`: same integration shape with coverage generation
  and a coverage summary, but no threshold.
- `cross-version-smoke`: one previous-release smoke sample, optionally pinned
  by workflow input.
- `nix`: full `nix build .#default`.
- `mcp-package`, `npm-package`, and `website`: package and documentation probes
  for measurement. These are not promoted gates yet.

Measure at least:

- Current Linux `pr-core`, policy, and lint timing.
- Current macOS short Go test command.
- macOS no-short/integration candidates.
- Linux no-short integration preserving current nightly shape:
  `BEADS_TEST_SKIP=dolt` with `-tags=integration,gms_pure_go`.
- Linux no-short integration with coverage, matching the current nightly signal.
- A cross-version smoke sample.
- Full `nix build`.
- Package checks for MCP, npm, and website.

### Initial Wrapper Measurement Snapshot

First sample: PR #4211, workflow run 26549957718, commit
`c5fd8fc34b3f28ab5a507b02a8fc9f1faf051d13`.

This sample was collected from additive wrapper jobs on the cleanup branch, not
from `main`. Treat it as a measurement smoke test and first baseline only; do
not make final tiering or sharding decisions from one run.

| Wrapper | Job wall clock | Wrapper command timing | Current long pole |
|---|---:|---:|---|
| `pr-policy` | 91s | 65s | `build bd for docs checks` at 53s |
| `pr-core` | 593s | 577s | `go test -race -short -skip '^TestEmbedded' ./...` |
| `pr-lint` | 211s | 143s, plus 54s tool install | `golangci-lint` at 143s |

Same-run job-level observations:

- Existing Linux short test job took 607s, matching the new `pr-core` wrapper
  shape closely enough for the first no-drift check.
- macOS short test took 383s; keep it off PRs and measure again before deciding
  whether to shard main-platform coverage.
- Windows smoke took 237s; keep it as smoke-only until there is evidence that
  broader Windows tests are worth the queue cost.
- Embedded Dolt had a slow-shard tail: storage 355s, cmd 19/20 403s, cmd 7/20
  387s, and cmd 4/20 334s. Sharding decisions should use repeated samples.

Second sample: branch-dispatched `pr-linux` measurement via `Nightly Full
Tests`, workflow run 26551186971, commit
`f4918fdaf97d659b568068ad2c1cfee88c8f118d`.

| Command | Duration | Notes |
|---|---:|---|
| `install gotestsum` | 10s | Pinned `gotestsum@v1.13.0`. |
| `install golangci-lint` | 41s | Pinned `golangci-lint@v2.9.0`. |
| `pr-policy wrapper` | 46s | Long pole was docs-check binary build at 32s. |
| `pr-core gotestsum` | 461s | Uploaded `pr-core-junit.xml` artifact. |
| `pr-lint wrapper` | 17s | Excludes lint tool install because install is measured separately. |

The full job wall clock was 607s. An earlier branch-dispatched run,
26550681849, failed visibly in `pr-core gotestsum` after 566s with
`TestInitRepairsPermissiveBeadsDir` hitting a `TempDir RemoveAll` cleanup error
for a non-empty `.git` directory. The workflow now continues independent
commands after a failure and exits nonzero at the end, so later samples still
collect lint timing when core flakes.

### Branch Measurement Batch

The first broad batch was dispatched from branch
`ci/bd-am3.1-wrapper-commands`, commit
`35135f235e49051bb777bcbb31d95291766eb190`, through `Nightly Full Tests` while
the reusable measurement workflow was still branch-local.

| Suite | Run | Job wall clock | Result | Command timings |
|---|---:|---:|---|---|
| `website` | 26552244751 | 72s | Pass | `npm ci` 9s; typecheck 1s; `llms-full` 1s; build 45s. |
| `nix` | 26552243745 | 209s | Pass | `nix build .#default` 197s. |
| `macos-short` | 26552238862 | 392s | Pass | macOS short Go test 316s. |
| `macos-candidates` | 26552239802 | 774s | Fail | no-short Go test 370s; integration-tag Go test 341s. |
| `linux-integration` | 26552240869 | 664s | Fail | install `gotestsum` 10s; integration Go test 632s. |
| `linux-integration-coverage` | 26552241818 | 818s | Fail | install `gotestsum` 13s; integration coverage Go test 790s; manual coverage summary from artifact was 37.9%. |
| `cross-version-smoke` | 26552242789 | 163s | Fail | candidate binary build 150s; previous-release smoke failed before running because no release tag was resolved. |
| `npm-package` | 26552245608 | 201s | Fail | package binary build 161s; install 0s; `npm run test:all` 23s. |
| `mcp-package` | 26552246442 | 173s | Fail | install `uv` 8s; package binary build 150s; `uv sync` 2s; Ruff failed before later checks ran. |

Failure modes from this batch:

- `macos-candidates`: no-short passed, but integration-tag tests failed in
  `internal/beads` on symlink deduplication, in `internal/doltserver` with
  repeated `fatal: empty ident name not allowed`, and in `internal/storage/dolt`
  on missing `depends_on_external`.
- `linux-integration` and `linux-integration-coverage`: both reported
  `cmd/bd TestAutoMigrateOnVersionBump_NoDatabase` through gotestsum and failed
  three `internal/storage/dolt` routing tests because `depends_on_external` was
  missing from the queried schema.
- `cross-version-smoke`: the workflow did not pass an explicit release tag and
  the script could not infer one. The measurement workflow now resolves the
  latest GitHub release tag when the input is empty.
- `npm-package`: the Claude Code for Web simulation failed because the expected
  JSONL file was not created. The measurement workflow now continues to the
  package dry-run after `test:all` failures.
- `mcp-package`: Ruff found 130 errors. The measurement workflow now continues
  through mypy, pytest, and build after independent package check failures.

Initial tiering read:

- `website` and `nix` are cheap enough to be good PR-risk or main candidates,
  pending path-gating policy.
- `macos-short` is feasible for `main` but still too expensive for default PRs.
- Linux integration and coverage are close to 11-14 minutes per unsharded job
  and currently fail, so measure sharding and fix failures before promotion.
- Package gates need cleanup before they can protect releases or risk paths.

Follow-up robustness reruns used commit
`d6b1314d8a7ab2c85656cfa440ca2c4cd8620087`, after the measurement workflow was
updated to continue independent package commands and resolve the default smoke
release tag:

| Suite | Run | Job wall clock | Result | Added signal |
|---|---:|---:|---|---|
| `cross-version-smoke` | 26552895263 | 171s | Pass | Auto-resolved `v1.0.4`; candidate build 149s; upgrade smoke 7s. |
| `npm-package` | 26552895239 | 212s | Fail | Binary build 154s; install 2s; `test:all` still failed after 23s; pack dry-run succeeded in 13s with 7 files, 79.9 MB package size, and 189.5 MB unpacked size. |
| `mcp-package` | 26552895273 | 264s | Fail | Install `uv` 2s; binary build 156s; `uv sync` 2s; Ruff failed with 130 errors; mypy failed with 4 errors in 2 files after 13s; pytest failed after 72s with 5 failed, 190 passed, 5 skipped, and 15 errors; build still succeeded. |
| `linux-integration-coverage` | 26552895222 | 856s | Fail | Install `gotestsum` 13s; coverage Go test failed after 821s with the same four failures as the first sample; coverage summary still ran and reported 37.9%. |

Roadmap updates from the measurement evidence:

- Keep the wrapper timing jobs additive until branch protection can switch to
  `pr-core`, `pr-policy`, and `pr-lint` by name.
- Promote `cross-version-smoke` to use explicit or auto-resolved release tags;
  the candidate build dominates its runtime, while the smoke scenarios are
  cheap once the binary exists.
- Treat `website` and `nix` as good first PR-risk candidates because they are
  relatively cheap and already green in the measurement workflow.
- Do not promote `npm-package` or `mcp-package` as blocking gates until the
  measured package failures are fixed. They are still release-critical checks,
  so cleanup should happen before release workflow hardening.
- Do not promote Linux integration coverage yet. It is currently red and near
  the upper end of the 25-minute total main-branch budget before sharding.
- Use the collected per-command timings to shard around the long poles: Go
  package tests, candidate binary builds for package/smoke jobs, and macOS
  integration-tag tests.

Action taken from this evidence:

- Added `linux-integration-sharded`, a six-way package-sharded measurement lane
  for the no-short Linux integration command. The first sharded run should
  replace the unsharded `linux-integration` sample for main-promotion sizing;
  keep unsharded coverage as a background measurement until coverage merge and
  shard-count policy are explicit.

First sharded run: 26569078396, commit
`ca22fe505a55594cae67124190540c3c9ee2a5f6`.

| Shard | Go test time | Result | Read |
|---:|---:|---|---|
| 1/6 | 542s | Pass | Tail shard; contains `cmd/bd` and `internal/doltserver`. |
| 2/6 | 179s | Pass | Contains `internal/storage/dolt`; no longer the tail under package sharding. |
| 3/6 | 174s | Fail | `internal/beads` duplicate `TestMain`; fixed after this run by moving integration setup behind the existing package `TestMain`. |
| 4/6 | 174s | Pass | Package shard is in the same range as shards 2/3. |
| 5/6 | 36s | Pass | Lightweight shard. |
| 6/6 | 77s | Pass | Lightweight shard. |

Follow-up after fixing the `internal/beads` duplicate `TestMain`: 26569719809,
commit `bc78a25a8c3773012c476ecd9adb275770e75f05`.

| Shard | Go test time | Result |
|---:|---:|---|
| 1/6 | 542s | Pass |
| 2/6 | 177s | Pass |
| 3/6 | 177s | Pass |
| 4/6 | 170s | Pass |
| 5/6 | 38s | Pass |
| 6/6 | 61s | Pass |

Package sharding is useful but not enough by itself: the full wall-clock tail is
still the `cmd/bd` shard. The next optimization should split `cmd/bd` by
top-level test names, then keep package sharding for the remaining packages.
The `linux-integration-hybrid-sharded` measurement suite implements that next
shape.

First hybrid run: 26596629423, commit
`7bfe2dc55cf257a179f121b33ed7f327a3e20dac`.

| Shard group | Shard | List time | Go test time | Job wall clock | Result |
|---|---:|---:|---:|---:|---|
| Packages | 1/6 | 15s | 43s | 89s | Pass |
| Packages | 2/6 | 13s | 177s | 219s | Pass |
| Packages | 3/6 | 15s | 156s | 199s | Pass |
| Packages | 4/6 | 14s | 173s | 218s | Pass |
| Packages | 5/6 | 13s | 37s | 81s | Pass |
| Packages | 6/6 | 13s | 73s | 115s | Pass |
| `cmd/bd` | 1/8 | 186s | 352s | 567s | Pass |
| `cmd/bd` | 2/8 | 185s | 356s | 570s | Pass |
| `cmd/bd` | 3/8 | 183s | 356s | 570s | Pass |
| `cmd/bd` | 4/8 | 184s | 378s | 596s | Pass |
| `cmd/bd` | 5/8 | 188s | 375s | 592s | Pass |
| `cmd/bd` | 6/8 | 184s | 363s | 576s | Pass |
| `cmd/bd` | 7/8 | 149s | 299s | 479s | Pass |
| `cmd/bd` | 8/8 | 174s | 339s | 539s | Pass |

This proved the hybrid split is structurally valid, but it also exposed a
measurement-specific inefficiency: `go test -list` was spending 149-188s per
`cmd/bd` shard just to discover test names. The workflow now uses the
build-tag-aware `scripts/ci/go-list-test-names` AST helper instead. Local
validation confirmed the helper exactly matched `go test -list '^Test'` for
`./cmd/bd` under `integration,gms_pure_go` and excludes `TestMain`.

Follow-up hybrid run with AST-based test discovery: 26597526345, commit
`4721974f3fa97889b6d34012c533b6a5c44bc012`.

| Shard group | Shard | List time | Go test time | Job wall clock | Result |
|---|---:|---:|---:|---:|---|
| Packages | 1/6 | 12s | 42s | 88s | Pass |
| Packages | 2/6 | 12s | 178s | 217s | Pass |
| Packages | 3/6 | 15s | 175s | 220s | Pass |
| Packages | 4/6 | 16s | 167s | 216s | Pass |
| Packages | 5/6 | 14s | 37s | 80s | Pass |
| Packages | 6/6 | 12s | 76s | 122s | Pass |
| `cmd/bd` | 1/8 | 16s | 486s | 536s | Pass |
| `cmd/bd` | 2/8 | 16s | 465s | 507s | Pass |
| `cmd/bd` | 3/8 | 15s | 474s | 518s | Pass |
| `cmd/bd` | 4/8 | 15s | 501s | 545s | Pass |
| `cmd/bd` | 5/8 | 16s | 488s | 531s | Pass |
| `cmd/bd` | 6/8 | 16s | 500s | 545s | Pass |
| `cmd/bd` | 7/8 | 18s | 548s | 597s | Pass |
| `cmd/bd` | 8/8 | 17s | 483s | 534s | Pass |

AST discovery removed the explicit list-time tax, but overall wall-clock did
not materially improve because `go test -list` had also warmed package
compilation. The next measurement should keep the package split and compare a
sixteen-way `cmd/bd` split. If the sixteen-way tail remains close to ten
minutes, the next optimization should be duration-weighted assignment or a
prebuilt shared test binary rather than more count-based shards.

Sixteen-way hybrid run: 26598339170, commit
`784e8a8d52eabd95d2816c46d41d11ca8e18ecf9`.

| Shard group | Shard | List time | Go test time | Job wall clock | Result |
|---|---:|---:|---:|---:|---|
| Packages | 1/6 | 13s | 41s | 86s | Pass |
| Packages | 2/6 | 13s | 175s | 221s | Pass |
| Packages | 3/6 | 11s | 183s | 221s | Pass |
| Packages | 4/6 | 11s | 168s | 207s | Pass |
| Packages | 5/6 | 15s | 38s | 81s | Pass |
| Packages | 6/6 | 12s | 64s | 108s | Pass |
| `cmd/bd` | 1/16 | 18s | 334s | 383s | Pass |
| `cmd/bd` | 2/16 | 14s | 473s | 517s | Pass |
| `cmd/bd` | 3/16 | 17s | 480s | 530s | Pass |
| `cmd/bd` | 4/16 | 14s | 503s | 546s | Pass |
| `cmd/bd` | 5/16 | 15s | 508s | 553s | Pass |
| `cmd/bd` | 6/16 | 15s | 480s | 524s | Pass |
| `cmd/bd` | 7/16 | 15s | 485s | 533s | Pass |
| `cmd/bd` | 8/16 | 18s | 476s | 522s | Pass |
| `cmd/bd` | 9/16 | 17s | 467s | 517s | Pass |
| `cmd/bd` | 10/16 | 17s | 380s | 428s | Pass |
| `cmd/bd` | 11/16 | 14s | 482s | 529s | Pass |
| `cmd/bd` | 12/16 | 18s | 497s | 547s | Pass |
| `cmd/bd` | 13/16 | 19s | 472s | 521s | Pass |
| `cmd/bd` | 14/16 | 16s | 481s | 528s | Pass |
| `cmd/bd` | 15/16 | 16s | 479s | 530s | Pass |
| `cmd/bd` | 16/16 | 19s | 474s | 523s | Pass |

The sixteen-way split reduced the runner-time `cmd/bd` tail only from 597s to
553s, while the overall workflow took about 14m22s because the wider matrix
queued. This is not enough improvement to justify more count-based sharding for
`main`.

JUnit artifacts showed the cause: many binary-subprocess tests were spending
about 140-144s in the first test that called a `go build` helper for that
shard. The subprocess test helpers now honor `BEADS_TEST_BD_BINARY` before
falling back to per-process builds. The next measurement is
`linux-integration-hybrid-prebuilt-sharded`, which prebuilds one `bd` binary
and reuses it across eight `cmd/bd` shards.

Prebuilt-binary hybrid run: 26599616086, commit
`4d186ad8024f4ab2d1315ff3d4c51da19429f42c`.

| Shard group | Shard | List/build time | Go test time | Job wall clock | Result |
|---|---:|---:|---:|---:|---|
| Prebuild | `bd` | 152s | n/a | 172s | Pass |
| Packages | 1/6 | 11s | 42s | 82s | Pass |
| Packages | 2/6 | 13s | 172s | 213s | Pass |
| Packages | 3/6 | 12s | 174s | 214s | Pass |
| Packages | 4/6 | 13s | 176s | 223s | Pass |
| Packages | 5/6 | 11s | 36s | 75s | Pass |
| Packages | 6/6 | 12s | 77s | 121s | Pass |
| `cmd/bd` | 1/8 | 15s | 333s | 380s | Pass |
| `cmd/bd` | 2/8 | 15s | 330s | 376s | Pass |
| `cmd/bd` | 3/8 | 18s | 331s | 380s | Pass |
| `cmd/bd` | 4/8 | 16s | 331s | 378s | Pass |
| `cmd/bd` | 5/8 | 16s | 330s | 386s | Pass |
| `cmd/bd` | 6/8 | 18s | 341s | 392s | Pass |
| `cmd/bd` | 7/8 | 16s | 344s | 394s | Pass |
| `cmd/bd` | 8/8 | 14s | 337s | 383s | Pass |

Repeat prebuilt-binary hybrid run: 26604010187, commit
`c0ca395d6f332d1ee6b22f5257c56aec296bd648`.

| Shard group | Shard | List/build time | Go test time | Job wall clock | Result |
|---|---:|---:|---:|---:|---|
| Prebuild | `bd` | 156s | n/a | 180s | Pass |
| Packages | 1/6 | 12s | 41s | 79s | Pass |
| Packages | 2/6 | 12s | 184s | 230s | Pass |
| Packages | 3/6 | 12s | 179s | 218s | Pass |
| Packages | 4/6 | 12s | 172s | 216s | Pass |
| Packages | 5/6 | 13s | 36s | 80s | Pass |
| Packages | 6/6 | 12s | 73s | 112s | Pass |
| `cmd/bd` | 1/8 | 16s | 345s | 400s | Pass |
| `cmd/bd` | 2/8 | 15s | 329s | 373s | Pass |
| `cmd/bd` | 3/8 | 19s | 259s | 308s | Pass |
| `cmd/bd` | 4/8 | 17s | 337s | 391s | Pass |
| `cmd/bd` | 5/8 | 15s | 340s | 394s | Pass |
| `cmd/bd` | 6/8 | 16s | 331s | 383s | Pass |
| `cmd/bd` | 7/8 | 16s | 326s | 372s | Pass |
| `cmd/bd` | 8/8 | 16s | 356s | 410s | Pass |

The repeat run passed and remained below the target: about 11m38s from dispatch
creation to completion, and about 10m41s from first job start to completion.
The `cmd/bd` tail was 410s, compared to 394s in the first prebuilt sample.
This prebuilt eight-way hybrid is now the first promoted every-`main` Linux
no-short integration shape in `.github/workflows/main.yml`: six package shards
exclude `cmd/bd`, eight `cmd/bd` shards split by top-level test name, and all
shards consume the `ci-build-artifacts` `bd-linux-gms-pure` binary. Further
optimization should target precompiling the `cmd/bd` test binary itself or
reducing the remaining slow test bodies, not adding more count-based shards.

## Package Gates

Package checks should be reusable from PR risk jobs, measurement jobs, `main`,
and release workflows.

Initial package gate implementation on branch `ci/bd-am3.1-wrapper-commands`
adds `scripts/ci/package-mcp.sh`, `scripts/ci/package-npm.sh`, and
`scripts/ci/website.sh`, with Make aliases `ci-package-mcp`,
`ci-package-npm`, and `ci-website`. CI uses `scripts/ci/detect-package-gates.sh`
to run these jobs only when package, website, release, workflow, or wrapper
paths are relevant; otherwise each package gate reports success as not
applicable. MCP and npm gates consume the `Build Artifacts` `bd-linux-gms-pure`
binary through `BEADS_TEST_BD_BINARY`.

### MCP Python Package

Build a candidate `bd` once and put it on `PATH`. Test only the `bd` binary
name, not the `beads` alias.

Wrapper command sequence:

```bash
go build -tags gms_pure_go -o /tmp/bd-mcp-test ./cmd/bd
cd integrations/beads-mcp
uv sync --all-groups --locked
uv run ruff check src/beads_mcp tests
uv run mypy src/beads_mcp
uv run pytest --durations=50
uv build
```

### npm Package

`npm-package` currently has no lockfile, so use `npm install` until a separate
packaging cleanup adds one. Build the native binary expected by
`npm-package/bin/bd.js`, and clean it up on exit by default.

Wrapper command sequence:

```bash
go build -tags gms_pure_go -o npm-package/bin/bd ./cmd/bd
cd npm-package
npm install
npm run test:all
npm pack --dry-run
```

The existing integration test already exercises a real `npm pack`; keep both
that real pack and the explicit dry-run file-list check.

The npm wrapper forces `CI=1` by default so `npm install` and package
installation tests do not download the latest published release in `postinstall`.
That keeps the gate focused on the candidate binary copied into
`npm-package/bin/bd`.

### Website

Classify `scripts/generate-llms-full.sh` as a docs/website check, not generic
policy.

Wrapper command sequence:

```bash
cd website
npm ci
npm run typecheck
cd ..
./scripts/generate-llms-full.sh
cd website
npm run build
```

Keep the internal link check in Actions through the existing Lychee workflow
step. External link checking remains non-blocking.

## Mandatory `testing.Short()` Cleanup

`-short` currently does double duty: it suppresses true slow tests and also acts
as an implicit integration/e2e tier boundary. That is mandatory cleanup before
the tier policy is considered complete.

Plan:

1. Audit every `testing.Short()` use.
2. Keep `testing.Short()` only for runtime, stress, or large-fixture skips.
3. Move integration, e2e, API, Docker, and external dependency boundaries to
   explicit build tags, environment checks, or named wrappers.
4. Update `engdocs/TESTING.md` after wrapper commands exist.

Initial cleanup on branch `ci/bd-am3.1-wrapper-commands` leaves
`testing.Short()` only in approved timeout, stress, and large-fixture tests:
`internal/hooks/hooks_test.go`, `internal/testutil/fixtures/fixtures_test.go`,
and `internal/storage/dolt/concurrent_test.go`.

The same change removes integration/e2e/API/Docker boundary skips from tagged
or environment-gated suites, promotes ADO/Linear round-trip and Dolt autostart
tests from `cgo`-only to `cgo && integration`, and renames the remaining
admin embedded tests to `TestEmbedded*` so the embedded shard runner owns them
explicitly. `scripts/check-testing-short.sh` now enforces the allowlist through
`make check-testing-short` and `make ci-pr-policy`.

## Release Policy

Release/tag workflows must independently re-run release-critical checks even
when `main` was green. Publishing should happen only after package-specific
checks pass.

- Reuse `scripts/ci/package-*` wrappers in release jobs.
- MCP release may upload `dist/*` produced by the same validated `uv build`.
- npm release should explicitly build/copy the native binary needed for
  publishing; the validation wrapper should clean temporary binaries by default.
- Keep privileged automation isolated and audited. Do not broaden normal
  validation to `pull_request_target`.

Initial release hardening on branch `ci/bd-am3.1-wrapper-commands` adds
release-only MCP, npm, and website package gate jobs to `.github/workflows/release.yml`.
GoReleaser now waits for all three gates before publishing GitHub release
assets. PyPI publishing downloads the validated `beads-mcp` `dist/*` artifact
from the MCP gate instead of rebuilding it in the publish job. npm publishing
waits for the npm package gate and both Linux/Windows and macOS GitHub release
artifacts, so `postinstall` URLs are populated before the package is published.

## Implementation Order

1. Add `scripts/ci/*` wrappers and `scripts/ci/lib/timing.sh`; add Make aliases.
   Initial PR wrappers exist on branch `ci/bd-am3.1-wrapper-commands`.
2. Wire existing workflows to wrappers with no intentional behavior drift.
   Additive PR timing jobs exist on branch `ci/bd-am3.1-wrapper-commands`; the
   existing direct jobs remain in place until the wrapper jobs are promoted.
3. Introduce the Linux `build` stage and route PR/merge queue consumers through
   its reusable `bd` artifact. Keep behavior equivalent while measuring the
   artifact handoff overhead. Initial job and first consumers exist on branch
   `ci/bd-am3.1-wrapper-commands`; current consumers are PR Core, Linux short
   coverage tests, and storage domain+uow.
4. Add the manual measurement workflow and pinned `gotestsum`.
   Initial workflow exists on branch `ci/bd-am3.1-wrapper-commands`; the legacy
   Linux coverage install has been pinned from `gotestsum@latest` to
   `gotestsum@v1.13.0`.
5. Add package wrappers and risk/measurement usage for MCP, npm, and website.
   Initial wrappers, measurement reuse, website deploy reuse, and path-gated CI
   package jobs exist on branch `ci/bd-am3.1-wrapper-commands`.
6. Perform the mandatory `testing.Short()` audit and cleanup.
   Initial cleanup and policy enforcement exist on branch
   `ci/bd-am3.1-wrapper-commands`.
7. Promote measured suites to `main` or scheduled jobs based on wall-clock data.
   The measured prebuilt Linux no-short integration hybrid is promoted to
   every-`main` CI on branch `ci/bd-am3.1-wrapper-commands`.
8. Harden release workflows to reuse package wrappers before publishing.
   Initial release-only package gates and publish-job dependencies exist on
   branch `ci/bd-am3.1-wrapper-commands`.
9. Split workflows by tier/domain once wrappers are stable:
   `pr.yml`, `pr-risk.yml`, `main.yml`, `release.yml`, and `nightly.yml`.
   Initial split exists on branch `ci/bd-am3.1-wrapper-commands`: the old
   monolithic `.github/workflows/ci.yml` has been replaced by
   `.github/workflows/pr.yml`, `.github/workflows/pr-risk.yml`, and
   `.github/workflows/main.yml`. Job display names are intentionally preserved
   where practical so branch-protection migration can be handled separately.
10. Add stable aggregate required-check candidates after the split.
    Initial aggregate jobs exist on branch `ci/bd-am3.1-wrapper-commands` as
    `PR / CI Gate / Required` and `PR Risk / CI Gate / Required`, backed by
    `.github/scripts/ci-gate.sh`.

## Deferred Decisions

- Exact branch-protection rollout timing after the aggregate check names appear
  and pass on a live PR/merge queue run.
- Whether no-CGO should become a full all-package gate or remain focused.
- Coverage thresholds for promoted main suites.
- Final sharding strategy for macOS, integration, and embedded jobs.
- Whether to promote a precompiled `cmd/bd` test binary after measurement.
- npm lockfile policy for `npm-package`.
