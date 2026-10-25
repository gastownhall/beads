# Required Check Topology

Status: aggregate gate jobs are implemented, and the default-branch ruleset
requires both aggregates (see Current State). The aggregate-gate policy below remains maintainer
context, but `.github/workflows/*.yml` and their structural tests are
authoritative for current job membership and display names. Copied workflow
wiring and rollout steps in this note describe the initial rollout, not the
live topology.

## Problem

GitHub branch protection should require one stable PR gate while still letting
CI skip expensive risk checks on low-risk pull requests. The required gate must
not be a path-filtered workflow or a conditionally-triggered workflow, because
GitHub leaves required checks pending when the whole workflow is skipped by
path filters, branch filters, or commit-message skip directives.

GitHub's safe distinction is:

- A skipped workflow can leave a required check pending.
- A skipped job inside a workflow reports success.
- A job that depends on other jobs must use an always-running condition when it
  is the required aggregate, otherwise upstream failures can skip the aggregate.
- Any required Actions check used with merge queue must run on `merge_group`.

References:

- <https://docs.github.com/en/actions/managing-workflow-runs-and-deployments/managing-workflow-runs/skipping-workflow-runs>
- <https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/troubleshooting-required-status-checks>
- <https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-jobs-with-conditions>

## Current State

Current PR-related workflow names:

- `.github/workflows/pr.yml`: `PR`
  Runs on `pull_request`, `merge_group` and push to `main`. Its `scope` job
  (`.github/scripts/ci-scope.sh`) decides which job sets each event runs: a
  pull request everything, a merge group the fast set, a push to `main` the
  post-merge complement (see [Merge Queue](#merge-queue)). Contains the
  baseline PR jobs, Linux build artifact stage, policy/lint compatibility
  jobs, package gates that consume the Linux artifact, focused storage
  domain/uow coverage, the Bazel lane (the `bazel` job calls `bazel.yml`),
  and the baseline aggregate gate `PR / CI Gate / Required`.
- `.github/workflows/bazel.yml`: `Bazel`
  Runs on `push` to `main`, manual dispatch, and `workflow_call` only. PRs and
  merge groups run it once, through `pr.yml`'s `bazel` job (a merge group
  without the Dolt-tier lanes: its scope passes `dolt-tiers: off`, see
  [Merge Queue](#merge-queue)); its own push run on `main` is every lane.
  Its `rbe` job
  decides the execution mode once (`remote`; for fork and Dependabot PRs,
  which get no secrets or variables, rbe-west's certificate mint decides
  (`rbe-mint.ops.gascity.com:8444/v1/status`, infra README "rbe-fork"):
  `fork-ro` or `fork-rw` while it is open for the run, every lane executing
  remotely on `rbe-fork.ops.gascity.com:8444` with its own short-lived
  certificate (instance `oss-fork`, the fork pool; or `oss`, the OSS pool,
  for allowlisted authors), else `local`; `cache` only for `rbe=cache`
  dispatches: local execution on the Blacksmith runner plus rbe-west's
  anonymous read-only action cache (rbe-cache), `.bazelrc`'s `fork-cache`
  config; `local` for `rbe=off` dispatches, fork and Dependabot PRs while
  rbe-fork is closed or refuses the run, unauthorized
  `pull_request_target` fork runs, same-repo runs without the executor
  secret, and every run in a fork repository's own CI
  (`github.repository_owner` is not `gastownhall`: a push to the fork or a
  PR within it; fork PRs into beads run with `github.repository`
  `gastownhall/beads`), so no run reads rbe-cache from a GitHub-hosted
  runner, which rbe-cache refuses (infra `nativelink-cas/west` README
  "rbe-cache", GitHub-hosted runners; every lane's `runs-on` is Blacksmith
  in modes `remote` and `cache`); or `skip` while the `RBE_WEST_WORKERS`
  repo variable is unset) and exports it as the `rbe-mode` / `rbe-enabled`
  outputs (`rbe-enabled` is `true` in `remote`, `fork-ro` and `fork-rw`);
  every lane exports its
  `job.status` as an output named after the job (in mode `remote`, the
  `bazel-doltserver` and `bazel-proxied` outputs carry the `bazel-dolt-race`
  union job's status instead, see [dolt-race Union Lane](#dolt-race-union-lane);
  the `bazel-server-storage` and `bazel-cmd-dolt` outputs carry the
  `bazel-dolt-int` union job's status instead, see
  [dolt-int Union Lane](#dolt-int-union-lane)).
  `pr.yml`'s gate requires the
  call's result (`BAZEL`) and `BAZEL_TEST`, `BAZEL_PURE`,
  `BAZEL_RELEASE_CROSS`, `BAZEL_EMBEDDED`, `BAZEL_INTEGRATION`,
  `BAZEL_DOLTSERVER`, `BAZEL_PROXIED` and `BAZEL_SERVER_STORAGE` (on a
  merge group `bazel-gate.sh` excuses the Dolt-tier ids, `BAZEL_EMBEDDED`,
  `BAZEL_DOLTSERVER`, `BAZEL_PROXIED`, `BAZEL_SERVER_STORAGE` and
  `BAZEL_CMD_DOLT`, exactly when the scope turned the tiers off and the call
  reports its `dolt-tiers` output `off`). These lanes are the only CI run of the Linux Go
  test tiers: the legacy jobs they mirrored in `pr.yml` and `pr-risk.yml`
  are retired (ga-96smfk.22; see
  [Legacy Tier Retirement](#legacy-tier-retirement-d2)), so the gate also
  requires `BAZEL_REMOTE`: the call's mode must be `remote`, `fork-ro` or
  `fork-rw`. `.github/scripts/bazel-gate.sh` reads the exported mode, never
  the variable or the fork flag: mode `skip` allows every Bazel id to skip,
  mode `cache` (`rbe=cache` dispatches) the remote-only `BAZEL_EMBEDDED`,
  `BAZEL_PROXIED` and `BAZEL_SERVER_STORAGE`, mode `local` (`rbe=off`
  dispatches, fork and Dependabot PRs while rbe-fork is closed, a fork
  repository's own CI) those and `BAZEL_INTEGRATION`, and modes `remote`, `fork-ro` and `fork-rw` none;
  on `pr.yml` the first three are red anyway through `BAZEL_REMOTE`.
  A lane that should run and fails, is cancelled, or reports no result fails
  the gate, and so does a missing or invalid mode.
  `bazel-integration` (`bazel test //... --config=integration`, the Bazel
  successor of `main.yml`'s former Linux integration shards) is required on every PR:
  `pr.yml` passes `integration: "on"` (policy-pinned), so it runs in modes
  `remote` (same-repo PRs) and `fork-ro`/`fork-rw` (fork and Dependabot PRs
  while rbe-fork is open), and in mode `cache` (`rbe=cache` dispatches,
  locally on the Blacksmith runner with the read-only cache), and a
  failure, cancellation or missing result turns the gate red. Its legacy
  twins still run only on push to `main`, so this lane is the only PR-time
  run of the full integration-tagged suite. In mode `cache` every action
  trusted CI already ran is a cache hit, so the runner builds and tests only
  what changed. Simulated on a 4-CPU runner (2026-10-02): about 2.5
  minutes warm, 3 to 5 for a `cmd/bd` change, and about 30 with the cache
  closed. Its step timeout (75 minutes) covers that cold local run with
  room to spare, and so also a remote cold compile plus the 1200 s
  per-action cap (both policy-tested). Mode `local` (`rbe=off`; fork and
  Dependabot PRs while rbe-fork is closed, red anyway through
  `BAZEL_REMOTE`) skips it.
  `scripts/ci_workflow_test.go` fails when a new `bazel.yml` job has neither
  a gate id nor an advisory entry.
  Runbook: if `main`'s Bazel BUILD files drift (every PR's Bazel lanes go
  red, and autofix only patches packages the PR itself changed), the RBE
  farm is down, or the beads CI RBE client certificate expires (about
  2027-09-27; a partial secret set also fails every same-repo run), fix
  `main` (`make bazel-sync`) or renew the secrets. There is no legacy tier
  to fall back to: unsetting the `RBE_WEST_WORKERS` repo variable (mode
  `skip`) turns `CI Gate / Required` red on every PR and merge group
  (`BAZEL_REMOTE`), by design. If rbe-west closes the read-only cache (the
  farm's kill switch; the client has none), runs in mode `cache`
  (`rbe=cache` dispatches) execute locally. If it
  is slow rather than closed, each lookup gives up after `fork-cache`'s
  15 s `--remote_timeout` (times its retries), and Bazel's failure circuit
  breaker (`--experimental_circuit_breaker_strategy=failure`) stops calling
  it once too many lookups fail (by default 10% within 60 s); the remaining
  actions run locally. Lookups that time out before the breaker trips still
  cost up to that timeout each, so a slow endpoint slows fork runs until it
  trips. The timeout also bounds each ByteStream read: a large blob the
  farm serves slowly (a cold read from its slow store) times out, and
  Bazel resumes it from the offset reached, but every timeout counts toward
  the breaker. Enough of them open it, the outputs still to be downloaded
  are then "Lost inputs", and Bazel restarts the build once
  (`Found transient remote cache error, retrying the build`). The restart
  hits the cache again and stays green, at the cost of several minutes
  (about 11 in one simulation).
  The anonymous endpoint refuses `FindMissingBlobs`, like every write. A
  cache-only client never calls it: Bazel uses it only to upload, either
  inputs before remote execution or results, and mode `cache` has no
  executor and `--noremote_upload_local_results`. A local probe of
  `--config=fork-cache` must therefore not inherit a user rc that sets
  `--remote_executor`. If it does, every cache miss fails with
  `PERMISSION_DENIED ... method not allowed`.
  Every mode's generated rc also hardens repository fetching, which always
  runs on the runner. `GOPROXY` falls back on any error (`|`), first to
  `proxy.golang.org` again and then to the module's origin; gazelle's
  `go_repository` fetches modules with the go command, which never retries.
  `--http_timeout_scaling=2.0` doubles the timeouts of Bazel's own
  downloader. Both are key neutral.
- `.github/workflows/bazel-farm.yml`: `Bazel Farm (trusted forks)`
  Runs on `pull_request_target` for fork PRs to `main` whose author and
  triggering user are on `.github/bazel-farm-allowlist.txt`, and calls
  `bazel.yml` with the RBE secrets and the PR's pinned head SHA. Advisory:
  no ruleset requires it and nothing reads its results; see
  [Trusted-Author Fork PRs](#trusted-author-fork-prs-bazel-farm).
- `.github/workflows/pr-risk.yml`: `PR Risk`
  Runs on `pull_request`, `merge_group` and push to `main`. Contains only the
  same `scope` job as `pr.yml`, the Nix packaging check (`test-nix`: `nix
  run .#default -- --help` and `nix flake check`; pull requests and pushes
  to `main`, a merge group skips it) and the risk aggregate gate `PR Risk /
  PR Risk Gate / Required`. Its
  embedded, proxied and server-Dolt test tiers, the `detect-ci-tier` risk
  detection and the `bazel-coverage` decision were retired (ga-96smfk.22):
  `pr.yml`'s Bazel lanes run those tests.
- `.github/workflows/main.yml`: `Main`
  Runs on pushes to `main`. Contains only what rbe-west cannot run: the macOS
  `go test -race -short` suite and the Windows smoke build on Blacksmith, and
  the push-to-main savers of the GOCACHEs pr.yml's Windows and macOS jobs
  restore. Every Linux check it used to repeat (migration hygiene, build
  tags, versions, doc flags, gofmt, the PR policy wrapper, the package gates
  and the Linux cache seeders) is gated before the merge by the merge
  queue's `pr.yml` run of the same commit, the Nix flake check runs in
  `pr-risk.yml`'s push run, and `bazel.yml`'s push run re-runs the Bazel
  targets (ga-96smfk.22).
- `.github/workflows/nightly.yml`: `Nightly`
  Runs at 02:00 UTC. Calls `bazel.yml` with `fresh-test-results: true` (every
  test re-executed, no cached results) and checks the rbe-west worker-env
  pin against gascity. It runs no `go test` on a runner: its former Full Test
  Suite, PR Core `go test -json` equivalence side and `ci-measurements.yml`
  dispatch suites are retired; the non-race embedded batch-apply step is
  `//internal/storage/embeddeddolt:embeddeddolt_batch_apply_nonrace_test` in
  the embedded tier.
- The differential regression suite (`tests/regression`, formerly
  `regression.yml`) is `//tests/regression:regression_test`, tagged
  `dolt-server-cmd`, so `bazel.yml`'s `bazel-cmd-dolt` lane (in mode
  `remote` the `bazel-dolt-int` union) runs it on every PR and push to
  `main` (a merge group skips the Dolt tiers; results cached until its
  inputs change), against the catalog-pinned `@bd_releases//:v0.49.6`
  baseline and a hermetic dolt sql-server.
- The upgrade smoke tests (`scripts/upgrade-smoke-test.sh`, formerly
  `cross-version-smoke.yml`) are `//tests/upgrade_smoke:upgrade_smoke_<release>_test`,
  one per release in the newest 30 the release catalog pins, which
  `bazel.yml`'s `bazel-test` lane runs on every PR, merge group and push to
  `main` (cached until their inputs change).
- `.github/workflows/nix-build.yml`: `nix build`
  Uses workflow-level `paths` filters on `pull_request` and `push`. This
  workflow must not be directly required.
- `.github/workflows/update-vendor-hash.yml`:
  `Update vendorHash for dependabot Go bumps`
  Runs on `pull_request_target` for Dependabot Go bumps. It mutates Dependabot
  branches and must not be a required PR check.
- `.github/workflows/worker-env-autosync.yml`: `rbe worker-env autosync`
  Runs on gascity's `repository_dispatch` (`gascity-worker-env-repinned`),
  every 15 minutes and on dispatch. Opens or updates the rbe worker-env
  pin-sync PR from `bot/worker-env-sync`; see
  [rbe worker-env Pin Sync](#rbe-worker-env-pin-sync).
- `.github/workflows/worker-env-sync-automerge.yml`:
  `rbe worker-env sync automerge`
  Runs on `pull_request_target`, on the completion of the pin-sync PR's
  own `PR` and `PR Risk` runs (`workflow_run`) and every 15 minutes, and
  does anything only while the repository variable
  `WORKER_ENV_SYNC_AUTOMERGE` is `true`: verifies the pin-sync PR and
  queues it once its required checks pass. No ruleset requires it.

As of 2026-05-26, the live `gastownhall/beads` ruleset named
`Protect main - light (beads and gastown)` enforces deletion and non-fast-forward
protection on the default branch. It does not require status checks.

Since 2026-09-29 a second, beads-only ruleset, `beads main: required CI
gates`, is active on the default branch. It requires the GitHub Actions
checks `CI Gate / Required` (`pr.yml`, which includes the Bazel lanes through
the `bazel` call) and `PR Risk Gate / Required` (`pr-risk.yml`). PR Risk's
gate job was renamed from `CI Gate / Required` to `PR Risk Gate / Required`
(#6939), so the two required contexts are distinct; before that both
workflows reported the same check name. Organization admins and one team
may bypass it. The ruleset has no `merge_queue` rule yet, so the checks
run on `pull_request` only, and the ruleset does not require branches to
be up to date (strict off). Both workflows are merge-queue ready (see
[Merge Queue](#merge-queue)): adding the queue rule is the only remaining
step. The ruleset applies to the default branch only: PRs into
`release/**` run both workflows (and the Bazel lane), but neither gate is
enforced there.

## Trusted-Author Fork PRs (Bazel Farm)

Fork PRs get no Actions secrets, so `pr.yml`'s Bazel call runs them
through rbe-fork (modes `fork-ro`/`fork-rw`) or, while it is closed or
refuses the run, in mode `local` (local execution on the GitHub-hosted
runner, slower; the remote-only embedded, proxied-server and
server-storage tiers and integration skip; never rbe-cache, which refuses
GitHub-hosted runners). `bazel-farm.yml` gives the
remote-execution farm to fork PRs from an allowlist of trusted authors
(`.github/bazel-farm-allowlist.txt`: the numeric user ids of the same four
people as gascity's `.github/blacksmith-allowlist.txt`). Everyone else's
fork PRs are unchanged.

### Design

- Trigger: `pull_request_target`, types `opened` and `synchronize`, base
  branch `main`. There is no `reopened` or `ready_for_review`: those
  re-test a head that no listed user necessarily pushed (see "Sender vs.
  author" below). Drafts run on `opened` and `synchronize` anyway.
  `pull_request_target` runs the
  workflow file from the base branch, and `uses: ./.github/workflows/bazel.yml`
  resolves from that same commit, so a PR cannot change either for its own
  run.
- `authorize` job: no secrets and no PR code. It sparse-checks-out only the
  allowlist and `.github/scripts/bazel-farm-authorize.sh` at `github.sha`
  (the base commit the workflow came from), without persisted credentials.
  Event facts reach the script through `env` only. It allows the run only
  when all of these hold:
  - the event is `pull_request_target` with action `opened` or
    `synchronize`;
  - the base repository is this one and the head repository is another (a
    fork);
  - the head repository's owner is the PR author (numeric ids), so the PR
    comes from the author's own fork;
  - the base ref is the default branch;
  - the head SHA is a full 40-hex id;
  - both `pull_request.user.id` (the author) and `sender.id` (who triggered
    the event: the opener, or the pusher on `synchronize`) are on the list.
    The list holds numeric user ids, with logins only as comments. A
    renamed account's old login can be registered by anyone, but an id
    never changes hands. Look one up with `gh api users/<login> --jq .id`.
- `farm` job: calls `bazel.yml` only when `authorize` allowed it, with
  `contents: read`, exactly the four RBE secrets, `checkout-sha` =
  `github.event.pull_request.head.sha`, `fork-farm: authorized`, and
  `integration: "on"`. `bazel.yml` checks out that SHA in every lane with
  `persist-credentials: false`. actions/checkout v7 refuses to check out a
  fork PR's head on `pull_request_target` unless `allow-unsafe-pr-checkout`
  is true. Each lane sets that input to
  `inputs.fork-farm == 'authorized' && github.event_name == 'pull_request_target'`,
  so only the authorized farm call opts in (policy-tested: never a literal
  `true`, and nowhere else). Its `rbe` job runs a fork remotely only when
  `inputs.fork-farm == 'authorized'`, the event is `pull_request_target`, and
  `checkout-sha` is set. An authorized farm run is `remote` or `skip` (farm
  switch off or secret missing), never `local`, because `pr.yml` already
  runs the local lanes.
- Concurrency: one farm run per PR (`bazel-farm-<number>`), and a newer
  event cancels the running one. This includes an event from an unlisted
  sender, which then runs nothing. The run it cancels tested an older head
  anyway.
- Gate: advisory. `pr.yml` is unchanged. Every fork PR, listed or not, still
  runs the local Bazel lanes inside the enforced `CI Gate / Required`, and
  the farm run adds a faster signal that also covers the remote-only
  embedded, proxied-server and server-storage tiers.
- Policy tests: `scripts/bazel_farm_workflow_test.go` pins the trigger,
  both jobs' conditions, the allowlist source, the pinned checkout, the
  permissions, where secrets appear, and that no expression is interpolated
  into a script. It also pins concurrency, the allowlist contents, the
  authorize script's decisions, and that no privileged workflow consumes a
  farm run. `TestBazelLaneIsGatedAlongsideLegacy` allows
  `pull_request_target` on a `bazel.yml` caller for this file only.

### Gate Options Considered

1. Advisory farm run, with `pr.yml` unchanged (chosen). Nothing the farm run
   produces can make a PR mergeable, so a flaw in it cannot weaken the
   enforced gate. Cost: listed authors' fork PRs run Bazel twice (local in
   `pr.yml`, remote here), and the merge still waits on the slower local
   lanes.
2. Authoritative farm run: `pr.yml` skips local Bazel for listed forks, and
   the ruleset requires a farm check. Rejected for now:
   - For fork PRs, `pull_request` runs the fork's own copy of `pr.yml`, so
     any "listed fork, skip local Bazel" decision there is PR-controlled.
   - `CI Gate / Required` cannot read another workflow's jobs.
   - Rulesets cannot require a check conditionally, so the farm check would
     have to exist on every PR. A skipped job reports success, so it would
     pass for everyone who is not listed, and a fork that edited `pr.yml`
     to skip local Bazel would then merge with no Bazel signal.

   Doing this safely needs a trusted reporter: a `pull_request_target` job
   that runs no PR code and writes one required check run on the head SHA
   after reading both workflows' results. It is the same reporter
   [Commit-Message Skip Directives](#commit-message-skip-directives)
   describes.
3. Standalone copy of the lanes instead of calling `bazel.yml`. Rejected:
   the copy would drift, and calling `bazel.yml` keeps every lane test
   applicable.

The farm run checks out the pinned head SHA, not `refs/pull/N/merge`. The
merge ref is mutable, so checking it out would leave a gap between
authorizing the run and checking out the code (TOCTOU). Cost: the farm run
tests the head, not the head merged into `main`. `pr.yml`'s local run still
tests the merge commit.

### Threat Model

Assets: the RBE client certificate and key (farm access and farm cache
writes), the repository's Actions cache (read by `push` runs on `main`), the
`GITHUB_TOKEN`, and the integrity of the required gates.

What a listed author's fork PR can do: what a same-repo PR can do. Its code
(`setup-bazel`, `.bazelrc`, repository rules, tests) runs with the RBE
certificate, so it can use or copy the certificate and write to the farm's
cache. Maintainers accepted this risk for same-repo PRs on 2026-09-28 (see
`bazel.yml`'s header). Listing a user extends that trust to the account,
and an account compromise has the same effect as a compromised same-repo
contributor.

Caches, the one place a `pull_request_target` run differs from a same-repo
PR. It runs in the default branch's cache scope, which `push` runs on
`main` restore from. Same-repo PRs use `refs/pull/N/merge`.

- GitHub's Actions cache: read-only. Since 2026-06-26 GitHub gives
  untrusted triggers, `pull_request_target` included, a read-only cache
  token for the default branch's scope, and the cache service enforces it.
  `cache-mode` (2026-09-10) makes `read` the default for these events and
  carries through reusable workflows. A declared `cache-mode: write` or
  `write-only` would override that default, so a policy test allows only
  `read` or `none` in `bazel-farm.yml`, `bazel.yml` and `setup-bazel`. The
  farm's safety depends on that default and on nothing declaring otherwise.
- Blacksmith's colocated cache: unknown. Farm lanes run on
  `blacksmith-2vcpu-ubuntu-2404`, whose cache transparently backs
  `actions/cache`, scoped by branch like GitHub's. Nothing documents whether
  it honours the read-only token. A canary farm run that tries to save
  a cache entry from a fork-authorized PR, or an answer from Blacksmith,
  settles it. Record the answer here.
- What a writable cache would reach, and what now stops it. The `bazel.yml`
  lanes on `main` and on same-repo PRs, which fall back to `main`'s scope,
  restore three caches:
  - The Bazel runner cache (`bazel-repo-v4-*`, restore-keys prefix). It
    holds only the content-addressable `--repository_cache`, whose hits
    Bazel re-hashes. The repo contents cache (extracted repos, never
    re-verified) is off (`--repo_contents_cache=`).
  - Bazelisk and the Bazel binary (`bazel-bin-v1-*`, exact key on
    `setup-bazel`'s action and `.bazelversion`, written only by
    `bazel-test` on a push to `main`). `setup-bazel` checks both files
    against their sha256 pins before installing them, replaces a file with
    other content by a download, and runs Bazel from a fresh Bazelisk home
    with `BAZELISK_SKIP_WRAPPER`. Like every `actions/cache` entry, the
    archive is extracted wherever its paths point, so the pin check covers
    the two binaries; the writer is what keeps the rest of the entry
    trustworthy.
  - The Go module cache (`beads-go-mod-v2-*`). `bazel-test` runs
    `go mod verify` right after restoring it.

  Outside the farm's path, other workflows restore caches that a writable
  default-branch scope would poison:
  - `release.yml`'s `setup-go` default cache (GOMODCACHE and GOCACHE, with a
    key predictable from `go.sum`, in a job that signs and attests
    binaries);
  - the `beads-go-build-v2-*` GOCACHE entries restored by `main.yml` and
    `pr.yml`;
  - the executables in `smoke-binaries-*` and `historical-dolt-*` (both
    retired with their workflows).

  GitHub's read-only default covers all of them. Hardening `release.yml`
  with `cache: false` is tracked separately.

What anyone else can do: nothing new.

- An unlisted author's fork PR triggers a run whose `authorize` job reads
  the base allowlist and says no. `farm` is skipped, and no secret or PR
  code is involved.
- Fork `pull_request` runs still get no secrets, and `bazel.yml` keeps them
  local (mode `local`) even if the fork's `pr.yml` passes `fork-farm`,
  because the event is not `pull_request_target`.

Attacks considered:

- Allowlist spoofing through PR edits. The allowlist and the decision
  script come from `github.sha` (base) in a sparse checkout that never
  contains PR files. A PR that edits them changes nothing until a
  maintainer merges it.
- Workflow-file edits in the PR. `pull_request_target` runs the base
  branch's `bazel-farm.yml`, and `./.github/workflows/bazel.yml` resolves
  from the same base commit. `setup-bazel` and everything after the
  checkout are PR code. That is the accepted listed-author trust, the same
  as for same-repo PRs.
- Sender vs. author. Both must be listed.
  - A collaborator on a listed author's fork who pushes gets
    `sender = collaborator`, and nothing runs.
  - Someone who opens a PR from a listed author's fork branch gets
    `author = opener`, and nothing runs.
  - A listed author who opens a PR from someone else's fork (compare
    across forks) gets nothing: the head repository's owner must be the
    author. Otherwise the fork's owner could push between the author's
    review and "Create".
  - A collaborator on a listed author's fork who pushes while the PR is
    closed or a draft (skipped: `sender = collaborator`) cannot get that
    head run by the author's click on "Reopen" or "Ready for review",
    because those actions are not triggers.
  - What remains is the listed author's responsibility: a PR opened, or a
    push made, by the author on top of commits a fork collaborator pushed.
    Listed authors should not add collaborators to their fork.
  - A listed author who pushes to an unlisted author's PR also gets nothing
    (the author check).
  - An upstream maintainer who pushes to the PR, or clicks "Update branch",
    is not listed either, so nothing runs. The next push by the author runs
    it.
  - Commit author/committer metadata is not checked: anyone can forge it.
  - A listed author who pushes commits written by others vouches for them,
    as a same-repo contributor does.
- Force-push races (TOCTOU). The lanes check out
  `github.event.pull_request.head.sha`, the commit the authorized event
  describes, never a branch or merge ref. A later push is a new
  `synchronize` event whose sender is judged again, and it cancels the
  older run. A SHA that is not 40 lowercase hex characters is refused.
- Script and expression injection. No `${{ }}` appears in any `run:` of
  `bazel-farm.yml` or `bazel.yml` (policy-tested). Titles, branch names and
  logins reach scripts only through `env`. The authorize script prints a
  login only after checking it against GitHub's login character set.
- Label and comment injection. There is no `labeled`, `issue_comment` or
  `edited` trigger, and no label or comment is read.
- Token and permissions. The workflow and every job have `contents: read`
  only. Checkouts do not persist the token. `secrets: inherit` is never
  used: exactly the four RBE secrets reach `bazel.yml`, and there only the
  `setup-bazel` step's env reads them (plus the `rbe` job's emptiness test).
- Artifact poisoning.
  - `bazel.yml` does not upload `bazel-sync-patch` on `pull_request_target`.
  - `bazel-autofix.yml` and `docs-autofix.yml`, the only `workflow_run`
    consumers, watch only `PR` and act only on `pull_request` runs.
  - Nothing downloads artifacts by run id.
  - The farm run's other artifacts (test logs, `bazel-farm-build-artifacts`)
    are for humans only.

  All of this is policy-tested.
- Farm cache poisoning. The farm's action cache is shared the same way
  same-repo PRs share it, and client uploads of local results are off
  (`--noremote_upload_local_results`). A beads-only RBE instance and
  farm-side denial of client action-cache writes are still open.

### Re-runs, Adding and Removing a User

A re-run of a farm run (whole run or failed jobs) replays the original
event. It keeps the original `GITHUB_SHA` and payload, so:

- It checks out the same pinned head SHA. A re-run never picks up a newer
  push; only a new `opened` or `synchronize` event does.
- It runs the original base commit's `bazel-farm.yml`, `bazel.yml`,
  allowlist and authorize script. A user removed from the allowlist on
  `main` still passes `authorize` when one of their old runs is re-run,
  and that run gets the RBE secrets again.

Only users with write access can re-run, so this is a small window, but
removal has to close it.

To add a user: add `<id> # <login>` to the allowlist (id from
`gh api users/<login> --jq .id`) and update `bazelFarmUsers` in
`scripts/bazel_farm_workflow_test.go` in the same PR. It takes effect when
the PR merges.

To remove a user:

1. Merge a PR that deletes their line from the allowlist and from
   `bazelFarmUsers`.
2. Cancel their in-progress farm runs, and delete their earlier farm runs
   so that nobody can re-run them:

   ```bash
   # Runs whose head repository (the author's own fork) belongs to <id>.
   gh api --paginate repos/gastownhall/beads/actions/workflows/bazel-farm.yml/runs \
     --jq '.workflow_runs[] | select(.head_repository.owner.id == <id>) | "\(.id) \(.status)"'
   gh run cancel <run-id>   # in-progress runs
   gh run delete <run-id>   # completed runs
   ```
3. If the removal is for cause (a compromised account, or suspected misuse
   of the certificate), assume the RBE client certificate was copied:
   - rotate `RBE_TLS_CERT` / `RBE_TLS_KEY`;
   - have the farm revoke the old certificate;
   - consider purging the farm's action cache for the beads instance.

   Rotating also closes the re-run window without step 2.

Only a GitHub run can verify these:

- The farm run's check runs appear on the PR.
- The Blacksmith runner group accepts `pull_request_target` jobs for fork
  PRs.
- `actions/checkout` fetches a fork's head SHA from the base repository, and
  the `allow-unsafe-pr-checkout` opt-in gets past v7's fork-checkout guard
  (the lanes fail with "Refusing to check out fork pull request code" if
  it doesn't).
- An unlisted author, or a push by an unlisted collaborator, skips `farm`.

## rbe-west Pre-warm

`bazel.yml`'s `rbe-prewarm` job is a best-effort attempt to have
gastownhall/gascity's rbe-west OSS worker pool (one Blacksmith-hosted
NativeLink worker, driven by gascity's own `rbe-worker-pool.yml`
`workflow_dispatch`) already booting by the time this run's Bazel lanes need
it, instead of each of them separately waiting out the ~120s it takes
NativeLink to notice demand and scale the pool up from zero. gascity dispatches
its own pool from inside its own `bazel-test.yml` job, same-repo, with
`github.token`; beads cannot use its own `GITHUB_TOKEN` against gascity's
repository, so this job needs its own credential into gascity instead.

Merge queue caveat: a `merge_group` run executes the queued commit's
workflow files, so once a maintainer queues a fork PR, that PR's own edits
to `bazel.yml` or `pr.yml` (this job included) run with the shared
`bazel-allocator` key. The B1 protections below (inlined dispatch logic,
base-branch workflow, approval-gated workflow edits) hold only for runs of
the committed file. Review fork PRs' `.github/` changes before queueing
(see [Merge Queue](#merge-queue)).

**What it does.** A job of its own (not a step inside `rbe`, so it never
delays the mode decision every lane waits on), gated on
`needs.rbe.outputs.mode == 'remote'` only - the one mode whose lanes target
rbe-west's `oss` instance, which is what `rbe-worker-pool.yml` serves.
(Earlier this also included `fork-rw`, but a fork or Dependabot
`pull_request` run never carries a `workflow_call` secret regardless of
runs-on or this job's `if:` - GitHub does not forward repository or App
secrets to a fork's `pull_request` event - so that half of the condition
always found no credential and no-opped; it was dropped as dead weight that
only cost an idle runner-minute. See "Accepted risk" below for why
`bazel-farm.yml`'s trusted-fork tier does not get pre-warming either.) When
scheduled, it mints a short-lived GitHub App installation token and uses it to
check gastownhall/gascity's current `rbe-worker-pool.yml` run count and, if
under the desired worker count, dispatch enough new runs to reach it. The
desired count is the repository variable `RBE_PREWARM_WORKERS`: unset, empty
or non-numeric means the default of 1, values above 4 are clamped to 4, and
0 is the kill switch (below). The in-workflow default is deliberately 1;
operators who want more warm workers raise the repository variable (it is
currently set to 2) rather than the workflow default. Every
failure path - no credential, gascity unreachable, `gh` rate-limited, the
dispatch itself rejected - prints a `::warning::` and the step still exits 0:
this job never gates anything (it is not a `needs` of any lane, and it is
never in `.github/scripts/bazel-gate.sh`'s vocabulary, so pr.yml's `ci-gate`
never looks at it), and it cannot fail the run either, since a called reusable
workflow's overall conclusion is "failure" if any job inside it fails
regardless of `needs` - the job itself therefore carries bazel.yml's one
deliberate `continue-on-error: true`.

*Unverified assumption:* the design above relies on a job-level
`continue-on-error: true` inside a called reusable workflow preventing that
job's own failure from flipping the *calling* workflow's own
`needs.bazel.result` (pr.yml's `ci-gate` reads that, not `bazel.yml`'s
internal conclusion, for its actual gating decision) to `failure`. This
follows from GitHub's documented `continue-on-error` semantics and was
reasoned through, not exercised against a real Actions run with a forced
`rbe-prewarm` failure (a `workflow_dispatch` run with a deliberate `exit 1`
patched into the dispatch step, observing `needs.bazel.result` in pr.yml's
`ci-gate` job, would confirm it); treat it as a documented assumption until
someone runs that check.

The dispatch logic is inlined directly into the job's own `run:` step, not
checked out from a `.github/scripts/*.sh` file. This job has no `actions/checkout`
step at all: `pull_request_target` always loads the calling workflow's own
YAML from the trusted base branch, but a step that checked out a PR's head
and then ran a script from that tree while a credential was live would let
that PR's content exfiltrate it - and, unlike editing this workflow file,
editing a script file does not trip the org's workflow-file approval policy,
so even a same-repo branch push could have altered it unreviewed. Nothing
executes after the mint step except this one inline dispatch step and an
`always()` result-recording step, neither of which touches a repository path
(policy-tested: `scripts/bazel_rbe_prewarm_test.go`).

**Credential scope.** A GitHub App named "bazel-allocator" is installed on
gastownhall/gascity alone, granted exactly two permissions: Actions
(read/write) and Metadata (read). Its private key (`RBE_POOL_APP_PRIVATE_KEY`)
*is* a long-lived credential, and the mint step's `with:` does read it
directly - that step, and only that step, holds it in the runner's memory, to
produce a short-lived installation token (`actions/create-github-app-token`,
pinned to a released commit SHA) scoped with `owner: gastownhall` and
`repositories: gascity`. The App is identified by its public Client ID
(`client-id: Iv23ligrqVEhlamZnoPU`, from `GET /apps/bazel-allocator`),
committed as a literal in the mint step rather than kept as a secret or
repository variable: it is not a credential, and a literal keeps it reviewed
and policy-tested with the rest of the step. It replaces the action's
deprecated `app-id` input. Every later step sees at most that token, never the
key itself, and the token can act on gascity and nothing else in the
gastownhall org. The token is used as `GH_TOKEN` for the `gh run list` /
`gh workflow run` calls against gastownhall/gascity's `rbe-worker-pool.yml` on
`main`, and nowhere else (`steps.mint.outputs.token` appears exactly once in
`bazel.yml`, policy-tested). pr.yml and nightly.yml pass the app private key
straight through like the four RBE secrets; `bazel-farm.yml` does not (see
"Accepted risk" below). A fork or Dependabot `pull_request` run never gets it
regardless (GitHub never forwards repository or App secrets to a
fork's `pull_request` event).

**Accepted risk.** The bazel-allocator private key is shared with gascity's
own rbe-west scaler (its `bazel-test.yml` job dispatches its own pool
same-repo, with `github.token`; beads instead got this dedicated App so it
could reach gascity's repository at all) - a deliberate, user-approved reuse,
not a credential minted solely for beads. Consequently, every same-repo
`pull_request`, `merge_group`, `push` (to `main`) and nightly.yml run of
`bazel.yml` - the only contexts that ever receive `RBE_POOL_APP_PRIVATE_KEY` -
reaches that shared key for the duration of the mint step. This is a
materially larger blast radius than losing beads' own revocable RBE client
certificate (see the "Accepted risk" discussion of that certificate in
`bazel.yml`'s own header): with the key, an attacker can mint
gastownhall/gascity Actions-write tokens indefinitely - dispatching
`rbe-worker-pool.yml` repeatedly to burn donated Blacksmith compute,
cancelling or re-running gascity's own workflow runs, deleting their logs or
caches, and reading their artifacts - until the key is rotated on gascity's
side. Because the key is shared, **rotating it to respond to a beads-side
leak also breaks gascity's own scaler**; the kill switch below (not
rotation) is the first response to a suspected leak, and this run stops
holding the credential within the same job. `bazel-farm.yml`'s
`pull_request_target` run - the one caller whose run executes a fork's own
code - never receives this secret, so an allowlisted fork author can
read beads' own RBE client certificate (an existing, accepted, revocable-for-
beads-alone risk) but never this shared key; that path's Bazel lanes get a
cold start instead of a pre-warmed pool.

**Secret names.**

- `RBE_POOL_APP_PRIVATE_KEY` - the bazel-allocator App's private key (PEM),
  the only secret this job reads. (The former `RBE_POOL_APP_ID` secret is no
  longer referenced by any workflow now that the mint step uses the literal
  Client ID; the repository secret can be deleted.)

**Kill switch.** Pre-warming disables itself cleanly, without touching the
job's `if:` or rotating the key, in either of two ways:

- Leave `RBE_POOL_APP_PRIVATE_KEY` unset (or clear it): the job's own
  `HAS_POOL_APP` check (the same env-boolean pattern as the `rbe` job's
  `HAS_EXECUTOR`) skips the mint step, the dispatch step finds no
  `GH_TOKEN`, warns, and exits 0. This is also the job's natural state before
  the App is provisioned at all, and the fastest response to a suspected
  leak of the token or key (it stops every future run from minting a new
  token; it does not invalidate one already minted, which expires on its
  own shortly after the job finishes).
- Set the repository variable `RBE_PREWARM_WORKERS` to `0`: the dispatch
  step's own kill switch, checked before any network call. Deleting the
  variable does not disable pre-warming; it restores the default of 1.

## rbe worker-env Pin Sync

rbe-west's OSS pool workers run gastownhall/gascity's toolchain and advertise
the sha256 of the manifest gascity's `tools/rbe/worker-env` measures, and the
schedulers run an action only on a worker that advertises the action's exact
`worker-env` pin (`//platforms:rbe_worker`). beads commits a byte-for-byte
copy of gascity main's manifest (`tools/rbe/worker-env.txt`) and its pin
(`platforms/BUILD.bazel`). When a gascity PR that re-pins (a Go, dolt or
library bump) merges, new workers advertise the new pin at once, and every
beads PR's `Bazel / execution mode` preflight (`tools/rbe/worker-env-sync`)
fails until beads' copy lands, unless the
[grace window](#rbe-worker-env-grace-window) covers it. On Oct 21,
gascity#8165 merged at 01:09:31Z and the copy, #7746, landed at 02:38:04Z:
89 minutes of red beads PRs.

The sync, end to end:

1. gascity sends `repository_dispatch` `gascity-worker-env-repinned`
   (`client_payload`: `sha`, `pin`) within about a minute of a push to its
   main that moves the pin.
2. `worker-env-autosync.yml` runs on that event, and every 15 minutes as a
   backstop. It resolves gascity main to a commit with `git ls-remote`
   (`tools/rbe/worker-env-sync-ref`): raw.githubusercontent.com serves a
   branch URL with `cache-control: max-age=300`, so for up to 5 minutes
   after a re-pin the branch URL can still return the old manifest. The
   payload's `sha` is used only while it is still main's head. On a
   mismatch it copies the manifest and pin from that commit, pushes
   `bot/worker-env-sync` (a run whose copy is already on the branch pushes
   nothing, #7533) and opens the PR.
3. With `WORKER_ENV_SYNC_AUTOMERGE` set to `true`, the PR opens with no
   review label, and `worker-env-sync-automerge.yml` verifies it
   (`tools/rbe/worker-env-sync-verify`: the author, branch and base, every
   commit of `main...<head>`, the branch's newest push, exactly the two
   files, the pin, and both files against gascity main, all read at one
   head commit). It queues the PR only once `CI Gate / Required` and
   `PR Risk Gate / Required` have both passed on that head, so GitHub queues
   it at once, from that head, and nothing stays armed. A pending
   auto-merge, by contrast, survives any writer's push: the PR's
   `--match-head-commit` binds only the moment of arming, so a PR armed
   before its checks pass could land a later, unverified head. The
   `workflow_run` trigger on the sync PR's own `PR` and `PR Risk` runs
   queues it the moment the second gate passes.
4. The guard's act job fails closed. After every verification (one that
   failed, or whose outputs were lost, included) it looks the open sync PR
   up itself. It queues the PR, or keeps it queued, only on the verdict
   "arm" for exactly the head it reads; an explicit "unknown" for that
   same head also holds a queue entry for the next check, but only when
   GitHub or gascity did not answer a read (briefly unreachable), never
   when the activity API does not show who pushed the head. It never keeps
   an auto-merge, whatever the verdict: it disables a pending one (armed,
   not queued), and a later run on a green head arms afresh; on a PR it
   keeps queued it disables the auto-merge and keeps the queue entry, since
   an auto-merge left on could queue the PR again after a push or an
   ejection. Otherwise it disables any auto-merge and takes the PR out of
   the merge queue, and the job fails if the PR is still armed or queued
   after that. A refused PR gets one comment per head commit with the
   reasons. Without the variable, the PR is labeled
   `status/needs-review-auto` and lands through the automated review, as
   before.
5. Every 15 minutes the guard re-checks an open sync PR (a missed event,
   or a merge-queue ejection: it re-queues a PR twice per head at most).
   On a `PR` or `PR Risk` run's completion, or that re-check, it also
   re-runs, once per head commit, the failed jobs of a first attempt that
   failed, was cancelled or timed out. A re-pin moves every action key, so
   that first run is uncached: #7746's release cross-compile took 50
   minutes, and the first attempts of #7411 and #7732 hit its 90-minute
   timeout.

The verifier reads the PR's files and file names, and prints them (a diff,
a refusal), but nothing it prints reaches the runner as a workflow command:
its stdout and stderr are defanged (`::` and the legacy `##[` broken up)
before they leave it, and its own annotations escape `%`, which the runner
decodes in a command's message. Unfiltered, a sync branch carrying a line
`##[add-mask]true` would have the runner mask `true`, and the runner then
drops every job output that holds a masked value. The act job's
fail-closed reading is the second line of defense.

Tokens: the autosync pushes with the gastownhall-autofix App (contents
write) and opens the PR with the gascity-autofix App (pull-requests write),
so neither of its tokens can do the other's job. The guard queues the PR
and takes it out with a gascity-autofix token minted with contents,
pull-requests and merge-queues write: `enablePullRequestAutoMerge` needs
contents and pull-requests write on one token, and a PR queued with the
`GITHUB_TOKEN` never starts its `merge_group` runs. It reads the PR with the
job's own read-only token, and comments and labels with a
pull-requests-only token from the same App. Both Apps' keys are secrets of
the `autofix` environment, whose deployment branch is `main` (a
`pull_request_target` or `workflow_run` run's ref is the default branch).

Turning it on:

1. Organization settings, Developer settings, GitHub Apps, gascity-autofix,
   Permissions & events, Repository permissions: Contents "Read and write"
   and Merge queues "Read and write" (Pull requests is "Read and write"
   already); save, then accept the new permissions on the gastownhall
   installation (Organization settings, GitHub Apps, gascity-autofix).
2. `gh variable set WORKER_ENV_SYNC_AUTOMERGE --repo gastownhall/beads --body true`

If the variable is set before the App has those permissions, the guard
cannot mint its merge token and labels the PR `status/needs-review-auto`
once instead (only if it carries no review-pipeline label), so the PR still
lands through the review.

Dry run, from a checkout on Linux with `gh` logged in:
`tools/rbe/worker-env-sync-verify <pr>` prints every check and what the
guard would do, and never writes. With no PR it finds the open sync PR.

Rollback: `gh variable delete WORKER_ENV_SYNC_AUTOMERGE --repo
gastownhall/beads`. The guard then does nothing and the autosync labels
the next PR for review. A sync PR that is already queued stays queued:
"Remove from queue" in the UI takes it out.

Starting over: delete the `bot/worker-env-sync` branch. The autosync
recreates it from main, and opens a fresh PR while beads is out of step.
Closing the PR alone is not enough: the autosync pushes nothing to a branch
that already carries the right two files, and opens a new PR from that same
branch.

`pull_request_target`: GitHub's workflow execution protections add a
default rule that disables `pull_request_target` on public repositories
without an applicable Actions event policy, enforced from 2026-12-02
(`bazel-farm.yml` depends on the trigger too). Queueing does not depend on
it: the guard queues on `workflow_run`. If the rule ends up blocking it
here, a push to the sync branch still drops a queued PR from the queue on
GitHub's side, and nothing the guard queues stays armed for the new head to
inherit; the refusal and its comment come with the next `workflow_run` (the
pushed head's own CI finishing) or the 15-minute run instead of at once.

### rbe worker-env Grace Window

A re-pin gascity can roll back (a Go release: the manifest's go line alone)
need not turn beads PRs red while the sync PR is on its way. gascity
publishes a record, `worker-env-grace.txt` alone on its
`rbe/worker-env-grace` branch (gascity `tools/rbe/worker-env-grace`), with
the pin before the re-pin and a window: beads may keep that previous pin
for at most two hours, and rbe-west keeps workers of it an hour longer, for
the runs beads accepted last. While the repository variable
`RBE_WORKER_ENV_GRACE` is `true`, the preflight in mode `remote` runs
`tools/rbe/worker-env-sync` with `WORKER_ENV_SYNC_GRACE=1`: a copy that
still pins the record's previous pin, inside its window, exits 3, and the
job proceeds with a notice naming the window's end, so the lanes run on the
previous-pin workers until the sync PR lands. gascity publishes the record
about a minute after the re-pin lands on its main, and until then the branch
holds an earlier re-pin's record, so a record for another pin (or one that
could not be read) is read again every 15 seconds for up to 150 seconds
from the script's start (`WORKER_ENV_SYNC_GRACE_WAIT`, inside the job's 5
minutes) before the preflight decides. Anything else fails the preflight as
before, with one more line saying why the window does not apply: no record;
a malformed one (anything but at most 64 KiB of printable ASCII in the exact
`key value` form, or a window over two hours); an unreadable one after the
wait; another pin after the wait; `previous none`; a previous pin other than
beads'; a gascity manifest that moved more than the go line, a Go other than
beads', or one whose rolled-back manifest does not hash to the previous pin;
or a window not yet open or closed. Mode `remote` only: `fork-ro` executes
on the separate `oss-fork` pool, which has no previous-pin workers, and fork
runs see no variables. The nightly check, the autosync and the guard's
verifier never ask for the window (`TestWorkerEnvSyncGraceStaysInThePreflight`),
so the sync PR still opens and lands as above.

Turning it on, once gascity publishes the record and rbe-west serves the
previous pin (gastownhall/gascity#8271 merged, gascity/infra#2452 deployed):
`gh variable set RBE_WORKER_ENV_GRACE --repo gastownhall/beads --body true`.
Rollback: `gh variable delete RBE_WORKER_ENV_GRACE --repo
gastownhall/beads`; the next run's preflight is strict again. Delete it too
whenever rbe-west stops serving previous pins (its grace switch off or
rolled back): beads would otherwise accept a pin no worker serves, and its
lanes would queue instead of failing fast.

## dolt-race Union Lane

In mode `remote`, `bazel.yml`'s `bazel-dolt-race` job (check name
`Bazel / dolt-race`) runs the dolt-server and proxied-server tiers in one
`bazel test //... --config=dolt-race`, in place of the `bazel-doltserver`
(`dolt-server lane`) and `bazel-proxied` (`proxied-server tier`) jobs.
`--config=dolt-race` is exactly the union of `--config=doltserver` and
`--config=doltserver-proxied`, with one tag filter for both tiers, so every
member target's test action is unchanged (`bazel aquery` at the cutover: the
27 + 35 = 62 `TestRunner` actions were byte-identical under the member
configs and the union; there are 27 + 37 = 64 since
`//cmd/bd:bd_managed_local_test` runs as 3 shards, and all must fit the
union's `--jobs=64` at once, `scripts/bazel_dolt_race_test.go`, which leaves
no headroom: another test action in either tier needs a `--jobs` or
shard-count change first). One client
waits on rbe-west for both tiers at once, so the job ends with the slower
tier and the other tier's runner is saved.

- Mode `remote` only (same-repo pull requests, push, nightly, dispatch and
  `bazel-farm.yml`'s authorized call; a merge group skips it with the other
  Dolt tiers, see [Merge Queue](#merge-queue)). In the fork modes
  (`fork-ro`, `fork-rw`), `cache` and `local`, `bazel-doltserver` and
  `bazel-proxied` run as before: there a single client does both tiers' work
  without in-flight deduplication, so a union would cost the sum of the
  tiers rather than the max, on the run's critical path.
- Exactly one arm runs in any mode. The union's `job.status` is both the
  `bazel-doltserver` and the `bazel-proxied` `workflow_call` output (each
  reads the union's result first, then its member's), so `pr.yml`'s gate
  ids (`BAZEL_DOLTSERVER`, `BAZEL_PROXIED`), `ci-gate.sh` and
  `bazel-gate.sh` are unchanged: a failed or cancelled union reds both ids,
  a union that reported nothing reads as `skipped`, which mode `remote`
  never allows, and the per-mode skips are the members' as before.
- `check_testcases.py` and `check_shard_coverage.py` run once over the
  union's BEP (the proxied tier's `--suite`; the dolt-server tier has no
  shard-coverage check). The `bazel test` step has a 20-minute timeout,
  every step has one, and the job's (55 minutes) is at least their sum plus
  5 and below the merge queue's 60-minute check timeout, which keeps a
  rollback of the fast set (a merge group running it again) safe.
- Its ci-analytics `LANE` is `dolt-race`.
- Policy: `scripts/bazel_dolt_race_test.go`. Rollback is a revert of the
  cutover PR: the gate ids never changed and the action keys are equal both
  ways, so cached results carry back.

## dolt-int Union Lane

In mode `remote`, `bazel.yml`'s `bazel-dolt-int` job (check name
`Bazel / dolt-int`) runs the server-storage and cmd-dolt tiers in one
`bazel test //... --config=dolt-int`, in place of the `bazel-server-storage`
(`server-storage lane`) and `bazel-cmd-dolt` (`cmd-dolt tier`) jobs.
`--config=dolt-int` is exactly the union of `--config=doltserver-integration`
and `--config=doltserver-cmd`, with one tag filter for both tiers. The final
argv and environment Bazel assembles for all 63 `TestRunner` actions (17
server-storage + 46 cmd-dolt) are unchanged under the union, so the remote
action digest -- what RBE's cache keys on -- is unaffected and cached results
should carry over (confirmed in practice by the first merge group's
`tests_cached == tests_total` in ci-analytics, not claimed here as already
observed). This is *not* a claim that the union is local-actionKey-identical
to the members: moving cmd-dolt's `-test.timeout` arg and
`BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION` env from `--config=doltserver-cmd`
onto the two cmd-dolt targets' own `args`/`env` (so the union's tag filter
alone decides which targets see them, and server-storage's Go timeout does
not change) moves them out of the build configuration's own fingerprint,
which every target under that configuration picks up, into each target's own
attributes, which only it carries -- changing the local actionKey for
cmd-dolt's 46 actions. One client waits on rbe-west for both tiers at once,
so the job ends with the slower tier and the other tier's runner is saved.

Unlike dolt-race, this union's members do not share a gating category:
`bazel-server-storage` is plain-gated (`BAZEL_SERVER_STORAGE`), while
`bazel-cmd-dolt` is flag-gated and advisory (`BAZEL_CMD_DOLT`,
`BAZEL_CMD_DOLT_REQUIRED`). The union job therefore carries *no* job-level
`continue-on-error`: on push, nightly and dispatch nothing reads
`BAZEL_SERVER_STORAGE` off a `workflow_call` output, so a job-level
`continue-on-error` (which would make `job.status` read success regardless
of the `bazel test` step's own result) would let a server-storage failure
end such a run green, where it must stay red exactly as it does on main
today. Instead the `bazel test` step and the two per-tier "Every target and shard
ran tests" steps (`check-ss`, `check-cd`, below) each carry their own
`continue-on-error`, and an always() "Classify tier results" step
(`tools/bazel/classify_tier_results.py`) reads the BEP by target label,
folds each check step's outcome in via `--check NAME=OUTCOME`, computes each
tier's own result, and fails the job itself iff server-storage's tier failed
(a cmd-dolt-only test or zero-testcase failure leaves that step green, as
`bazel-cmd-dolt`'s own `continue-on-error` already does outside mode
`remote`); `bazel`'s exit code being neither 0 nor 3 (not attributable to one
tier) or a non-`PASSED` result for a label outside both `--tier` lists (the
label/tag lists having drifted from the BUILD files, rather than trust a
silent pass) both mark every tier failed and fail the job regardless. The
classify script's own flags are hardened to match: `--bep`/`--rc`/
`--gate-tier`/`--github-output` may each be given only once, and a repeated
`--tier`/`--check` name is rejected, so an edit that accidentally widens or
un-gates a tier fails the script rather than silently changing what gates
the job (`scripts/bazel_dolt_int_test.go`'s exact pin of the classify step's
`run:` argv closes the same gap one level up). The job exposes two outputs,
`server_storage_result` and `cmd_dolt_result`, one per tier, instead of the
uniform `result` every other lane's always() step records.

A cmd-dolt failure fails the job only where no caller gate reads
`cmd_dolt_result`. In `pr.yml`'s call (pull requests, merge groups) the gate
reads it as `BAZEL_CMD_DOLT`, so a pull request shows a cmd/bd tier failure
as before: `BAZEL_CMD_DOLT` red, the call's aggregate `BAZEL` green. On a
push to `main`, nightly, a dispatch and `bazel-farm.yml`'s advisory call
nothing reads it, so the job's last step ("The cmd/bd Dolt-server tier fails
the run outside pr.yml's call") fails the job when `cmd_dolt_result` is
`failure`. A merge group skips the Dolt tiers, so `main`'s push run is the
tier's first run on the merged tree, and its red is what
`main-ci-alert.yml` reports (see [Merge Queue](#merge-queue)). That holds
whatever `pr.yml`'s `BAZEL_CMD_DOLT_REQUIRED` says: the flag decides only
what a pull request's gate requires.

- Mode `remote` only (same-repo pull requests, push, nightly, dispatch and
  `bazel-farm.yml`'s authorized call; a merge group skips it with the other
  Dolt tiers), and only when `integration`
  is not `"off"` (matching the members). In the fork modes (`fork-ro`,
  `fork-rw`), `cache` and `local`, `bazel-server-storage` and `bazel-cmd-dolt`
  run as before: there a single client does both tiers' work without
  in-flight deduplication, so a union would cost the sum of the tiers rather
  than the max, on the run's critical path.
- Exactly one arm runs in any mode. `bazel-server-storage`'s and
  `bazel-cmd-dolt`'s `workflow_call` outputs each read the union's matching
  per-tier output first, then their own member's, so `pr.yml`'s gate ids
  (`BAZEL_SERVER_STORAGE`, `BAZEL_CMD_DOLT`), `ci-gate.sh` and
  `bazel-gate.sh` are unchanged: a job that dies before the classify step
  leaves both outputs empty, and `pr.yml` falls back an empty
  `needs.bazel.outputs.bazel-server-storage`/`bazel-cmd-dolt` to the literal
  `"skipped"` (`pr.yml`'s `BAZEL_SERVER_STORAGE`/`BAZEL_CMD_DOLT` env lines),
  so `ci-gate.sh` takes its "was skipped" branch, not "is unset" (that branch
  is only an env var that is empty after the `||` fallback, which these never
  are) -- `report_failure "$var was skipped"`, the same failure as an empty
  `bazel-server-storage`/`bazel-cmd-dolt` result today -- and the per-mode
  skips are the members' as before.
- `check_testcases.py` runs twice over the union's BEP, once per tier
  (`check-ss`, scoped with `--label` to server-storage's two targets; `check-cd`,
  scoped to cmd-dolt's two), each with `continue-on-error: true` so a
  zero-testcase shard in one tier reaches only that tier's classify result
  (must-fix 2 of PR #7535's review), not the other's and not the job
  directly. `--label`'s sets are policy-tied to the BUILD files' own
  `dolt-server-integration`/`dolt-server-cmd` tag sets
  (`TestBazelDoltIntTierLabelsMatchTheTags`), the same sets
  `TestBazelDoltIntFitsTheJobsBudget` derives, so the two cannot drift apart.
  `check_shard_coverage.py` still runs once over the union's BEP. The job
  also runs a "Shard balance" step scoped with
  `--label //internal/storage/dolt:dolt_server_full_test`, distinct from
  `bazel-server-storage`'s own unscoped shard-balance step, since the union's
  BEP otherwise mixes both tiers' shards under one report. The `bazel test`
  step has a 25-minute timeout, every step has one (the two check steps
  1 minute each, preserving the pre-split step's combined 2-minute budget),
  and the job's (59 minutes) is at least their sum plus 5 and below the merge
  queue's 60-minute check timeout -- leaving only about 1 minute for runner
  pickup and the rbe job inside that 60-minute budget, so a hang that runs to
  this job's own timeout can drop the merge group (were a merge group to run
  it again: today it skips the Dolt tiers); accepted, because such a
  run is red anyway and the 59-minute job timeout bounds only this job, not
  the whole run. "Upload test logs" runs `if: failure() || steps.test.outcome
  == 'failure'`, not `failure()` alone, since the `bazel test` step's own
  `continue-on-error` means an ordinary test failure there does not make
  `failure()` true until the classify step (which runs after it) exits
  non-zero.
- Its ci-analytics `LANE` is `dolt-int`.
- Policy: `scripts/bazel_dolt_int_test.go`, and
  `tools/bazel/classify_tier_results_test.py` for the classify script itself.
  Rollback is a revert of the cutover PR: the gate ids never changed and the
  remote action digests are equal both ways, so cached results carry back.

## Remote Repo Contents Cache

A cold lane client re-runs its repository rules (gazelle's `go_deps`, the Go
SDK, the LLVM slice) before its first action. Bazel 9's remote repo contents
cache serves those extracted trees from rbe-west's `oss` action cache instead
(ported from gascity's `bazel.yml`; design: gascity
`engdocs/design/bazel-remote-repo-contents-cache.md`). The repository variable
`RBE_REPO_CONTENTS_CACHE` rolls it out, in mode `remote` only:

| value | effect |
|---|---|
| unset / `off` | nothing (default) |
| `seed` | each push to main runs `bazel.yml`'s `rrc-seed` job: every lane's command in `.github/scripts/rrc-lane-commands.txt` with `--nobuild`, uploading repository trees with a 30-minute `rbe-rrc-writer-beads` certificate that rbe-west's mint signs only for that job (GitHub OIDC; `.github/scripts/rrc-writer-credential.sh`). rbe-west's `rrc-gate` admits only repo-contents entries from it |
| `canary` | `seed`, and the `test` lane reads the cache |
| `on` | `seed`, and every lane but the release cross-compile and the package gates reads it |

Readers append `startup --experimental_remote_repo_contents_cache` and
`common --loading_phase_threads=64` to `.bazelrc.local` before setting up
Bazel; both are key neutral, and the lanes keep
`--noremote_upload_local_results`. While the variable is not `off`,
nightly.yml's call also runs `rrc-verify`: a cold fetch of every lane,
compared with the cached entries by `tools/bazel/rrc_verify.py`; a mismatch
fails the job and opens an `rrc-verify` issue, which lists the containment
steps below.

Mode `cache` lanes (`rbe=cache` dispatches; no fork or Dependabot PR
reaches mode `cache`, since rbe-cache refuses GitHub-hosted runners) do
not follow the variable (the rbe job's `rrc` output is `off` outside mode
`remote`). The jobs with a reader step above
pass `BAZEL_RRC_READ: 'true'` to setup-bazel, and only those: their configs
are the ones `rrc-lane-commands.txt` seeds (`bazel-release-cross` builds for
other platforms and does not opt in). In mode `cache` they read the same
entries through rbe-cache's anonymous AC and CAS reads: `write-bazelrc.sh`
adds the same two lines to its rc whenever
`.github/actions/setup-bazel/cache-rrc-probe.sh` gets a GetCapabilities
answer from rbe-cache, then NOT_FOUND for a GetActionResult key no action
has and for a ByteStream Read of a blob the CAS does not hold. The reader
lanes that run in mode `cache` run there on Blacksmith's `ubuntu-2404`
image, the family `rrc-seed` writes from; their GitHub-hosted fallback (the
other non-remote modes, which do not read) is a pinned `ubuntu-24.04`,
not `ubuntu-latest`. Each job's step summary records whether it reads, and
rbe-cache's TCP connect time (DNS lookup excluded). rbe-cache refuses every
write, and fork-cache uploads nothing.

An unreachable cache with the startup flag set costs every repository a
failed lookup, so a cache that is closed or down when the probe runs gets
no lines and a notice instead, and the lane fetches as before. That holds
at probe time only. After loading, Bazel fetches a cached repository's
files lazily from rbe-cache (:8443). If the cache goes away mid-run
(rbe-cache-gate closes, the runner's IP is banned, or the breaker opens),
the command fails, and Bazel does not re-fetch the repository (bazel#30218,
gascity design R4).

To stop mode `cache` lanes reading:

1. Commit `fork_rrc_read=off` in both probe scripts: beads'
   `.github/actions/setup-bazel/cache-rrc-probe.sh` and gascity's
   `tools/rbe/cache-rrc-probe.sh`. This is the preferred way: lanes that
   already read finish, and later runs fetch for themselves. beads has no
   fresh-merge, so an open fork PR keeps its old switch until it is pushed
   again.
2. For immediate server-side containment, an operator runs
   `rbe-cache-gate close` on core. That stops all fork-cache, cache hits
   included, and fails the lanes that are reading at that moment.
3. The mode `remote` lanes read the same entries: rbe-cache serves the same
   `oss` action cache. On an `rrc-verify` mismatch, set
   `RBE_REPO_CONTENTS_CACHE` to `seed`. That stops every mode `remote`
   reader, while `rrc-seed` and `rrc-verify` keep running. Set it to `off`
   only to stop seeding too; that also stops `rrc-verify`. If the run only
   says it could not check the cache, leave the variable as it is.

Known limit (gascity design R4): rbe-cache's anonymous CAS is a size
partition whose upper store, for blobs of 512 MiB or more, is a noop. A
cached repository with a file that large is a hit whose download always
fails. rrc-gate does not refuse such entries today.

`rrc-seed` asks for `id-token: write` and `rrc-verify` for `issues: write`, so
every caller of `bazel.yml` (pr.yml, nightly.yml, bazel-farm.yml) grants both:
GitHub checks a called workflow's job permissions when the run starts, even
for jobs that skip. Neither job runs on a pull_request, merge_group or
pull_request_target event, and every other `bazel.yml` job keeps
`contents: read`. Rollback for the mode `remote` readers: set the variable to
`off` (or `seed`, to keep the cache warm without readers). It does not reach
mode `cache`; use the steps above.

## Required Check Contract

After the aggregate checks are verified on the branch, branch protection or the
default-branch ruleset should require stable aggregate GitHub Actions checks
from unfiltered workflows. The original single-check proposal assumed all PR
jobs lived in one workflow; after the workflow split, a single in-workflow
aggregate can only cover jobs in that same workflow. The implemented first
rollout uses one aggregate per required workflow. An external status aggregator
would only be needed if maintainers still want exactly one required check.

- Baseline aggregate candidate: `PR / CI Gate / Required`
- Risk aggregate candidate: `PR Risk / PR Risk Gate / Required`
- Source: GitHub Actions
- Required on: pull requests and merge queue groups targeting `main`; not
  on `release/**`

Do not require these existing check names directly:

- `Scope` (`pr.yml`'s and `pr-risk.yml`'s scope job; the gates require
  its result)
- `Report main's push CI` (`main-ci-alert.yml`, push runs on `main` only)
- `Detect CI tier`
- `Fast checks (frozen migrations, .beads diff)` (the base-diff checks; the
  tree checks it used to run are Bazel tests, see
  [Repository Guards as Bazel Tests](#repository-guards-as-bazel-tests))
- `Check pure-Go and js/wasm boundaries (CGO_ENABLED=0)`
- `Check doc flags freshness`
- `Windows Make shell (native, msys2, cygwin)` (F7a fold of the former
  `host: [native, msys2, cygwin]` three-job matrix)
- `Test (Windows) small packages (doltversion, dbproxy server)` (F7a fold of
  the former `test-windows-doltversion` and `test-windows-dbproxy-server`)
- `Test (ubuntu-latest)`
- `Test (macos-latest)`
- `Go checks (vet)`
- `PR Core (wrapper timing)`
- `Build Artifacts`
- `Bazel tier coverage`
- `Build (Embedded Dolt)`
- `Test (Embedded Dolt Storage 1/5)` through `Test (Embedded Dolt Storage 5/5)`
- `Test (Embedded Dolt Conformance - core)` and `- audit`
- `Test (Embedded Dolt Cmd 1/20)` through `Test (Embedded Dolt Cmd 20/20)`
- `Test (Proxied Dolt Cmd 1/15)` through `Test (Proxied Dolt Cmd 15/15)`
- `Test (Server Dolt Conformance)`
- `Test (Server Dolt Full Suite 1/16)` through `Test (Server Dolt Full Suite 16/16)`
- `Test (Windows - smoke)`
- `Test Nix Flake`
- `Bazel / test` and the other jobs of `bazel.yml`
- `Bazel Farm / *` (`bazel-farm.yml`'s advisory, PR-controlled results)

Those checks should remain visible for diagnosis, but branch protection should
point at aggregate gates after the gate jobs are verified.

## Workflow Topology

### 1. Keep the Required Workflow Always Triggered

`.github/workflows/pr.yml` is the required baseline workflow owner. Its PR and
merge queue triggers must stay unfiltered:

```yaml
on:
  pull_request:
    branches: [ main ]
  merge_group:
    types: [ checks_requested ]
  push:
    branches: [ main ]
```

(`push` carries the post-merge complement; see [Merge Queue](#merge-queue).)

Do not add `paths`, `paths-ignore`, or narrower branch filters to `pr.yml` or
`pr-risk.yml`. Path and risk decisions belong in detector jobs and job-level
`if` conditions.

### 2. Add Aggregate Gate Jobs

The block below is a historical snapshot of the initial baseline gate. It is
intentionally not kept in lockstep with later leaf additions and renames; use
`.github/workflows/pr.yml` and its structural tests for the current wiring.

`.github/workflows/pr.yml` introduced one final baseline gate job:

<!-- markdownlint-disable MD013 -->

```yaml
  ci-gate:
    name: CI Gate / Required
    runs-on: ubuntu-latest
    needs:
      - build-artifacts
      - check-build-tags
      - check-cmd-bd-puregeo-tests
      - check-version-consistency
      - check-no-duplicate-migrations
      - check-doc-flags
      - check-no-beads-changes
      - detect-package-gates
      - package-mcp
      - package-npm
      - package-website
      - pr-policy-wrapper
      - pr-core-wrapper
      - pr-lint-wrapper
      - test-domain-uow
      - fmt-check
      - lint
    if: ${{ always() }}
    steps:
      - uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd # v6

      - name: Evaluate CI gate
        env:
          CI_GATE_NAME: PR baseline gate
          CI_GATE_REQUIRED: >-
            BUILD_ARTIFACTS
            CHECK_BUILD_TAGS
            CHECK_CMD_BD_PUREGEO_TESTS
            CHECK_VERSION_CONSISTENCY
            CHECK_NO_DUPLICATE_MIGRATIONS
            CHECK_DOC_FLAGS
            CHECK_NO_BEADS_CHANGES
            DETECT_PACKAGE_GATES
            PACKAGE_MCP
            PACKAGE_NPM
            PACKAGE_WEBSITE
            PR_POLICY_WRAPPER
            PR_CORE_WRAPPER
            PR_LINT_WRAPPER
            TEST_DOMAIN_UOW
            FMT_CHECK
            LINT
          BUILD_ARTIFACTS: ${{ needs.build-artifacts.result }}
          CHECK_BUILD_TAGS: ${{ needs.check-build-tags.result }}
          CHECK_CMD_BD_PUREGEO_TESTS: ${{ needs.check-cmd-bd-puregeo-tests.result }}
          CHECK_VERSION_CONSISTENCY: ${{ needs.check-version-consistency.result }}
          CHECK_NO_DUPLICATE_MIGRATIONS: ${{ needs.check-no-duplicate-migrations.result }}
          CHECK_DOC_FLAGS: ${{ needs.check-doc-flags.result }}
          CHECK_NO_BEADS_CHANGES: ${{ needs.check-no-beads-changes.result }}
          DETECT_PACKAGE_GATES: ${{ needs.detect-package-gates.result }}
          PACKAGE_MCP: ${{ needs.package-mcp.result }}
          PACKAGE_NPM: ${{ needs.package-npm.result }}
          PACKAGE_WEBSITE: ${{ needs.package-website.result }}
          PR_POLICY_WRAPPER: ${{ needs.pr-policy-wrapper.result }}
          PR_CORE_WRAPPER: ${{ needs.pr-core-wrapper.result }}
          PR_LINT_WRAPPER: ${{ needs.pr-lint-wrapper.result }}
          TEST_DOMAIN_UOW: ${{ needs.test-domain-uow.result }}
          FMT_CHECK: ${{ needs.fmt-check.result }}
          LINT: ${{ needs.lint.result }}
        run: |
          skipped_ok=""
          if [[ "$GITHUB_EVENT_NAME" == "merge_group" ]]; then
            skipped_ok="CHECK_NO_BEADS_CHANGES"
          fi
          export CI_GATE_SKIPPED_OK="$skipped_ok"
          bash .github/scripts/ci-gate.sh
```

<!-- markdownlint-enable MD013 -->

`.github/workflows/pr-risk.yml` has a companion aggregate for `test-nix`.

`.github/scripts/ci-gate.sh` is a small shell evaluator. It fails on any
`failure` or `cancelled` result. It accepts `skipped` only for jobs that are
intentionally absent for that event or risk tier:

- (Historical: `CHECK_NO_BEADS_CHANGES=skipped` was accepted on
  `merge_group` while the step was PR-only. It now runs on `merge_group`
  too, against `merge_group.base_sha`, and no skip of it is excused.)
- In the baseline aggregate, `BAZEL_REMOTE` is red unless the Bazel call
  ran in mode `remote`, `fork-ro` or `fork-rw` (its lanes are the only run
  of the Linux Go test tiers), whatever `bazel-gate.sh` lets skip.
- All baseline jobs must be `success`.

This keeps branch protection pointed at stable aggregate jobs while preserving
the underlying job names and logs.

### 3. Keep Risk Decisions at Job Level

Conditional risk checks should use this pattern:

```yaml
  detect-risk:
    name: Detect risk
    outputs:
      run_risk: ${{ steps.detect.outputs.run_risk }}

  risk-check:
    name: Risk check
    needs: detect-risk
    if: needs.detect-risk.outputs.run_risk == 'true'

  ci-gate:
    name: CI Gate / Required
    needs: [detect-risk, risk-check]
    if: ${{ always() }}
```

The required aggregate should treat `risk-check=skipped` as success only when
`detect-risk.outputs.run_risk != true`. If the detector wanted the risk check
and the risk check is skipped, failed, or cancelled, the aggregate must fail.

Do not use this pattern for required checks:

```yaml
on:
  pull_request:
    paths:
      - 'go.mod'
      - 'go.sum'
```

If that workflow or one of its jobs is made required, PRs that do not touch the
listed paths can be blocked waiting for a check that GitHub never creates.

## Conditional Check Placement

### Embedded Dolt Matrix

Retired (ga-96smfk.22). `pr-risk.yml`'s `detect-ci-tier` used to run the
embedded, proxied and server-Dolt matrices only for risky paths; the Bazel
lanes that replaced them run on every PR and every push to `main` (a merge
group skips these Dolt tiers, see [Merge Queue](#merge-queue)), and their
result cache keeps an unaffected PR's cost to a cache hit.

### Legacy Tier Retirement (D2)

D2 retired legacy test jobs one step at a time where `pr.yml`'s gated
Bazel lanes run the same tests: `pr-risk.yml`'s Dolt tiers (steps 1 and 2)
and `pr.yml`'s own Go test jobs (step 3). Until ga-96smfk.22 each step had a
committed flag (column 4) and a `bazel-coverage` decision job chose, per
PR, between the legacy jobs and the lanes; every PR and merge group took the
lanes (forks too, since `BAZEL_COVERS_FORKS`, ga-96smfk.15), so the legacy
jobs, the flags, the decision and its `BAZEL_*_RETIRED` gate ids were
removed and replaced by one gate id, `BAZEL_REMOTE`. The parity tests
(`TestBazelEmbeddedJobRunsEmbeddedTier`, `TestBazelDoltServerTiers`,
`TestBazelPureJobMirrorsPureGoJob`) now pin the lanes against the retired
jobs' frozen commands.

<!-- markdownlint-disable MD013 -->

| Step | Retired legacy jobs | Bazel lanes (`bazel.yml`) | Former flag | Former `pr.yml` gate id |
| --- | --- | --- | --- | --- |
| 1 | `test-embedded-storage` x5, `test-embedded-conformance` x2, `test-embedded-cmd` x20 | `bazel-embedded` (`--config=embedded`) | `BAZEL_RETIRES_LEGACY_EMBEDDED` | `BAZEL_EMBEDDED_RETIRED` |
| 2 | `test-proxied-cmd` x15, `test-server-storage`, `test-server-storage-full` x16 | `bazel-proxied` (`--config=doltserver-proxied`), `bazel-server-storage` (`--config=doltserver-integration`) | `BAZEL_RETIRES_LEGACY_DOLT_SERVER_TIERS` | `BAZEL_DOLT_SERVER_RETIRED` |
| 3 | `pr.yml`: `pr-core-wrapper` (PR Core), `build-artifacts`, `check-cmd-bd-puregeo-tests` (pure-Go and js/wasm) | `bazel-test` (`--config=ci`, which also publishes `bazel-ci-build-artifacts`), `bazel-pure` (`--config=pure`, `--config=js-wasm`), `bazel-doltserver` (`--config=doltserver`; the only run of the former `test-domain-uow` and `contract-corpus` suites) | `BAZEL_RETIRES_LEGACY_PR_LANES` | `BAZEL_PR_LANES_RETIRED` |

<!-- markdownlint-enable MD013 -->

The Bazel lanes run the same tests with the same shard scripts and
manifests. They are the tiers' only pre-merge run, and `CI Gate /
Required` requires them to have run remotely and passed.

- Step 3 specifics (`pr.yml`'s own jobs):
  - Artifact consumers: no job in any workflow reads `pr.yml`'s bd
    artifact (`docs-autofix.yml` reads `check-doc-flags`'
    `cli-docs-freshness-patch`, which is unaffected).
  - F3: the package gates (`package-mcp`, `package-npm`) moved into
    `bazel.yml` itself, behind the caller input `package-gates` (`pr.yml`
    passes `"on"`; `bazel-farm.yml`/`nightly.yml` keep the default `"off"`).
    They need only the `rbe` job, not the rest of the `bazel` call, and no longer download an artifact: on a
    same-repo PR (`needs.rbe.outputs.enabled == 'true'`) each job builds its
    own bd with `bazel build --@rules_go//go/config:race
    //cmd/bd:bd_for_tests` (the race flag matches `test:ci`'s top-level
    build setting, so the action keys and output path equal `bazel-test`'s
    `bd_for_tests`, race itself still off for the binary); otherwise (forks,
    Dependabot, rbe off/skip) they fall back to `go build ./cmd/bd`, same as
    before F3. The Bazel bd carries no vcs build info (`bd version` prints
    `1.3.0 (dev)`, no commit); no consumer reads it. Verified 2026-10-02:
    both package gates pass with the Bazel-built bd (MCP: 228 passed, 5
    skipped; npm: all tests and the pack dry run), the same as with a
    `go build` bd. In mode remote, package-npm runs on
    `blacksmith-4vcpu-ubuntu-2404` and package-mcp on
    `blacksmith-8vcpu-ubuntu-2404` with `pytest -n 16`
    (`BEADS_MCP_PYTEST_WORKERS`; the script's default is `-n 8`);
    `ubuntu-latest` (and `-n 8`) otherwise. `bazel-test`'s own
    `bazel-ci-build-artifacts` upload is no longer consumed by anything; it
    is kept for the F3.5.3 SHA256SUMS comparison and for debugging.
  - The Dolt-backed domain, uow, tracker, doctor/fix and protocol suites
    run only on the dolt-server lane, against hermetic `dolt sql-server`s
    started from the pinned dolt; no CI job runs them against the
    testcontainers backend (no `requires-docker` targets, no
    `--config=docker`). The Dolt server fingerprint is checked in CI on that
    local backend by `//internal/testutil:testutil_dolt_test`; its container
    half runs under plain `go test` wherever docker and the image are
    present, and the image tag stays pinned to the dolt release by
    `TestPinnedDoltCLIMatchesContainerImage`. The release-target
    cross-compilation (formerly pr.yml's
    `check-release-target-cross-compilation`, `go build ./...` with
    `CGO_ENABLED=0` per target) is bazel.yml's `bazel-release-cross` lane
    (`BAZEL_RELEASE_CROSS`), split out of `bazel-pure` so it runs in
    parallel with it: `scripts/ci/bazel-release-cross-compile.sh` runs one
    remote `bazel build //tools/bazel:release_cross`, every `go_library` and
    `go_binary` for each row of `scripts/ci/release-targets.txt`, plus
    `//tools/bazel:pure_bd_has_no_cgo_only_deps` (a pure bd must not link
    gozstd, which `go build` rejects with cgo off). Same cgo-off boundary as
    the job it replaces.
  - `go test`'s own vet checks (cmd/go's `defaultVetFlags`, policy-tested
    equal to the toolchain's) and the golangci-lint linters `.golangci.yml`
    enables run as nogo (`//tools/nogo`): natively in `bazel test //...
    --config=ci`, and for every release platform in the
    `bazel-release-cross` lane (engdocs/LINTING.md). Since F5 S1 each
    configuration in `scripts/nogo_lint_policy_test.go`'s
    `nogoConfigurations` table (race, pure, js/wasm) validates in exactly
    one required lane, not beside every compile of every lane: the race
    configuration's nogo is owned by `test` above, and `embedded`,
    `doltserver`, `doltserver-proxied`, `dolt-race` and the
    package gates pass `--norun_validations`, since they compile a strict
    subset of what `test` already validates on that configuration
    (`scripts/nogo_lint_policy_test.go`, engdocs/LINTING.md). Since F5 S2
    (Variant A) the integration configuration (`--config=integration`'s
    `gms_pure_go,integration` tags) is no longer a three-lane exception:
    `bazel-integration` (`--config=integration`), `bazel-server-storage`
    (`--config=doltserver-integration`), `bazel-cmd-dolt`
    (`--config=doltserver-cmd`) and `bazel-dolt-int` (`--config=dolt-int`,
    the union of the latter two) all pass `--norun_validations`. Instead
    `bazel-integration` runs a narrow extra build step, `Nogo: integration
    library files`, that validates only `//internal/testutil/integration`
    (the one non-test library package the `integration` build tag selects)
    under `--config=integration --@rules_go//go/config:race
    --output_groups=nogo_fix`. This accepts a coverage loss: the 56
    integration-tagged `_test.go` files listed in
    `tools/nogo/unvalidated_integration_tests.txt` are no longer analyzed by
    nogo in any lane (`scripts/nogo_lint_policy_test.go`'s `integ` row of
    `nogoConfigurations` and `TestNogoIntegrationStepCoversItsConfiguration`
    pin this; engdocs/LINTING.md). The former
    `scripts-go-checks` (`Go checks (vet)`) and `pr-lint-wrapper`
    (`PR Lint (native|windows|darwin)`) jobs are retired.
  - The repository policy tests (`./scripts/...`, including the D2 guards)
    and the tests that walk the checkout run only under Bazel, remotely:
    the `//scripts` go_tests and `//test/docsync:docsync_test`, each over
    the part of the checkout it reads. `tools/bazel/go_srcs.py` keeps a
    `repo_files` block in every package that partitions its files into
    `repo_go_srcs`, `repo_go_test_srcs`, `repo_doc_files` and
    `repo_other_files`, aggregated at the root; their union `//:repo_files`
    is every tracked file outside `.bazelignore`, which only
    `//scripts:tracked_files_test` and two repository guards read (the
    BUILD sync step's `make bazel-sync-check` fails on a package without
    the block). So a Go-only change re-runs no workflow policy test, and a
    docs-only or workflow-only change no Go-source scan. The release
    formula under `.bazelignore`d `.beads/` comes in as `@beads_formulas`.
    Their former `go test` legs (`Go checks (scripts-test)` and
    `Go checks (allowlisted)`) are gone.

    `tools/bazel/equivalence_allowlist.txt` holds only the two `cmd/bd`
    tests of plain `go test`'s own bd build fallback, which Bazel never
    takes; `pr-preflight-platforms` runs them on macOS and Windows
    ("Exercise generated Git hook timeout process boundary and go test's bd
    build fallback"). `TestBazelOnlySkipsAreAllowlisted` (under
    Bazel too) requires every top-level test with a `TEST_SRCDIR`- or
    `bazeltest.IsBazel()`-guarded `t.Skip` to have an allowlist `skip`
    entry, and no test anywhere to run part of its checks under `go test`
    only.
  - Every artifact `bazel.yml` uploads sets `overwrite: true`, so
    "Re-run failed jobs" of a lane (the recovery for an eviction or a flake)
    does not fail on the upload with a 409 (policy-tested).
  - PR Core's other work: `scripts/ci/pr-core.sh` is the one `go test` (plus
    a timing summary); its `BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION=1` is
    `test:prcore`'s too. `bazel-test`'s equivalence step compares Bazel's
    tests with `go list`, not with PR Core's run, so it is unaffected. The
    skip-parity check (`equivalence.py --go-test-json`) has no CI caller
    since nightly's go-test side was retired (ga-96smfk.22); run it by hand
    with `BEADS_PR_CORE_GO_TEST_JSON` when auditing a skip.
  - `--config=sole-run` (`--experimental_remote_cache_eviction_retries=0`,
    the step 1 and 2 hardening) is added to every `bazel test` of the three
    lanes wherever they execute remotely (`BAZEL_SOLE_RUN`: modes `remote`,
    `fork-ro` and `fork-rw`), which every PR and merge group runs in. It used to
    carry `--nocache_test_results` too; test results are now cached on every
    run but nightly's (see [Merge Queue](#merge-queue), "Test result
    caching"). Measured 2026-10-02: `bazel test //... --config=ci
    --nocache_test_results` remotely took 132 s (113 targets).
  - Pinned for step 3 (`scripts/pr_lanes_bazel_coverage_test.go`): the
    three lanes' Bazel steps and the `prcore`, `ci`, `doltserver`, `pure`,
    `js-wasm` and `sole-run` rc lines exactly; no other rc line of a config
    they use may filter, narrow or re-run tests; no BUILD rule other than
    the pinned retired-tier targets, and no `.bzl`, may carry `-test.short`,
    `-test.run`, `-test.skip`, `BEADS_TEST_SKIP`, `--test_filter` or a
    non-`False` `flaky`. (Pinning every target's args, as steps 1 and 2 do
    for their few targets, is not practical for the whole tree.) The flaky
    target query in the step 1 and 2 lanes covers `tests(//...)` and runs on
    every covered PR.
- Gate: on every `pull_request` and `merge_group` run (same-repo, fork and
  Dependabot alike; merge groups run on this repository's
  `gh-readonly-queue/*` branch with the CI secrets) `pr.yml`'s gate requires
  `BAZEL_REMOTE` (the call's mode is `remote`, `fork-ro` or `fork-rw`) and
  every lane's own id (a merge group every lane's but the Dolt tiers', which
  main's push run runs: see [Merge Queue](#merge-queue)). Mode `skip` (the farm switch off) and mode `local`
  (the executor secret missing, or rbe-fork closed or refusing a fork run)
  are red, never a silent pass with fewer tests.
  `scripts/pr_risk_bazel_coverage_test.go`
  (`TestPRGateRequiresRemoteBazelLanes`) runs `bazel.yml`'s actual `rbe`
  step and `pr.yml`'s actual gate step over every event, variable value,
  secret, fork, actor and mint answer.

  On push to `main`, `bazel.yml`'s push run is the only run of these tiers
  (`main.yml`'s legacy embedded, proxied, integration, domain+uow and Linux
  unit jobs were removed, ga-96smfk.14), and for the Dolt tiers the first
  run on the merged tree.
- Enforcement scope: the beads-only ruleset requires both gates on the
  default branch (`main`) only. On PRs into `release/**` both workflows run
  and report, but merging does not wait for them, before or after this
  change. Without the merge queue rule and with `strict` off, no
  pre-merge run catches a gap; only `bazel.yml`'s push run does, after
  merge.
- Lane hardening that the retirement relies on, for `bazel-embedded`,
  `bazel-proxied` and `bazel-server-storage` alike (policy-tested in
  `scripts/ci_workflow_test.go` and `scripts/pr_risk_bazel_coverage_test.go`):
  - Test result caching: originally `--nocache_test_results` in
    `test:embedded`, `test:doltserver-proxied` and
    `test:doltserver-integration` (every run executed every test, like the
    legacy `-test.count=1` jobs). Since merge queue readiness these lanes
    cache results like every other lane, and nightly's `--config=fresh`
    re-executes every test once a day (see [Merge Queue](#merge-queue),
    "Test result caching"). Only `test:fresh` sets result caching in
    `.bazelrc`.
  - No retries: no `--flaky_test_attempts` or
    `--runs_per_test_detects_flakes` anywhere, and no `flaky =` other than
    a literal `False`. Each lane also runs
    `bazel query 'attr(flaky, 1, tests(//...))'` before its tier and fails
    on any result, so a macro or variable cannot hide a flaky target.
  - No whole-invocation retry: `--experimental_remote_cache_eviction_retries=0`
    in all three lane configs (no other `.bazelrc` line may set it; it is
    not in `remote-exec`, which may only hold flags that are key-neutral
    between trusted and fork runs, policy-tested).
    By default (5) Bazel re-runs the entire `bazel test` invocation when an
    input was evicted from the remote cache ("Lost inputs ... Found
    transient remote cache error, retrying the build"), and that exit code
    outranks a test failure. With `--keep_going` (and a failed test, which
    is never served from the cache), a test that failed in the first attempt is
    then executed again and only the retry's result (and BEP) is reported,
    so a flaky failure could turn green. With 0, an eviction fails the step
    instead; "Re-run failed jobs" retries it visibly. Reproduced against a
    local HTTP cache emptied mid-build (2026-10-02): default retries
    rebuilt and exited 0, `=0` failed with "Unexpected lost inputs". The
    real proxied run before this change hit an eviction on all 15 shards
    (before any test ran) and was retried silently.
  - Pinned selection:
    - Each lane's `bazel test //... --config=<config>` step and its
      config's `.bazelrc` lines are pinned exactly.
    - No other rc line of a config the lanes use (unconfigured,
      `remote-exec`, `fork-cache`, or anything they reference) may filter,
      narrow, re-run or re-route tests.
    - `.bazelrc` imports only the two gitignored try-imports, and no other
      rc file is committed.
    - `tools/bazel/*.sh` and the whole setup-bazel action may not inject
      test selection, skips or the tiers' switches
      (`BEADS_TEST_PROXIED_SERVER`, `BEADS_TEST_ENV_RUN_DOLT`, ...).
    - The `args` and `env` of every target tagged `embedded`,
      `dolt-server-proxied` or `dolt-server-integration` are pinned.
  - `tools/bazel/check_shard_coverage.py` runs after each tier. It requires:
    - every Bazel shard of `//cmd/bd:bd_embedded_test` and
      `//cmd/bd:bd_embedded_part2_test` (50 each: shards 1-50 and 51-100 of
      the manifest's 100-shard block, which their ranges must tile exactly
      once; the manifest's frozen 20-shard block, once the retired
      `test-embedded-cmd` jobs' split, has been deleted, F1),
      `//internal/storage/embeddeddolt:embeddeddolt_embedded_test` (40;
      the frozen 5-shard block was `test-embedded-storage`'s, F1),
      `//cmd/bd:bd_proxied_test` (34; the manifest's frozen 15-shard block,
      once `test-proxied-cmd`'s split, has been deleted, F2) and
      `//internal/storage/dolt:dolt_server_full_test` (16) to have run
      exactly the tests its shard script lists (list-only mode, minus
      `TestMain`, which `grep '^func Test'` lists but which is never a
      test; a policy test runs all four real scripts and requires every
      other listed name to be a `func Name(t *testing.T)`);
    - no shard, and no unsharded target (the embedded conformance
      partitions, `//internal/storage/dolt:dolt_server_conformance_test`),
      to be all skipped.

    A test the scripts discover from source but the Bazel binary lacks
    fails the lane instead of passing silently, and so does an injected
    `-test.short` or `BEADS_TEST_SKIP` that skips a whole shard.
  - Verified on a real remote run before step 2 (2026-10-01): proxied 164
    top-level tests over 15 shards, 0 skipped; server storage 1246 + 1
    conformance, 10 skipped (none all-skipped); both checkers pass.
- `conformance.yml` is retired. Its Tier 1 (the embedded-Dolt
  `TestConformance`) duplicated `test-embedded-conformance` and the
  embedded lane's `embeddeddolt_conformance_{core,audit}_test`; its Tier 2
  (the real-binary CLI corpus, `go test -tags 'gms_pure_go e2e'
  ./test/conformance`) is `//test/conformance:conformance_test` against the
  injected non-race `bd_for_tests`. Its files build under `e2e ||
  integration`, so it is `integration-only` and `--config=integration` runs
  it in `bazel-integration`, which `pr.yml`'s gate requires (PR Core's `go
  test` builds neither tag). Neither tier was part of a required gate
  before. `scripts/conformance.sh` stays as the local `go test` entrypoint.
  Its Tier 3, the served HTTP corpus (`./internal/httpclient` with
  `BEADS_TEST_EMBEDDED_DOLT=1` and `BEADS_HTTP_TEST_REQUIRED=1`), runs in
  the embedded lane as `//internal/httpclient:httpclient_served_test`
  (race build, 16 shards).
  `docs-mintlify.yml` likewise drops its docsync job (`go test
  ./test/docsync`, which `bazel-test` runs as `//test/docsync:docsync_test`)
  and keeps only Mintlify's network-bound broken-link check.

### F7a: Same-Repo Blacksmith Moves and Job Folds

F7a moves pr.yml/pr-risk.yml
jobs that are same-repo-safe and restore no GitHub-saved build cache onto the
org's Blacksmith runner pool, and folds several independent single-purpose
jobs into fewer jobs with multiple isolated steps, to cut same-repo PR queue
time. Jobs that need a Blacksmith-side build-cache saver (scripts-go-checks,
pr-lint-wrapper, the preflight/doc-freshness matrices' ubuntu legs) are F7b's
scope, not this slice's.

- **Runner moves** (same-repo-Blacksmith expression, §
  [Trusted-Author Fork PRs](#trusted-author-fork-prs-bazel-farm)'s pattern,
  reused verbatim with only the vCPU label varying; pinned by
  `TestSameRepoBlacksmithRunners` and `TestSameRepoBlacksmithExpressionSemantics`
  in `scripts/ci_workflow_test.go`): pr.yml's `fast-checks`,
  `advisory-reports`, `check-release-target-cross-compilation` (8 vCPU),
  `check-doc-flags` (4 vCPU), `pr-policy-wrapper` (4 vCPU)
  (and `test-dolt-server-fingerprint`, since retired); pr-risk.yml's
  `test-nix` (4 vCPU). Every
  other job keeps the default 2 vCPU label. Forks and Dependabot PRs fall back
  to `ubuntu-latest`, as F3's `ci-gate` jobs
  and bazel.yml's `rbe` job already do; `TestBlacksmithJobsReadNoSecrets`
  requires that no job eligible for a Blacksmith label ever reads a secret, so
  a maintainer re-running a Dependabot PR landing on Blacksmith (actor change,
  same as F3) is harmless.
- **`fast-checks` fold.** `check-build-tags`, `check-version-consistency`,
  `check-migration-hygiene`, `check-no-beads-changes` and `fmt-check` became
  five steps of one job, each with its own `id` and `if: ${{ !cancelled() }}`
  so one check's failure does not stop the others from running. The job
  exposes each step's `outcome` as a job output; ci-gate reads
  `needs.fast-checks.outputs.<x> || 'skipped'` into the same
  `CI_GATE_REQUIRED` token (`CHECK_BUILD_TAGS`, `CHECK_VERSION_CONSISTENCY`,
  `CHECK_MIGRATION_HYGIENE`, `CHECK_NO_BEADS_CHANGES`, `FMT_CHECK`) that token
  named before the fold, so a failing check still reds the exact same gate
  line. `FAST_CHECKS` (the job's own `.result`) is an added backstop token: if
  the whole job is lost before any step reports, the five per-check tokens all
  read `skipped` (not `skipped_ok`, so the gate still reds), but `FAST_CHECKS`
  gives `ci-gate.sh` one clear line naming the job instead of five confusing
  ones. `TestPRCIGateFastChecksTokens` pins the whole mapping. (The fold
  kept `CHECK_NO_BEADS_CHANGES`'s `pull_request`-only step `if:` and its
  `merge_group` entry in `CI_GATE_SKIPPED_OK`; merge queue readiness later
  made the step run on `merge_group` too and dropped that entry.)
- **Cross-compilation 8 → 2.** `check-release-target-cross-compilation`'s
  eight per-target matrix legs (one `go build` each) became two legs
  (`unix`, `desktop`) driven by `scripts/ci/release-targets.txt`, a
  `GOOS GOARCH GROUP` manifest kept in sync with `.goreleaser.yml`'s `builds:`
  list (plus the darwin/amd64 and darwin/arm64 exception release.yml's
  `goreleaser-macos` job builds natively) by
  `TestReleaseTargetCrossCompilationMatrixMatchesGoreleaser`. Each leg runs
  `scripts/ci/check-release-cross-compile.sh <group>`, which builds every
  target in its group sequentially and reports every failure before exiting
  non-zero, so a PR touching two platforms at once sees both failures in one
  log instead of needing a per-target re-run. (Since retired: bazel.yml's `bazel-release-cross`
  lane builds the same manifest with Bazel, `--platforms` per target.)
- **`advisory-reports` fold.** `build-examples` and `complexity-report` (both
  already advisory: neither was in ci-gate's `needs`/`CI_GATE_REQUIRED`)
  became one job's steps. Each keeps its pre-fold step-level
  `continue-on-error`/`timeout-minutes` design, because a job-level timeout or
  failure would cancel or red the job — exactly the blocking-by-a-different-door
  outcome both were written to avoid. The job does carry a generous job-level
  `timeout-minutes: 60` (review N-2: raised from an initial 45 for headroom
  above the sum of the step timeouts), because `TestSameRepoBlacksmithRunners`
  requires a timeout on every Blacksmith-eligible job; it is a backstop
  against a stuck runner, not a realistic ceiling.
- **`windows-make-shell` 3 → 1.** The `host: [native, msys2, cygwin]` matrix
  (three Windows job instances) became one job with three step pairs (install
  + exercise), each pair's own `id` and `if: ${{ !cancelled() }}` so a leg
  still runs after an earlier leg fails. Review SF-1 found the original fold's
  hand-written aggregator step (meant to reproduce the pre-fold rule — a
  native or MSYS2 failure reds the job, but only a Cygwin *exercise* failure
  does, since Cygwin *setup* stays warning-only as it was in the pre-fold
  `cygwin` matrix leg) survived a mutation that always exited 0, because
  `continue-on-error: true` was left on every install/exercise step as well as
  the Cygwin setup step. The aggregator was deleted outright:
  `continue-on-error` now sits only on the Cygwin *setup* step (id `cygwin`),
  so a native or MSYS2 install/exercise failure, or a Cygwin *exercise*
  failure, reds the job natively — no hand-written step can silently
  undershoot that. `test-windows-doltversion` and `test-windows-dbproxy-server`
  folded the same way into `test-windows-small`, with the same aggregator
  deletion; neither was ever in ci-gate's `needs`/`CI_GATE_REQUIRED`, so no
  gate token changed. `TestPRWindowsMakeShellLegsFailTheJob` pins the leg
  `if`/`continue-on-error` contract and the absence of both jobs' aggregator
  steps. Both folded jobs stay on `windows-latest` — F7a does not move any
  Windows job to Blacksmith (there is no Windows Blacksmith pool), and
  neither job is one F4's concurrent Windows-region edits touch.

### Repository Guards as Bazel Tests

ga-96smfk.20 moved the repository guards that pr.yml ran on GitHub runners
into Bazel tests over narrow slices of the checkout
(`tools/bazel/repo_subset.bzl`, laid out as a throwaway git checkout by
`scripts/repochecks/run_check.sh`), so they run remotely in bazel.yml's
`test` lane and a pass is reused until one of the files the guard reads
changes. They gate through `BAZEL_TEST`; pinned by
`TestPRPolicyChecksRunAsBazelTargets`.

| Former job / step | Bazel target |
| --- | --- |
| fast-checks: `check-build-tags.sh` | `//scripts/repochecks:build_tags_test` |
| fast-checks: `check-go-install-guidance.sh` | `//scripts/repochecks:go_install_guidance_test` |
| fast-checks: `check-winget-portable-alias.sh` | `//scripts/repochecks:winget_portable_alias_test` |
| fast-checks: `check-versions.sh` | `//internal/versioncheck:versioncheck_test` (same `versioncheck.Check` over the release metadata) |
| fast-checks: migration hygiene checks A and B | `//scripts/repochecks:migration_hygiene_test` (`MIGRATION_HYGIENE_SCOPE=tree`) |
| fast-checks: `make fmt-check` | `//scripts/repochecks:fmt_test` (the registered SDK's gofmt) |
| PR Policy: `checkworkflowtags` | `//scripts/repochecks:workflow_tags_test` |
| PR Policy: `check-testing-short.sh` | `//scripts:go_sources_test` (`TestCheckTestingShortPassesOnCleanRepoTree`) |
| PR Policy: workapi frontend boundary | `//scripts/repochecks:workapi_frontend_boundary_test` |
| PR Policy: `make api-check` drift | `//scripts/repochecks:types_gen_drift_test`; its `go test ./internal/httpapi/...` half is the httpapi targets in the same lane |
| PR Policy / check-doc-flags: `check-doc-freshness.sh` | `//scripts/repochecks:doc_freshness_test` |
| check-doc-freshness-platforms, Linux leg | `//scripts:doc_freshness_required_test` (integration lane) and `//scripts/gitattributespolicy:gitattributespolicy_required_host_test` |

What stays a job, and why:

- `fast-checks` keeps the two checks that need the PR's base commit:
  check-migration-hygiene.sh's delta half (`MIGRATION_HYGIENE_SCOPE=delta`:
  frozen shipped migrations, ignored-plane twins, prepared DML in new
  migrations) and the `.beads/issues.jsonl` guard. No action key can hold a
  base commit.
- `check-doc-flags` validates the generated CLI docs against bd built from
  the release tag in `docs/cli-docs.pin` (git fetch plus a pure-Go
  `go build`), and attributes drift against the merge-base. Releases publish
  no pure-Go Linux binary a Bazel repository could pin by sha256.
- The macOS and Windows legs of `check-doc-freshness-platforms` (no remote
  workers for those hosts), on Blacksmith for every PR and every push to
  `main` (a merge group skips them, see [Merge Queue](#merge-queue)).

`check-doc-freshness.sh` compares review dates with today's date, which is
not in the action key: a cached pass stands until one of its inputs changes,
and nightly's `--config=fresh` run re-checks it against the date.

### Blacksmith Windows and macOS Jobs

Every Windows and macOS job runs on Blacksmith for every PR, forks and
Dependabot included (ga-96smfk.22), and for every push to `main`; a merge
group skips them (the scope job's `platforms` switch, see
[Merge Queue](#merge-queue)). Everything Linux runs under Bazel on rbe-west. pr.yml's Windows jobs (`test-windows-liveness`,
`test-windows-small`, `worktree-remove-windows`, `windows-make-shell`) name
their `blacksmith-*vcpu-windows-2025` label literally; the two mixed-OS
matrix jobs, `pr-preflight-platforms` and `check-doc-freshness-platforms`,
run a macOS leg on `blacksmith-6vcpu-macos-26` (Apple Silicon) and a Windows
leg on `blacksmith-4vcpu-windows-2025`, each leg naming its label in the
matrix (`runs-on: ${{ matrix.runner }}`). An 8 vCPU Windows preflight leg
was tried (#7381): it restored the 4 vCPU saver's cache, but queued 63s for
the larger runner and finished slower (144s vs 129s), so it stays on 4 vCPU. There is no GitHub-hosted fallback:
Blacksmith serves this org's fork PRs (gascity's fork PRs run their CI on
`blacksmith-*` labels). Fork runs get no secrets and a read-only token
(`TestBlacksmithJobsReadNoSecrets` keeps every Blacksmith job free of secret
reads), each job runs in a fresh VM, and the repository's fork-PR approval
setting stays the boundary for who may run code there (a fork's own workflow
file controls `runs-on` anyway). `release.yml` and `nightly.yml` are not PR
checks and keep their own runners.

The Linux leg of `pr-preflight-platforms` and the Linux-cross-compiled
Windows test binaries (`windows-test-binaries`, its "-prebuilt" twins,
main.yml's `windows-test-binaries-cache` and the `WINDOWS_PREBUILT_REQUIRED`
flag) are retired: Bazel runs the Linux leg's tests, and the native Windows
pair (`test-windows-liveness`, `worktree-remove-windows`) is the required
run of the twins' tests.
macOS jobs; `release.yml` stays on `macos-latest`.

- **Label.** Pinned to `macos-26` because GitHub's `macos-latest` resolves to
  `macos-26-arm64` today, so both paths run the same OS and architecture and
  the PR legs and their saver never straddle a Blacksmith `-latest` alias
  move. Bump it when GitHub moves `macos-latest`. 12 vCPU is not justified
  for legs dominated by one incremental `./cmd/bd` test compile.
- **Caches.** The legs stay restore-only (`setup-go` `cache: false`,
  `actions/cache/restore`). Blacksmith cannot see GitHub-saved caches, so
  main.yml's `blacksmith-macos-go-build-cache` job is their seeder: same
  label, push-to-main-only job guard, module cache plus a non-race GOCACHE
  keyed by `go.sum` and UTC day, warmed by the shared
  `scripts/ci/warm-non-race-cache.sh`. main.yml's `test` job (the macOS
  full suite) runs on the same label and restores its caches. main.yml's
  `test-windows` seeds the Windows legs the same way.
- **Pins.** `TestBlacksmithWindowsMacOSRunsOnEveryEvent`,
  `TestBlacksmithMacOSSaverMatchesPRLegs`,
  `TestBlacksmithSaverJobsGuardedAgainstPullRequest`,
  `TestBlacksmithSaverCacheKeysAreNotPerCommit`,
  `TestBlacksmithReachablePRJobsDisableDefaultCachingActions` and
  `TestGoCacheOwnershipTopology`.

### Server Dolt Storage Matrix

Retired from `pr-risk.yml` (ga-96smfk.22): `bazel.yml`'s `bazel-server-storage`
lane runs `//internal/storage/dolt:dolt_server_full_test` (16 shards) and
`:dolt_server_conformance_test` against hermetic dolt sql-servers.

- `.github/scripts/server-storage-test-shard.sh` discovers top-level
  `Test*` functions from `internal/storage/dolt/*_test.go` (excluding
  `TestConformance`, which runs in its own target), assigns known-heavy
  tests via the committed `.github/scripts/server-storage-test-shards.txt`
  manifest, and hash-distributes everything else — the same
  manifest-plus-fallback mechanism `embedded-storage-test-shard.sh` uses.
- 16 shards: the package has 1126 top-level tests, each a real socket
  round-trip against a Dolt server with a per-test CREATE/DROP DATABASE.
  `TestCloudAuthCLIRouting` alone costs ~9.5 minutes and is pinned alone
  on shard 1 in the manifest so it cannot delay any other shard.

### Regression Tests

`regression.yml` is retired. The suite runs as
`//tests/regression:regression_test` (14 shards) in `bazel.yml`'s
`bazel-cmd-dolt` lane, which `pr.yml`'s `CI Gate / Required` requires
(`BAZEL_CMD_DOLT_REQUIRED`), so regression is now part of the aggregate gate
on PRs (a merge group skips the Dolt tiers; the push to `main` runs it)
instead of a separate, path-detected advisory workflow. Its path detector is replaced by test result caching: the target
re-runs only when `bd_for_tests`, the suite or its pinned inputs change. The
baseline is `@bd_releases//:<BASELINE_VERSION>` (`tools/bazel/bd_releases.bzl`,
pinned by `scripts/migration-test/release-catalog.json`); `TestMain` fails if
the binary does not report `BASELINE_VERSION`.

### Nix Build

`.github/workflows/nix-build.yml` dropped its `pull_request` trigger entirely
(F7c, spec-f7.md §2.4): it now runs `nix build .#default` only on push to
`main` and on `workflow_dispatch`, so it is no longer a PR check at all, let
alone a required one. PR coverage is PR Risk's required `test-nix` job
(`nix run .#default -- --help` plus `nix flake check -L`), which is a
superset of plain `nix build .#default`.

If the full `nix build` must additionally affect PR mergeability, move it (or
an equivalent build step) into an unfiltered required PR workflow behind a
detector and job-level `if`, then teach the aggregate gate when a skipped Nix
build is acceptable. Do not make `nix build .#default` directly required on
`nix-build.yml` - its path filter means it would silently fail to report on
PRs that don't touch Nix or Go module files.

### Cross-Version Smoke

`cross-version-smoke.yml` is retired. Its suite runs as
`//tests/upgrade_smoke/...` inside `bazel-test`, which `CI Gate / Required`
requires, so it now gates PRs and merge groups through the aggregate. Each
target pins its previous release from the release catalog (`@bd_releases`)
instead of resolving `gh release list` at run time, so the tested set is
reviewed: the newest 30 stable releases with a linux/amd64 asset, where the
workflow tested the newest 5 GitHub releases (prereleases included) on PRs
and 30 on tag pushes.

## Merge Queue

`pr.yml` and `pr-risk.yml` run on `merge_group` (`types: [
checks_requested ]`) and report `CI Gate / Required` and `PR Risk Gate /
Required` on each queue entry's merge-group commit, so the ruleset's two
required contexts work unchanged once the queue rule is on. A merge group
runs only the fast build and unit set; the Windows, macOS and Nix jobs and
the Dolt-tier Bazel lanes are skipped there by design and run after the
merge, on `main`'s push ([Post-merge complement](#post-merge-complement)).
`scripts/ci_merge_queue_test.go` simulates a `merge_group` run through the
shared `evalGHExpr` evaluator and the workflows' own step scripts;
`scripts/ci_merge_group_scope_test.go` runs the scope script and both gates'
real steps over a pull request, a merge group and a push to `main`.

### Queueing a PR

```bash
gh pr merge <number> --auto --squash --repo gastownhall/beads
```

On a green PR this adds it to the queue; while the PR's own required
checks are still running, `--auto` queues it once they pass (this needs
the repository's "Allow auto-merge" setting). The queue's own merge
method (squash) decides how it lands. `gh pr merge <number>
--disable-auto` disarms a PR still waiting for its checks; gh leaves a PR
that is already queued as it is, and only "Remove from queue" in the UI or
the GraphQL `dequeuePullRequest` mutation takes it out. `--admin` (bypass
actors only) merges directly, skipping the queue.

Queue settings (`merge_queue` rule in the `beads main: required CI gates`
ruleset): merge method `SQUASH`, grouping strategy `ALLGREEN`,
`max_entries_to_merge` 5, `max_entries_to_build` 3, `min_entries_to_merge`
1, `min_entries_to_merge_wait_minutes` 2 (it only delays merging while
fewer than `min_entries_to_merge` entries are queued, so at 1 it never
waits), `check_response_timeout_minutes` 60. The build limit (3) and the
timeout (60) leave headroom while the farm scales from zero, so a backlog
does not time entries out and set off a rebuild cascade. The fast set
leaves `max_entries_to_build` at 3; raising it to 5 is the follow-up, once
the first days' `merge_group` durations confirm the shorter runs. The
required status checks stay exactly `CI Gate / Required` and `PR Risk Gate
/ Required` (GitHub Actions).

### What runs on a merge group

Each queue entry gets a commit on `gh-readonly-queue/main/pr-<n>-<sha>`:
`main` plus every PR ahead of it in the queue plus this PR. Both
workflows run on that commit like a same-repo PR, but only the fast set.

- Scope: one script, `.github/scripts/ci-scope.sh`, decides what each
  event runs. Both workflows' `scope` job runs it (the only job that reads
  the event; it checks out nothing else), and every other job needs it and
  runs on one of its switches:

  | Event | `platforms` | `dolt-tiers` | `linux` | `advisory` |
  | --- | --- | --- | --- | --- |
  | `pull_request` | true | on | true | true |
  | `merge_group` | false | off | true | false |
  | `push` to `main` | true | on | false | false |

  - `platforms`: the Windows and macOS jobs (`test-windows-liveness`,
    `test-windows-small`, `worktree-remove-windows`, `windows-make-shell`,
    `check-doc-freshness-platforms`, `pr-preflight-platforms`) and
    `pr-risk.yml`'s `test-nix`.
  - `linux`: `fast-checks`, `check-doc-flags` and the `bazel` call.
  - `dolt-tiers`: passed to the `bazel` call. `off` skips
    `bazel-embedded`, `bazel-dolt-race` and `bazel-dolt-int` (outside mode
    `remote` their members `bazel-doltserver`, `bazel-proxied`,
    `bazel-server-storage` and `bazel-cmd-dolt`), and the call reports
    what it ran as its `dolt-tiers` output. Anything but `off` (every other
    caller, push and dispatch) runs every lane. With `cmd-dolt-required`
    the integration build's `//cmd/bd:bd_test` belongs to the cmd/bd
    Dolt-server tier, so a merge group defers it as well.
  - `advisory`: `advisory-reports`, pull requests only.

  Any other event fails the scope job, and so both gates.
- So a merge group runs `fast-checks`, `check-doc-flags`, the package
  gates and the Bazel lanes `test`, `pure-Go and js/wasm`, `release
  cross-compile` and `integration`. Merge-group job end times before the
  change (W1, 2026-10, p50/p90 minutes from run creation): embedded
  6.5/13.8, dolt-race 5.0/10.0, dolt-int 4.5/11.7, test 4.0/8.5, the
  Windows jobs 2.4-3.0/3.8-4.8, Nix 2.6/4.0, integration 2.3/8.8, release
  cross-compile 2.0/7.5, pure-Go 1.9/6.1. Without the skipped jobs the
  merge-group run's p50 drops from about 7.0 to about 4.2 minutes, ending
  with the `test` lane, and the Windows Make installers' network flakes (8
  of the 12 W1 ejections) no longer eject entries. The scope job adds one
  short Blacksmith job (a sparse checkout of the script) before the lanes
  start.
- Trust: same repository, CI secrets present, actor
  `github-merge-queue[bot]`. Every Blacksmith ternary names
  `merge_group` explicitly, `bazel.yml`'s `rbe` job takes mode `remote`
  (never asks rbe-fork), and `rbe-prewarm` runs (mode `remote`).
  A merge group runs the workflow files and scripts of the queued commit,
  not of `main`, and no approval gate applies. A queued fork PR's own edits
  to `.github/`, `.bazelrc`, `tools/bazel/`, `MODULE.bazel`, BUILD files or
  `scripts/ci/` therefore run with every CI secret: the RBE client
  certificate and the shared `bazel-allocator` app key (rbe-prewarm). They
  run even if the entry is later ejected and never merges. A queued
  Dependabot PR likewise runs with the Actions secrets. That is the trust
  of merging (a push to `main` would run the same code). **Maintainer
  rule: review a fork PR's changes to those paths before queueing it,
  exactly as before merging.** A ruleset `pull_request` rule (1 approval,
  approval of the most recent push) would enforce a review before queueing.
  That decision is pending.
- The gate requires the fast set's Bazel lanes to have run remotely and
  passed (`BAZEL_REMOTE`), exactly as on a PR: with the farm switch off
  (mode `skip`) or a missing executor secret (mode `local`) it is red.
  `bazel-integration` and the package gates run as on a PR. The Dolt-tier
  ids (`BAZEL_EMBEDDED`, `BAZEL_DOLTSERVER`, `BAZEL_PROXIED`,
  `BAZEL_SERVER_STORAGE`, `BAZEL_CMD_DOLT`) may skip only because
  `bazel-gate.sh` sees both halves: the scope turned the tiers off and the
  call reports `dolt-tiers` `off`. A Dolt-tier lane that ran anyway and
  failed still reds the gate.
- Diff bases: a merge group has no `pull_request` payload and no
  `base_ref`. The migration-hygiene `BASE_SHA`, the package gates'
  `PR_BASE_SHA`/`PR_HEAD_SHA`, the `.beads/issues.jsonl` guard and the
  CLI-docs drift attribution (`BD_DOCS_DIFF_BASE`; without a base it would
  fail every entry on drift already on `main`) fall back to
  `github.event.merge_group.base_sha`/`head_sha`; every other
  `github.event.pull_request.*` read is classified in
  `TestMergeQueuePullRequestFieldsHandleMergeGroup` (null is correct
  there: fork facts, the rbe-fork PR number, autofix metadata). With
  ALLGREEN every entry must pass, so each entry's own diff from its base
  commit is enough (the base is `main` or the entry ahead of it). With
  HEADGREEN only the batch head would have to pass and a path-filtered
  package gate could miss an earlier entry's change: keep ALLGREEN.
- Concurrency: groups key on `github.event.pull_request.number ||
  github.ref`, and a merge group's ref is its own queue branch, so queue
  entries never cancel each other or the PR's own runs. `bazel.yml` never
  cancels in progress for a merge group.
- Autofix: `bazel-autofix.yml` and `docs-autofix.yml` act only on
  `pull_request` runs; a red merge group never gets an autofix push.

### Post-merge complement

The queue lands a batch of entries (up to `max_entries_to_merge`) as one
push: `main` fast-forwards to the last entry's tested merge-group commit,
the same tree, which contains every entry of the batch. Push workflows run
once per push, for its head commit, so the push runs of that head run the
rest:

- `pr.yml` (scope `push`): the Windows and macOS jobs, and `CI Gate /
  Required` over them. The Linux jobs and the whole `bazel` call are
  skipped and excused there: the merge group ran the former on the same
  tree, and `bazel.yml`'s own push run replaces the latter. `pr.yml` never
  calls `bazel.yml` on a push: that run's `rrc-seed` mints its writer
  certificate only for a main-push `bazel.yml` token.
- `pr-risk.yml` (scope `push`): `test-nix`.
- `bazel.yml`'s own push run: every Bazel lane (it has no inputs there, so
  the Dolt tiers run, and `bazel-integration` keeps `//cmd/bd:bd_test`).
  A cmd/bd Dolt-server tier failure fails `bazel-dolt-int` there (see
  [dolt-int Union Lane](#dolt-int-union-lane)), as every other Dolt-tier
  lane's failure fails its own job.
- `main.yml`, as before.

The invariant (`TestCIScopeCoversEveryGatedJobOnMain`,
`TestCIScopeExcusedIDsRedTheirPushRun`): every push head on `main` runs
every job either gate reads on a pull request, the fast set in its merge
group and the rest in its push runs, and every id a merge group's gates
excuse turns that push run red when its job fails there. An earlier entry
of a batch gets the fast set on its own tree in its own merge group, and
the complement only as part of the batch head's tree, which also holds
the entries after it. `advisory-reports`, which neither gate reads, runs
on pull requests only. A direct push to `main` (a bypass actor) has no
merge group: as before, only its push runs check it.

`pr.yml`, `pr-risk.yml` and `bazel.yml` key a push run's concurrency group
on its commit, so a later push never cancels an earlier one's complement
run or displaces it while it waits (GitHub cancels a group's pending run
when a newer one queues).

The cost: a regression only the Windows, macOS, Nix or Dolt-tier jobs see
is no longer stopped by the queue. The PR's own run still runs them, so
what reaches `main` is a regression that came from combining the PR with
what landed after that run, or a flake; it surfaces on the push run, and
the alert names every PR of the pushed range as a candidate.

Caveats:

- Concurrent push runs. Keying push runs on their commit lets a landing
  burst run several full `bazel.yml` push runs at once: more farm load,
  and parallel `rrc-seed` jobs. `rrc-seed`'s writer mint retries a 429
  for about a minute (`.github/scripts/rrc-writer-credential.sh`) and then
  fails the job, which reds that push run for infra noise; the alert then
  reports it like any red, and re-running the failed job clears it. If it
  shows up, give `rrc-seed` a shared job-level concurrency group that
  queues instead of cancelling (`queue: max`; its uploads are
  content-addressed, so serial seeding loses nothing). Not done here: a
  key GitHub rejected in `bazel.yml` would fail every pull request's call
  at startup.
- Mode `remote`. The complement assumes `bazel.yml`'s push run takes mode
  `remote`, as the merge group did (its gate requires it, `BAZEL_REMOTE`).
  A push run in mode `skip` (the farm switch turned off) runs no lane, and
  one in mode `local` (the executor secret gone) skips the remote-only
  tiers and integration; both stay green on what they skipped. Only a change of the switch
  or the secret between a merge group and its push gets there.

### How a red main surfaces

`.github/workflows/main-ci-alert.yml` runs on `workflow_run` (completed)
of `PR`, `PR Risk`, `Bazel` and `Main` on `main`, and acts only on this
repository's push runs (`.github/scripts/main-ci-alert.sh`):

- A red push run (`failure`, `timed_out`, `startup_failure`) opens the one
  `ci/main-red` issue with the workflow, the run, the head SHA and subject,
  the candidate PRs and the failed jobs; while that issue is open, later
  reds comment on it instead. The candidates are every PR a squash subject
  (`(#N)`) names between that workflow's previous decided push run's head
  and this run's head (the compare API, at most 100 commits; the head
  commit's PR alone when there is no such run or the range cannot be
  read): a batch lands as one push.
- It comments on each candidate (at most the newest ten) only when this
  run turns the workflow red (its previous decided push run was green, or
  there is none) and is the run's first attempt. While the workflow stays
  red, later reds, and a re-run that goes red again, update the issue
  alone. Push runs overlap (each push keys its own concurrency group) and
  can finish out of order: a red run with a newer green push run of the
  same workflow is stale and notifies nobody, and reports only on an issue
  already open. A runs or jobs API error does not lose the issue: the
  candidates fall back to the head commit's PR (notified), or the job list
  to a link to the run, with a warning.
- A later green push run of the same workflow comments that it is green
  again, and the issue closes once no tracked workflow's latest decided
  push run on `main` is red. Cancelled runs (`main.yml` cancels older
  runs) say nothing either way: the script reads the runs API page after
  page past them. The API lists a workflow's newest 1000 push runs only; a
  workflow none of them decided is unknown and keeps the issue open.
- It holds `issues: write`, `pull-requests: write`, `actions: read` and
  `contents: read`, checks out only its script from the default branch,
  reads no artifact and runs nothing from the run it reports on. Runs are
  serialized (`queue: max`), so two reds finishing together open one
  issue.

Fix forward or revert; the issue names the candidates.

### Rolling back the fast set

Make the merge group's scope full again: give `merge_group` the
`pull_request` row in `.github/scripts/ci-scope.sh` (`platforms=true
dolt-tiers=on linux=true`) and update `ciScopeRows` in
`scripts/ci_merge_group_scope_test.go`. The merge group then runs every job
again and the gates excuse nothing there; no ruleset change is needed. The
push runs keep running the complement, redundantly, until they are removed
too.

### Test result caching

Bazel lanes rerun only what changed. Test results are cached (read and
written, Bazel's default) on every run except nightly's: on a PR (any
attempt), a merge group and a push to `main` alike. A test whose action key
(test binary, runfiles, test env, platform) is unchanged reuses the PASS
the farm recorded for it; a test whose inputs changed runs. The PR's own
run seeds the cache, so a merge group re-runs only the tests the merge
changed (on a fresh base, those depending on what landed in between).
`nightly.yml` passes `fresh-test-results: true`, so every `bazel test` in
`bazel.yml` appends `--config=fresh` (`--nocache_test_results`) last, and
the whole suite re-executes once a day. A dispatch of `bazel.yml` can ask
for the same.

This replaces the D2 rule "always execute" (`--nocache_test_results` in the
retired tiers' configs and `sole-run`, parity with the legacy
`-test.count=1` jobs, review F2). That rule also stopped Bazel *writing*
results, so nothing could ever be reused. Why caching is safe:

- A cached PASS is valid for identical inputs: these targets are hermetic
  (dolt and every input are runfiles).
- A cached failure is never reused: Bazel treats a failed cached action
  as a miss, so whatever failed before runs again.
- Poisoning is bounded: only rbe-west's workers write the action cache
  (client writes are refused, forks are read-only), so a cached PASS is a
  real remote execution, and anyone able to run a same-repo branch already
  holds the CI certificate (accepted risk in `bazel.yml`'s header).
- What is given up is re-sampling a nondeterministic (-race, timing) test
  on unchanged inputs between nightlies. A flaky test fails at most once
  per change before it is cached as passing, and nightly samples
  everything again.
- No retries were added: `--experimental_remote_cache_eviction_retries=0`
  (retired tiers' configs and `sole-run`) and the no-`flaky_test_attempts`
  policy are unchanged.

To confirm reuse on the farm, count `testResult` events with
`cachedRemotely` in the BEP of the first few merge-group runs. The same
check shows that `test.xml` is downloaded for cache hits, which
`equivalence.py`, `check_testcases.py` and the pure-Go lane's test-count
check read.

Non-Bazel required jobs re-run in full wherever they run (approximate PR
timings, 2026-10): on a merge group `fast-checks` (~40 s),
`check-doc-flags` (~1.7 min) and the package gates (seconds unless their
paths changed); on a pull request and on the push to `main` also
`check-doc-freshness-platforms` and `pr-preflight-platforms` (Windows and
macOS legs, up to ~5 min on Windows), `windows-make-shell` (~2.7 min), the
native Windows pair (`test-windows-liveness`, `worktree-remove-windows`,
~1-5 min) and `test-nix` (~3 min). None caches results the way Bazel
does. Those platform jobs used to be the merge group's critical path (the
Windows legs, ~5 min) and its flakiest checks (the Windows Make
installers' network); a merge group now skips them, so its critical path
is the Bazel `test` lane, which reuses cached results. An entry's first
build after a base change still compiles what changed.

### Failures and flakes

A red required gate on an entry ejects that PR from the queue (GitHub
comments on it); every entry behind it is rebuilt without it and re-runs.
With ALLGREEN a batch merges only when every entry in it is green. A
flake therefore costs the flaky PR its place and re-runs everything
behind it; re-queue the PR (`gh pr merge --auto --squash` again). A
re-queued entry on unchanged inputs reuses its Bazel lanes' passing
results, so the retry re-runs the failed tests and the non-Bazel jobs; a
reused PASS cannot hide a failure, because failures are never cached. The
same holds for "Re-run jobs" on a PR: passing tests are not re-executed.
To re-sample a suspected flake on unchanged inputs, dispatch `bazel.yml`
on the branch with `fresh-test-results` checked. Runs of queue entries
that were invalidated (an entry ahead was ejected) keep running to
completion and use farm capacity; plan the build limit with that in mind.
The 60-minute check timeout ejects an entry whose gates never report;
both gates normally finish in about 4 minutes (p50; 8-9 at p90) on the
fast set. A flake in a job the merge group skips (a Windows installer
download, a Dolt-tier lane) no longer ejects anything: it reds a push run
on `main` instead, which opens or comments on the `ci/main-red` issue
([How a red main surfaces](#how-a-red-main-surfaces)); re-run that push
run's failed jobs.

Rollback of the queue: delete the `merge_queue` rule from the ruleset.
Queued PRs fall back to the normal merge button; the `merge_group`
triggers are inert without a queue. Rollback of the fast set alone:
[Rolling back the fast set](#rolling-back-the-fast-set).

Policy for `merge_group`:

- `PR` and `PR Risk` must include `merge_group` (`checks_requested`), and
  no job or step condition in them may key on the event: decisions belong
  in detector jobs (`TestMergeQueueRequiredGatesRunOnMergeGroup`). The
  scope job is that detector for what a merge group or a push to `main`
  skips; `bazel.yml`'s `rbe` job decides the execution mode.
- The scope job is the only place that may excuse a skip, and only these
  sets: on a merge group the Windows, macOS and Nix jobs
  (`CI_SCOPE_PLATFORMS_IDS`) and, through `bazel-gate.sh` and only when
  the Bazel call reports `dolt-tiers` `off` too, the Dolt-tier lanes; on a
  push to `main` `pr.yml`'s Linux jobs and its whole Bazel call
  (`CI_SCOPE_LINUX_IDS`); on a pull request nothing. A scope job that
  failed, or whose outputs are not exactly one event's row, reds both
  gates (`TestCIScopePRGate`, `TestCIScopeRiskGate`). The execution-mode
  skips of `bazel-gate.sh` are unchanged.
- Any risk detector added to the required topology should default to run on
  `merge_group`, because the merge group commit may combine individually safe
  PRs into a risky integration state.
- Beyond those sets the aggregate gates evaluate merge group results exactly
  like PR results.

## Initial Rollout Snapshot

The following checklist records the original rollout plan. It is retained as
decision context, not as a current deployment procedure. Workflow plumbing is
implemented; the branch-protection and ruleset policy changes in steps 7 and 8
remain pending maintainer decisions.

1. Add `.github/scripts/ci-gate.sh` and aggregate gate jobs to the required PR
   workflows. The initial implementation was developed on branch
   `ci/bd-am3.1-wrapper-commands`.
2. Open a PR and verify the new aggregate check names appear exactly as
   expected from GitHub Actions.
3. Verify the gate succeeds on a docs-only PR where embedded jobs are skipped.
4. Verify the gate succeeds on a risky PR or manual test branch where embedded
   jobs run and pass.
5. Verify a deliberately failing underlying job makes its aggregate gate fail.
6. Verify a merge queue run reports the aggregate gates on the merge group.
7. Update the default-branch ruleset or branch protection to require only
   the aggregate gates from GitHub Actions.
8. Remove any direct requirements for individual CI, regression, Nix, or
   cross-version job names.

## Rollback Steps

1. Remove the aggregate gate checks from the default-branch ruleset or branch
   protection.
2. Restore the previous required check list if one existed.
3. Revert the workflow commit that added the aggregate gate job and evaluator.
4. Confirm a fresh PR no longer waits for the aggregate gates.

If rollback is needed because the aggregate logic is wrong, prefer first
relaxing branch protection to remove the aggregate requirement. That unblocks
merges without hiding the failed workflow logs needed for diagnosis.

### Blacksmith Off

No slice that moves a job onto Blacksmith reads a repository variable to do
so (F3's precedent, kept by F7a) — the runner choice is a literal
`github.event_name`/`github.actor`/`head.repo.full_name` expression baked
into each job's `runs-on:`, so rollback is a plain revert of the commit(s)
that changed it. Each slice reverts independently.

In a Blacksmith outage, every same-repo job pinned to a `blacksmith-*` label
would queue forever on a missing runner label instead of falling back to
`ubuntu-latest` (the ternary's fallback arm is for forks/Dependabot, not for
the label being unavailable). Revert these four together to take same-repo
PRs off Blacksmith entirely and back onto `ubuntu-latest`:

1. **F3's gate runners** — pr.yml's and pr-risk.yml's `ci-gate` jobs, and
   their `scope` jobs, which share the gates' runner expression.
2. **bazel.yml's `rbe` job** runner.
3. **F7a** — pr.yml's `fast-checks`, `advisory-reports`,
   `check-release-target-cross-compilation`, `check-doc-flags`,
   `pr-policy-wrapper`, `test-dolt-server-fingerprint`; pr-risk.yml's
   `test-nix`.
4. **F7b** (cache-dependent moves, once that slice lands) — main.yml's
   Blacksmith cache seeds and the `scripts-go-checks`/`pr-lint-wrapper`/
   preflight/doc-freshness runner moves, including the preflight/
   doc-freshness macOS legs and `blacksmith-macos-go-build-cache`
   ([Blacksmith Windows and macOS Jobs](#blacksmith-windows-and-macos-jobs)).
5. **ga-96smfk.22** — the Windows and macOS jobs' literal Blacksmith labels
   ([Blacksmith Windows and macOS Jobs](#blacksmith-windows-and-macos-jobs)):
   during an outage point them back at `windows-latest` / `macos-latest`.

F7c (the advisory workflows) is excluded from this list: it is advisory only,
so leaving it on Blacksmith during an outage delays non-required checks but
never blocks a merge.

## Commit-Message Skip Directives

The topology above prevents pending required checks caused by path-filtered and
branch-filtered workflows. GitHub can still skip `push` and `pull_request`
workflows when the HEAD commit message contains skip directives such as
`[skip ci]`. If maintainers want a hard guarantee that commit-message skips
fail closed instead of pending, the required check must be emitted by a tiny
trusted reporter that is not itself skipped by those directives, for example a
`pull_request_target` workflow that does not check out or run PR code and
creates a check run named `CI Gate / Required` on the PR head SHA after
inspecting the untrusted `pull_request` workflow results.

That reporter is intentionally outside the narrow first rollout. Until then,
do not use commit-message skip directives on PRs targeting `main`.
