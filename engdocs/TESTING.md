# Testing Guide

`TESTING.md` is the single authority for test commands, test selection, and
test design in this repository. Use the commands here for local work.

## Building and Testing: Bazel Is the Gate

Bazel is beads' build and test system. CI gates every pull request on the
`bazel test` lanes in `.github/workflows/bazel.yml`, and several checks exist
only as Bazel targets: nogo (go test's vet checks plus the golangci-lint
linters, `tools/nogo`), gofmt (`//scripts/repochecks:fmt_test`) and the
repository guards under `scripts/repochecks`. A plain `go test` runs none of
them, so it is an inner-loop convenience CI does not enforce; a change is
ready when its Bazel lanes pass.

Install [Bazelisk](https://github.com/bazelbuild/bazelisk) as `bazel`; it
reads `.bazelversion`. The pre-commit hook (nogo on staged packages) and the
pre-push hook (the test lane) both need it.

Where actions run is per machine, never committed:

- **Contributors** read rbe-west's anonymous, read-only cache with
  `--config=fork-cache`: everything CI already executed is a cache hit, misses
  run on your machine, and nothing is uploaded. Make it the default with
  `build --config=fork-cache` in your gitignored `.bazelrc.local`, or pass it
  per command (`make test BAZEL_FLAGS=--config=fork-cache`).
- **Maintainers** with an rbe-west client certificate execute remotely with
  `--config=remote-exec`; the executor endpoint and TLS lines go in the
  gitignored `user.bazelrc` or `.bazelrc.local` (see the `.bazelrc` comments).
- **Agent hosts** whose `~/.bazelrc` names the executor need neither flag.

Each lane, as bazel.yml runs it (add your `--config=fork-cache` or
`--config=remote-exec` when it is not your default):

| Lane (bazel.yml job) | Command | Notes |
|---|---|---|
| Test (`bazel-test`) | `make test`, i.e. `bazel test //... --config=ci` | PR Core's selection: race, `-short`, skips. Includes nogo, gofmt and the repository guards. The default gate for every Go change. |
| Lint, all platforms | `make ci-pr-lint` | nogo natively plus the windows/amd64 and darwin/arm64 passes. `make lint-changed` covers only your changed packages. |
| Pure-Go (`bazel-pure`) | `bazel build --config=pure //cmd/bd:bd //cmd/bd:bd_test` | cgo off. The job's cmd/bd test subset (`PURE_CMD_BD_TESTS`) and js/wasm step are in bazel.yml. |
| Release cross-compile (`bazel-release-cross`) | `./scripts/ci/bazel-release-cross-compile.sh` | Every `go_library`/`go_binary` for each row of `scripts/ci/release-targets.txt`, cgo off, with nogo. |
| Integration (`bazel-integration`) | `bazel test //... --config=integration` | The `integration`-tagged build. Runs with the read-only cache too. |
| Dolt server (`bazel-doltserver`) | `bazel test //... --config=doltserver` | Starts its own `dolt sql-server` from the pinned binary; no docker. |
| cmd/bd Dolt server (`bazel-cmd-dolt`) | `bazel test //cmd/bd:bd_dolt_server_test --config=doltserver-cmd` | 16 shards of the integration-tagged cmd/bd suite. |
| Embedded Dolt (`bazel-embedded`) | `bazel test //... --config=embedded` | Remote execution only in CI; locally it is slow. |
| Proxied server (`bazel-proxied`) | `bazel test //... --config=doltserver-proxied` | Remote execution only: 30 shards, each with a Dolt server. |
| Server-Dolt storage (`bazel-server-storage`) | `bazel test //... --config=doltserver-integration` | Remote execution only. |
| Docs | `make check-docs` | `//test/docsync:docsync_test` and `//scripts/repochecks:doc_freshness_test` in the test lane's configuration, then `scripts/check-doc-flags.sh` against the pinned release. |

`make check` runs the testing.Short policy, `make ci-pr-lint` and
`make test`. After adding, removing or renaming Go files or changing imports
or `go.mod`, run `make bazel-sync` (gazelle, the `go_srcs` filegroups and
`MODULE.bazel`).

The Makefile keeps plain-Go twins under explicit `-go` names: `make test-go`
(`./scripts/test.sh`), `make check-go` and `make check-docs-go`. They work
offline and without Bazel, but they are not what CI enforces. GitHub Actions
jobs that still run Go-native suites call the `-go` names, never `make test`
(`TestWorkflowsNameTheirTestEngine`).

## Choose the Smallest Useful Test

Test at the lowest seam that can fail for the user-visible reason. Add a
higher tier only when it covers a distinct risk that the lower tier cannot
show: integration wiring, a real persistence property, a process boundary, or
an external contract.

This keeps feedback fast and failures readable. It does not mean every test is
a unit test: use the real boundary when the defect could live there.

| Need | Run | When |
|---|---|---|
| Docs-only validation | `git diff --check` and `make check-docs` | For prose-only changes; add any generated-doc or surface-specific link check the changed paths require. Do not run the full suite merely because a Markdown file changed. |
| Focused red/green loop | `bazel test //path/to/package:package_test --config=ci --test_filter='^TestExactName$'`, or the plain-Go `./scripts/test.sh -run '^TestExactName$' ./path/to/package/...` | While writing or fixing one behavior. The `go test` loop is fine here; the Bazel targets below are the gate. |
| Affected-package confidence | `bazel test //path/to/package/... --config=ci` | After the focused test passes; include directly affected neighbors when their contract changed. |
| Final gate | `make test` | Once after focused work on Go code is green: the whole test lane, mostly cache hits. |
| Another lane's risk | that lane's command from the table above | When the change touches what that lane covers (integration-tagged files, the Dolt server path, embedded Dolt, pure-Go builds). |
| Named CI wrapper | `make ci-pr-core` or `make ci-pr-lint` | Run the wrapper whose risk or surface is affected, or use it to reproduce that CI check. Do not run all three routinely for every edit. |
| Hook shims against real timeout implementations | `bazel test //tests/hook_timeout_backends:hook_timeout_backends_test` | After changing the hook generator in `cmd/bd/hooks.go` (then `make githooks-regen`) or anything under `.githooks/`. Runs the tracked managed sections against GNU coreutils, uutils, busybox and toybox `timeout` — the multicalls also installed as `gtimeout` alone — with and without Perl, under dash, bash and busybox ash (240 cases). About one deadline of wall time; needs no Go build. The PR-core lane runs it on every PR. |

Do not replace the focused loop with repeated full-suite runs. Run the final
`make test` once the affected tests are green. For docs-only changes, use
the docs, link, and diff checks instead.

### Git Hooks

`make install` points `core.hooksPath` at `.githooks`. The pre-commit hook
formats staged Go files and runs nogo on their packages
(`make lint-changed LINT_CHANGED_SCOPE=staged`). The pre-push hook runs
`scripts/pre-push-suite.sh` for branch pushes whose commits change Go or
Bazel inputs: `bazel test //... --config=ci`, with `--config=remote-exec`
when some rc file names an executor and `--config=fork-cache` otherwise.
`BD_PREPUSH_SUITE=rbe|cache|go` picks the mode; `go` (and the fallback when
bazel is not installed) runs `make test-go` under a banner saying it is not
the suite CI gates on.

## Plain `go test` (Inner Loop)

`./scripts/test.sh` is the plain-Go runner behind `make test-go`. It sources
`.buildflags`, creates an isolated test environment, applies `.test-skip`, and
defaults to a **25m per-package** Go-test timeout. The timeout is a hang
backstop, not a target runtime. Override it only when diagnosing a legitimate
slow path:

```bash
TEST_TIMEOUT=30m ./scripts/test.sh ./cmd/bd/...
TEST_VERBOSE=1 ./scripts/test.sh ./cmd/bd/...
TEST_RUN='^TestExactName$' ./scripts/test.sh ./cmd/bd/...

# Equivalent command-line options.
./scripts/test.sh -v -run '^TestExactName$' ./cmd/bd/...
./scripts/test.sh -timeout 30m ./cmd/bd/...
```

Use the opt-in ICU regex path only when the change requires it:

```bash
make test-icu-path
```

It is maintainer-only and not part of normal validation. `make test-full-cgo`
and `./scripts/test-cgo.sh` remain deprecated compatibility aliases.

Use a named specialized target only when its risk is in scope:

```bash
make test-regression
make test-upgrade
make test-cross-version
make test-migration
```

For a failing GitHub Actions check, run the command of its bazel.yml lane
from the table above; for the jobs that are not Bazel lanes, follow the
workflow and its `Makefile` target.

### Test Environment and Readiness

The runner isolates `HOME`, Git configuration, and Dolt state. By default its
test environment adds `dolt` to `BEADS_TEST_SKIP`; set
`BEADS_TEST_ENV_RUN_DOLT=1` only when deliberately exercising the Dolt path
and its prerequisites are available. Do not make ordinary tests depend on a
developer's database, daemon, global Git configuration, or filesystem state.

To skip an optional service explicitly, use the existing skip mechanism:

```bash
BEADS_TEST_SKIP=dolt ./scripts/test.sh ./...
```

Tests that need a Dolt SQL server get one from `internal/testutil`
(`EnsureDoltContainerForTestMain`, `RequireDoltContainer`,
`StartIsolatedDoltContainer[Handle]`, `NewContainerProvider`). Two backends
sit behind that API, selected by `BEADS_TEST_DOLT_SERVER`:

- `container`: the `dolthub/dolt-sql-server` image through testcontainers
  (needs docker and the pulled image). The default under plain `go test`.
- `local`: a `dolt sql-server` started by the test process from the pinned
  dolt CLI (`BEADS_TEST_DOLT_BINARY`, else `dolt` on `PATH`; it must be the
  image's version). No docker. Used only when explicitly selected, under
  `go test` and `bazel test` alike (a Bazel target's `env`, or
  `--test_env=BEADS_TEST_DOLT_SERVER=local`); unset means `container`, which
  in a Bazel action without docker keeps the usual skip.

`BEADS_TEST_REQUIRE_DOLT_CONTAINER=1` turns an unavailable backend into a
failure (per test and in every `TestMain`) instead of a skip; lanes that
exist to run the Dolt suites set it. `BEADS_TEST_REQUIRE_SOCAT=1` does the
same for the proxied subtests that bridge an external endpoint with `socat`
(external-unix, the outage/reconnect matrix); `//cmd/bd:bd_proxied_test`
sets it, and the legacy GitHub proxied jobs, which have no `socat`, do not.

Under Bazel, `bazel test //... --config=doltserver` runs the Dolt-backed
domain, uow, tracker, doctor/fix, protocol and testutil suites on the `local`
backend (the `dolt-server` targets); they need no docker and execute
remotely with `--config=remote-exec`. No Bazel target uses the `container`
backend; run `go test` with docker and the pulled image to exercise it.
PR Risk's heavier server tiers have configs of their own, run by bazel.yml
only with remote execution, each in a job of its own (`bazel-proxied`,
`bazel-server-storage`): `--config=doltserver-proxied` is the
proxied-server cmd/bd tier ("Test (Proxied Dolt Cmd N/15)",
`//cmd/bd:bd_proxied_test`), and `--config=doltserver-integration` the
server-Dolt storage tier ("Test (Server Dolt Conformance)", "Test (Server
Dolt Full Suite N/16)", `//internal/storage/dolt:dolt_server_*_test`), which
builds with the integration tag like `--config=integration`. Each shard runs
its CI job's shard script, so for `--config=doltserver-integration` Bazel
shard k runs the tests of job k+1 (both split the manifest's 16-shard block
the same way). `--config=doltserver-proxied`'s `bd_proxied_test` instead
runs the manifest's own 30-shard block — bin-packed by measured duration,
not the legacy jobs' 15-shard, bd-init-cost-proxy block — so shard k there
is not job k+1's tests; it is a different split of the same tests.

`--config=doltserver-cmd` (`//cmd/bd:bd_dolt_server_test`, bazel.yml's
advisory `bazel-cmd-dolt` job) runs the whole integration-tagged cmd/bd
suite on the `local` backend: the Dolt-gated cmd/bd tests (`TestCLI_*`, the
init and store-backed suites) that every other lane skips with
`BEADS_TEST_SKIP=dolt` or leaves out of its manifest. It shares
`--config=integration`'s build, passes the binary no test selection (the Go
binary shards itself over every top-level test, 16 shards), and runs where
the integration lane runs (remote, or with the read-only cache). pr.yml's
gate requires it once `BAZEL_CMD_DOLT_REQUIRED` is `"true"`; pr.yml then
also passes bazel.yml `cmd-dolt-required: true`, and the PR's
`bazel-integration` lane runs `//... -//cmd/bd:bd_test`, so each cmd/bd
integration-build test runs once (push, nightly and bazel-farm runs keep
`bd_test` in the integration lane). Locally:
`bazel test //cmd/bd:bd_dolt_server_test --config=doltserver-cmd`.

An ambient `BEADS_DOLT_SERVER_PORT` or `BEADS_DOLT_PORT` is never honored by
the suites that call `testutil.EnsureDoltContainerForTestMain`. When a test
container is started, that container's port overwrites both variables; when one
cannot be started -- for any reason, including `BEADS_TEST_SKIP=dolt` -- both
are cleared, so no environment-named server can be resolved. Point a test run
at a specific Dolt server by starting a container for it, not by exporting a
port.

The one sanctioned way to hand these suites a server you started yourself is
`./scripts/test.sh` with `BEADS_TEST_SHARED_SERVER=1`: it starts one
`dolt sql-server`, exports its port as `BEADS_DOLT_PORT`, and marks it by
exporting `BEADS_TEST_SHARED_DOLT_SERVER` set to that same port number. It
starts nothing if either port variable is still set when it gets there. The
runner's test environment clears both first, so that only happens when that
isolation is skipped (for example `BEADS_TEST_ENV_DISABLE=1`), and the script
then says so on stderr. The marker -- set only by that script, only for a
port it allocated -- is what the helper treats as container-equivalent, and
only for a variable holding exactly the port it names: that variable survives
the clearing above, and any other port variable is still cleared. A container
still wins where one can be started; the shared server is the Docker-less path.

Clearing the environment variables closes the channel the ambient port
travelled on; it does not make a port unresolvable in general. Resolution
continues into the file chain (`.beads/dolt-server.port`, `config.yaml`,
`metadata.json`), and `internal/storage/dolt`'s production-port detection is
narrower than that resolution -- see `be-rl6tm`, which tracks the remaining
gap.

Tests that need a temporary repository or store should use `t.TempDir()` and
`t.Cleanup()`. Temporary repositories must set a repository-local hooks path;
do not inherit the developer's global hooks configuration.

In `cmd/bd`, fresh-workspace command fixtures should call
`isolateBeadsDirForTest(t)` before setup or dispatch. It clears inherited
`BEADS_DIR` and restores that variable exactly at cleanup, even after raw
command-dispatch mutations. The `TestMain` reset only isolates startup.
These fixtures must not use `t.Parallel()`. Tests intentionally selecting a
workspace should use `t.Setenv("BEADS_DIR", ...)`; `initConfigForTest` and
`ensureCleanGlobalState` preserve that selection.

For manual CLI experiments, run both initialization and subsequent commands
from a disposable working directory:

```bash
beads_manual_dir="$(mktemp -d)"
(
  set -e
  cd "$beads_manual_dir"
  bd init --quiet --prefix test --skip-hooks --skip-agents
  bd create "Test issue" -p 1
)
rm -rf -- "$beads_manual_dir"
```

`BEADS_DB` selects a database for database-opening commands, but it does not by
itself redirect `bd init` workspace setup. Never run a manual `bd init` from a
production workspace merely because `BEADS_DB` points elsewhere.

**Tmpfs hosts:** the `cmd/bd` test suite creates an isolated `$HOME` and several
test binaries under `$TMPDIR`. They are normally cleaned by the test process,
but a SIGKILLed or OOMed run can leave orphans behind. On hosts where `/tmp`
is tmpfs (e.g. Fedora Atomic / Bluefin), run `make clean-test-tmp` between
test runs if `du -sh /tmp/beads-* /tmp/bd-*` shows accumulation. See bd-3q2u.

`testing.Short()` is for genuine runtime, stress, or large-fixture skips. It
is not a substitute for declaring an integration, end-to-end, API, Docker, or
external-dependency boundary. Keep new uses within the repository policy:

```bash
make check-testing-short
```

### Dolt Container Tests (podman-rootless)

Anything that does not carry `BEADS_TEST_SKIP=dolt` — including
`BEADS_TEST_ENV_RUN_DOLT=1` and a bare `go test` — reaches a real
`dolt sql-server`: by default through testcontainers-go where a container
runtime and the pinned image are present (otherwise those suites self-skip),
and from the local `dolt` CLI in the suites that use
`testutil.RequireDoltBinary`. Lanes that set
`BEADS_TEST_REQUIRE_DOLT_CONTAINER=1` fail instead of skipping. Two limitations
of the containerized path are worth recognizing before reading a failure as a
product bug. Neither failure mode manifests in production or on GitHub Actions:
CI exercises this same containerized path green on every risk-tier PR, and
`.github/workflows/pr-risk.yml` pulls the pinned image precisely to defeat the
self-skip. A hang there is a real bug, not this section's subject.

**Migration 0032 hangs over the wire protocol.** Migration `0032`
(`drop_schema_migrations_applied_at`) hangs indefinitely when applied through
the containerized sql-server. How it surfaces depends on the caller's context.
A `go test ./internal/storage/uow/... -count=1` with no `BEADS_TEST_SKIP`
passes `context.Background()`, so it sits in `initSchema` until the package
timeout rather than failing — that is the shape you will normally see, with no
deadline to cut it short. A caller that does supply one gets the hang reported
by the server-mode store open (`newServerMode` in
`internal/storage/dolt/store.go`) instead:

```
failed to initialize schema: context deadline exceeded
```

The cause is the podman-rootless container port-forwarding path, not migration
0032's SQL: the identical statement completes normally through the embedded/CLI
engine, and against a bare-host-process `dolt sql-server` matching the deployed
shape. Evidence chain in `be-j3szz`. Use `BEADS_TEST_SKIP=dolt` unless the
container path is what you are testing.

**Disabling Ryuk removes the container safety net.** This repository sets no
`TESTCONTAINERS_RYUK_DISABLED`, so CI runs with Ryuk — testcontainers-go's
orphan-reaper sidecar — enabled. The podman-rootless flow generally requires
turning it off by hand (`TESTCONTAINERS_RYUK_DISABLED=true`), because under
rootless podman Ryuk frequently cannot start: it wants the runtime socket. See
`be-w3n2m`. With Ryuk off, nothing reaps a container whose test process exited
without running its cleanup — `os.Exit` reached before a deferred
`TerminateDoltContainer`, for instance. `be-5kkk6` records the cost: 101 leaked
containers, and an exhausted swap. When running with Ryuk disabled, keep
teardown on the normal return path using the `testMainInner` pattern
(`beads_test.go`), and check for strays afterwards. Carry the rootless socket
this flow runs on — a bare `docker ps` talks to the CLI's default endpoint and
reports a false all-clear — and read the tag from the pin rather than copying
it, so the command cannot drift when the pin moves. An empty read would be the
same false all-clear (`ancestor=` matches nothing), so the command refuses to
run without a tag:

```bash
dolt_image=$(sed -n 's/.*DoltDockerImage = "\(.*\)".*/\1/p' \
  "$(git rev-parse --show-toplevel)/internal/testutil/testdoltcommon.go")
DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock \
  docker ps -a --filter "ancestor=${dolt_image:?could not read the Dolt image pin}"
```

## Test Design

### Seams, Scenarios, and Doubles

Write one scenario at the smallest seam that demonstrates the behavior. Cover
the boundary or failure mode that changes the user result; use table-driven
subtests when examples share setup. Do not repeat the same scenario through a
helper, every caller, and the CLI merely because all are available.

A recording double or fake should be narrow: model only the calls, inputs,
outputs, and failures the test needs. It should not recreate a storage engine,
process manager, or another subsystem just to make a unit test look realistic.

A behavioral fake is different. If it stands in for a contract shared by
multiple production implementations, give it the same semantic-conformance
suite as those implementations. That shared suite defines observable behavior;
it prevents the fake from teaching callers a contract production code does not
honor.

Semantic conformance asks whether an implementation produces the promised
results, errors, and state transitions for the same operation. Persistence
conformance asks whether the real persistence boundary preserves its required
durability, transaction, migration, and recovery properties. They answer
different questions. Do not claim backend parity unless a stated contract and
its conformance suite establish it.

### Tier Admission

An integration test belongs above the unit seam only when it covers a distinct
boundary that a narrow double cannot prove, such as configuration wiring, a
real filesystem or Git interaction, a subprocess protocol, or persistence
behavior.

An end-to-end test is admitted only when all of these are true:

1. The failure would be user-visible.
2. A real process, setup, or wiring boundary owns a distinct failure risk that
   no lower seam can prove.
3. Lower-tier tests cover the underlying behavior where practical, leaving the
   end-to-end test focused on that boundary.
4. No existing end-to-end test already covers the same boundary risk.

Lower-tier coverage of the same user journey does not disqualify the
end-to-end test; duplicate coverage of the same boundary risk does. State that
risk in the test name or nearby documentation.

### Avoid Incidental Complexity

Avoid these patterns unless they are the behavior under test:

- Duplicate scenarios at several layers.
- Global-state reset choreography. Prefer per-test state and cleanup.
- Subprocesses, listeners, sleeps, or real-store setup for a unit-level claim.
- Assertions on private implementation shape when observable behavior is the
  contract.
- Performance claims without a repeatable measurement and stated workload.

Sleeps, listeners, and real stores are appropriate when the test is specifically
about timing, lifecycle, protocol, or persistence. Keep the setup scoped and
make that reason apparent.

## Failures, Skips, and Review

`.test-skip` is a local, temporary exception list. If an unrelated failure is
already listed, report it rather than silently broadening the skip. Before
adding a new skip, record the issue it tracks and remove the skip when the
underlying failure is fixed.

Before opening a PR:

1. For docs-only changes, run the applicable docs, link, freshness, and diff
   checks; do not run the full suite by default.
2. For Go code, keep the focused and affected-package tests green, then run one
   final `make test` (the Bazel test lane), plus the lane of any other tier
   the change touches.
3. Run only the named CI wrapper, specialized target, or risk gate required by
   the changed surface, or the one needed to reproduce a CI result.

For historical CI inventory and maintainer planning context, consult
[CI_TEST_SURFACE_AUDIT.md](CI_TEST_SURFACE_AUDIT.md) and
[CI_CLEANUP_PLAN.md](CI_CLEANUP_PLAN.md). For current commands, use the
workflow files and `Makefile`; these context documents are not a second testing
guide or a live CI inventory.
