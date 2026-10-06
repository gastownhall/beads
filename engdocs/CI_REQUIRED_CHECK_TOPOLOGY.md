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
  Runs on `pull_request` and `merge_group`. Contains the baseline PR jobs,
  Linux build artifact stage, policy/lint compatibility jobs, package gates
  that consume the Linux artifact, focused storage domain/uow coverage, the
  Bazel lane (the `bazel` job calls `bazel.yml`), and the baseline aggregate
  gate `PR / CI Gate / Required`.
- `.github/workflows/bazel.yml`: `Bazel`
  Runs on `push` to `main`, manual dispatch, and `workflow_call` only. PRs and
  merge groups run it once, through `pr.yml`'s `bazel` job. Its `rbe` job
  decides the execution mode once (`remote`; for fork and Dependabot PRs,
  which get no secrets or variables, rbe-west's certificate mint decides
  (`rbe-mint.ops.gascity.com:8444/v1/status`, infra README "rbe-fork"):
  `fork-ro` or `fork-rw` while it is open for the run, every lane executing
  remotely on `rbe-fork.ops.gascity.com:8444` with its own short-lived
  certificate (instance `oss-fork`, the fork pool; or `oss`, the OSS pool,
  for allowlisted authors), else `cache`: local execution plus rbe-west's
  anonymous read-only action cache, `.bazelrc`'s `fork-cache` config;
  `local` for `rbe=off` dispatches; or `skip` while the `RBE_WEST_WORKERS`
  repo variable is unset) and exports it as the `rbe-mode` / `rbe-enabled`
  outputs (`rbe-enabled` is `true` in `remote`, `fork-ro` and `fork-rw`);
  every lane exports its
  `job.status` as an output named after the job. `pr.yml`'s gate requires the
  call's result (`BAZEL`) and `BAZEL_TEST`, `BAZEL_PURE`, `BAZEL_EMBEDDED`,
  `BAZEL_INTEGRATION`, `BAZEL_DOLTSERVER`, `BAZEL_PROXIED` and
  `BAZEL_SERVER_STORAGE`. The legacy jobs these mirror stay required in
  `pr.yml` and `pr-risk.yml`, except where `BAZEL_EMBEDDED`;
  `BAZEL_PROXIED` and `BAZEL_SERVER_STORAGE`; or `BAZEL_TEST`, `BAZEL_PURE`
  and `BAZEL_DOLTSERVER` run in their place (see
  [Legacy Tier Retirement](#legacy-tier-retirement-d2)).
  `.github/scripts/bazel-gate.sh` reads the exported mode, never the variable
  or the fork flag: mode `skip` allows every Bazel id to skip, mode `cache`
  allows only the remote-only `BAZEL_EMBEDDED`, `BAZEL_PROXIED` and
  `BAZEL_SERVER_STORAGE` (fork and Dependabot PRs rely on `pr-risk.yml`'s
  legacy tiers for them), mode `local` (`rbe=off` dispatches) those and
  `BAZEL_INTEGRATION`, and modes `remote`, `fork-ro` and `fork-rw` allow
  none.
  A lane that should run and fails, is cancelled, or reports no result fails
  the gate, and so does a missing or invalid mode.
  `bazel-integration` (`bazel test //... --config=integration`, the Bazel
  successor of `main.yml`'s former Linux integration shards) is required on every PR:
  `pr.yml` passes `integration: "on"` (policy-pinned), so it runs in modes
  `remote` (same-repo PRs) and `fork-ro`/`fork-rw` (fork and Dependabot PRs
  while rbe-fork is open), and in mode `cache` (fork and Dependabot PRs
  while it is closed, locally on the GitHub-hosted runner with the
  read-only cache), and a
  failure, cancellation or missing result turns the gate red. Its legacy
  twins still run only on push to `main`, so this lane is the only PR-time
  run of the full integration-tagged suite. In mode `cache` every action
  trusted CI already ran is a cache hit, so the runner builds and tests only
  what the PR changed. Simulated on a 4-CPU runner (2026-10-02): about 2.5
  minutes warm, 3 to 5 for a `cmd/bd` change, and about 30 with the cache
  closed. Its step timeout (75 minutes) covers that cold local run with
  room to spare, and so also a remote cold compile plus the 1200 s
  per-action cap (both policy-tested). Mode `local` (`rbe=off`) still
  skips it.
  `scripts/ci_workflow_test.go` fails when a new `bazel.yml` job has neither
  a gate id nor an advisory entry.
  Runbook: if `main`'s Bazel BUILD files drift (every PR's Bazel lanes go
  red, and autofix only patches packages the PR itself changed), the RBE
  farm is down, or the beads CI RBE client certificate expires (about
  2027-09-27; a partial secret set also fails every same-repo run), fix
  `main` (`make bazel-sync`) or renew the secrets. To turn remote execution
  off instead, first commit `BAZEL_RETIRES_LEGACY_EMBEDDED: "false"`,
  `BAZEL_RETIRES_LEGACY_DOLT_SERVER_TIERS: "false"` and
  `BAZEL_RETIRES_LEGACY_PR_LANES: "false"` in both
  `pr.yml` and `pr-risk.yml` (see
  [Legacy Tier Retirement](#legacy-tier-retirement-d2)),
  then unset the `RBE_WEST_WORKERS` repo variable: same-repo runs then take
  mode `skip`, which the gate accepts (fork PRs still build, in mode `cache`,
  so drift on `main` still reaches them). Unsetting the variable while a
  flag is still `"true"` turns `CI Gate / Required` red on every same-repo PR
  (`BAZEL_EMBEDDED_RETIRED`, `BAZEL_DOLT_SERVER_RETIRED`,
  `BAZEL_PR_LANES_RETIRED`), because the legacy tiers no longer run for
  them. If rbe-west closes the read-only cache (the farm's kill switch; the
  client has none), fork runs fall back to executing everything locally
  (slower, still green; `bazel-integration` then takes about 30 minutes). If it
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
  Runs on `pull_request` and `merge_group`. Contains embedded Dolt risk
  detection, the `bazel-coverage` decision, embedded build/test
  shards, the proxied and server Dolt shards, the Nix flake smoke check, and
  the risk aggregate gate `PR Risk / PR Risk Gate / Required`.
- `.github/workflows/main.yml`: `Main`
  Runs on pushes to `main`. Contains the main branch health checks, package
  gates, platform smoke/short coverage, embedded Dolt coverage, and promoted
  Linux no-short integration shards.
- `.github/workflows/regression.yml`: `Regression Tests`
  Runs on `pull_request`, `push` to `main`, and manual dispatch. Does not
  currently run on `merge_group`. Uses job-level conditional regression
  execution.
- `.github/workflows/cross-version-smoke.yml`: `Cross-Version Smoke Tests`
  Runs on every PR to `main`, tag pushes, and manual dispatch. Does not
  currently run on `merge_group`.
- `.github/workflows/nix-build.yml`: `nix build`
  Uses workflow-level `paths` filters on `pull_request` and `push`. This
  workflow must not be directly required.
- `.github/workflows/update-vendor-hash.yml`:
  `Update vendorHash for dependabot Go bumps`
  Runs on `pull_request_target` for Dependabot Go bumps. It mutates Dependabot
  branches and must not be a required PR check.

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

Fork PRs get no Actions secrets, so `pr.yml`'s Bazel call runs them in mode
`cache` (local execution with the anonymous read-only cache, slower; the
remote-only embedded, proxied-server and server-storage tiers skip). `bazel-farm.yml` gives the
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
  restore two caches:
  - The Bazel runner cache (`bazel-repo-v3-*`, restore-keys prefix). It now
    holds only the content-addressable `--repository_cache`, whose hits
    Bazel re-hashes. The Bazel binary is not cached: `setup-bazel`
    downloads it into a fresh Bazelisk home with `BAZELISK_VERIFY_SHA256`
    and checks it against a sha256 pinned for `.bazelversion`. The repo
    contents cache (extracted repos, never re-verified) is off
    (`--repo_contents_cache=`).
  - The Go module cache (`beads-go-mod-v2-*`). `bazel-test` runs
    `go mod verify` right after restoring it.

  Outside the farm's path, other workflows restore caches that a writable
  default-branch scope would poison:
  - `release.yml`'s `setup-go` default cache (GOMODCACHE and GOCACHE, with a
    key predictable from `go.sum`, in a job that signs and attests
    binaries);
  - the `beads-go-build-v2-*` GOCACHE entries restored by `main.yml` and
    `pr.yml`;
  - the executables in `smoke-binaries-*`, `historical-dolt-*` and
    `regression-baseline-*`.

  GitHub's read-only default covers all of them. Hardening `release.yml`
  with `cache: false` is tracked separately.

What anyone else can do: nothing new.

- An unlisted author's fork PR triggers a run whose `authorize` job reads
  the base allowlist and says no. `farm` is skipped, and no secret or PR
  code is involved.
- Fork `pull_request` runs still get no secrets, and `bazel.yml` keeps them
  local (mode `cache`) even if the fork's `pr.yml` passes `fork-farm`,
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

- `Detect CI tier`
- `Fast checks (build tags, versions, migrations, beads diff, fmt)` (F7a fold
  of the former standalone `Check build-tag policy`, `Check version
  consistency`, `Check for .beads changes` and `Check formatting` checks into
  one job's steps; see
  [F7a: Same-Repo Blacksmith Moves and Job Folds](#f7a-same-repo-blacksmith-moves-and-job-folds))
- `Check pure-Go and js/wasm boundaries (CGO_ENABLED=0)`
- `Check doc flags freshness`
- `Check release target cross-compilation (unix)` and `(desktop)` (F7a fold
  of the former eight-target `check-release-target-cross-compilation` matrix)
- `Windows Make shell (native, msys2, cygwin)` (F7a fold of the former
  `host: [native, msys2, cygwin]` three-job matrix)
- `Test (Windows) small packages (doltversion, dbproxy server)` (F7a fold of
  the former `test-windows-doltversion` and `test-windows-dbproxy-server`)
- `Test (ubuntu-latest)`
- `Test (macos-latest)`
- `Test (storage domain + uow)`
- `Test (Dolt server fingerprint)`
- `Go checks (scripts-test)`, `Go checks (vet)` and `Go checks (allowlisted)`
- `Contract corpus (golden + determinism + conformance)`
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
- `PR Lint (native)`, `PR Lint (windows)` and `PR Lint (darwin)`
- `Test Nix Flake`
- `Differential Regression (v0.49.6 baseline)`
- `Upgrade smoke (chunk N)` (F7c: folded from one job per version into one job
  per 5-version chunk; still never require a matrix-expanded chunk job
  directly)
- `Resolve versions to test`
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
```

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

`.github/workflows/pr-risk.yml` has a companion aggregate for `detect-ci-tier`,
`bazel-coverage`, `build-embedded`, the embedded, proxied and server
Dolt test jobs, and `test-nix`.

`.github/scripts/ci-gate.sh` is a small shell evaluator. It fails on any
`failure` or `cancelled` result. It accepts `skipped` only for jobs that are
intentionally absent for that event or risk tier:

- (Historical: `CHECK_NO_BEADS_CHANGES=skipped` was accepted on
  `merge_group` while the step was PR-only. It now runs on `merge_group`
  too, against `merge_group.base_sha`, and no skip of it is excused.)
- In the risk aggregate, `BUILD_EMBEDDED` and the embedded, proxied and server
  Dolt test ids may be `skipped` when `FULL_EMBEDDED != true`.
- In the risk aggregate, `TEST_EMBEDDED_STORAGE`, `TEST_EMBEDDED_CONFORMANCE`
  and `TEST_EMBEDDED_CMD` may also be `skipped` when `bazel-coverage`
  reported `embedded=true`; `TEST_PROXIED_CMD`, `TEST_SERVER_STORAGE` and
  `TEST_SERVER_STORAGE_FULL` when it reported `dolt_server=true`; and
  `BUILD_EMBEDDED` when it reported both. `BAZEL_COVERAGE` (that job's
  result) must be `success`.
- In the baseline aggregate, `BUILD_ARTIFACTS`, `PR_CORE_WRAPPER`,
  `CHECK_CMD_BD_PUREGEO_TESTS`, `TEST_DOMAIN_UOW` and `CONTRACT_CORPUS` may
  be `skipped` when `bazel-coverage` reported `pr_lanes=true`.
- In the baseline aggregate, `BAZEL_COVERAGE` must be `success`;
  `BAZEL_EMBEDDED_RETIRED` is red when `embedded=true` but the Bazel embedded
  lane did not run remotely and pass, `BAZEL_DOLT_SERVER_RETIRED` when
  `dolt_server=true` but the proxied and server-storage lanes did not, and
  `BAZEL_PR_LANES_RETIRED` when `pr_lanes=true` but the test, pure-Go and
  dolt-server lanes did not.
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

The current embedded Dolt topology already fits the required-check model:

- `detect-ci-tier` always runs.
- `build-embedded`, `test-embedded-storage`, `test-embedded-conformance` and
  `test-embedded-cmd` use job-level `if`; the three test jobs (and, with the
  proxied and server jobs, `build-embedded`) also stand down where the Bazel
  lanes cover them (next section).
- `.github/scripts/ci-embedded-tier.sh` runs full embedded coverage for
  `push`, `merge_group`, unavailable PR diff bounds, and risky paths.
- Docs-only PRs can skip the embedded matrix without leaving the required gate
  pending, because the aggregate job still runs.

### Legacy Tier Retirement (D2)

D2 retires legacy test jobs on same-repo PRs, one step at a time, where
`pr.yml`'s gated Bazel lanes run the same tests: `pr-risk.yml`'s Dolt tiers
(steps 1 and 2) and `pr.yml`'s own Go test jobs (step 3):

<!-- markdownlint-disable MD013 -->

| Step | Legacy jobs | Bazel lanes (`bazel.yml`) | Flag | `pr.yml` gate id |
| --- | --- | --- | --- | --- |
| 1 | `test-embedded-storage` x5, `test-embedded-conformance` x2, `test-embedded-cmd` x20 | `bazel-embedded` (`--config=embedded`) | `BAZEL_RETIRES_LEGACY_EMBEDDED` | `BAZEL_EMBEDDED_RETIRED` |
| 2 | `test-proxied-cmd` x15, `test-server-storage`, `test-server-storage-full` x16 | `bazel-proxied` (`--config=doltserver-proxied`), `bazel-server-storage` (`--config=doltserver-integration`) | `BAZEL_RETIRES_LEGACY_DOLT_SERVER_TIERS` | `BAZEL_DOLT_SERVER_RETIRED` |
| 3 | `pr.yml`: `pr-core-wrapper` (PR Core), `build-artifacts`, `check-cmd-bd-puregeo-tests` (pure-Go and js/wasm), `test-domain-uow`, `contract-corpus` | `bazel-test` (`--config=ci`, which also publishes `bazel-ci-build-artifacts`), `bazel-pure` (`--config=pure`, `--config=js-wasm`), `bazel-doltserver` (`--config=doltserver`) | `BAZEL_RETIRES_LEGACY_PR_LANES` | `BAZEL_PR_LANES_RETIRED` |

<!-- markdownlint-enable MD013 -->

`build-embedded`, whose `embedded-test-binaries` artifact feeds exactly
those six legacy jobs and nothing else, also stands down where both steps
apply. The Bazel lanes run the same tests with the same shard scripts and
manifests. On those PRs they are the tiers' only pre-merge run, and
`CI Gate / Required` requires them to have run remotely and passed.

- Step 3 specifics (`pr.yml`'s own jobs, so the legacy jobs and their Bazel
  lanes run in the same `pr.yml` run):
  - The five jobs add `needs: bazel-coverage` and
    `if: needs.bazel-coverage.outputs.pr_lanes != 'true'`; `pr.yml`'s gate
    accepts their skips only when `pr_lanes` is exactly `true`, and then
    requires `BAZEL_TEST`, `BAZEL_PURE` and `BAZEL_DOLTSERVER` to have run
    remotely and passed (`BAZEL_PR_LANES_RETIRED`). `pr-risk.yml` commits
    the flag too, only so the shared `bazel-coverage` job stays identical;
    nothing in PR Risk reads `pr_lanes`.
  - Artifact consumers: `build-artifacts`' `ci-build-artifacts` fed PR Core,
    domain+uow and (before F3) the package gates; all three now stand down
    with it. No other job in any workflow reads `pr.yml`'s artifacts
    (`docs-autofix.yml` reads `check-doc-flags`'
    `cli-docs-freshness-patch`, which is unaffected).
  - F3: the package gates (`package-mcp`, `package-npm`) moved into
    `bazel.yml` itself, behind the caller input `package-gates` (`pr.yml`
    passes `"on"`; `bazel-farm.yml`/`nightly.yml` keep the default `"off"`).
    They need only the `rbe` job, not `bazel-coverage`, `build-artifacts` or
    the rest of the `bazel` call, and no longer download an artifact: on a
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
    `go build` bd. Both run on `blacksmith-4vcpu-ubuntu-2404` when
    `rbe.outputs.enabled == 'true'` (4 vCPU: `pytest -n 8` is pinned to
    timing measured there), `ubuntu-latest` otherwise. `bazel-test`'s own
    `bazel-ci-build-artifacts` upload is no longer consumed by anything; it
    is kept for the F3.5.3 SHA256SUMS comparison and for debugging.
  - Kept on every PR: the Dolt server fingerprint (container image vs the
    pinned dolt CLI the Bazel dolt-server lanes start), formerly
    `test-domain-uow`'s first step, is its own required job
    `test-dolt-server-fingerprint` (`Test (Dolt server fingerprint)`,
    `TEST_DOLT_SERVER_FINGERPRINT`). `check-release-target-cross-compilation`
    still `go build`s `./...` with `CGO_ENABLED=0` on every PR.
  - Also kept on every PR, in the required job `scripts-go-checks`
    (`SCRIPTS_GO_CHECKS`; PR Core's environment: dolt, git and dolt
    identity, `scripts/ci/lib/test-env.sh`):
    - `go test ./scripts/...` with PR Core's flags
      (`scripts/ci/scripts-go-test.sh`). The repository policy tests,
      including the D2 guards, check part or all of their rules under
      `go test` only (their inputs are not in `//scripts:scripts_test`'s
      runfiles), so without this they would have no required pre-merge run
      on covered PRs.
    - `go test`'s own vet checks (cmd/go's `defaultVetFlags`, policy-tested
      equal to the toolchain's) over `./...` (`scripts/ci/go-test-vet.sh`):
      rules_go's `go_test` runs no vet, so a `go test` vet finding would
      otherwise first fail on `main` and then on every fork PR.
    - the Go tests `bazel test --config=ci` does not run or skips
      (`tools/bazel/equivalence_allowlist.txt`), under `go test`
      (`scripts/ci/allowlisted-go-tests.sh`); each entry must match a test
      that ran and passed.

    `TestBazelOnlySkipsAreAllowlisted` (itself go-test-only, so in that
    job) requires every top-level test with a `TEST_SRCDIR`- or
    `bazeltest.IsBazel()`-guarded `t.Skip` to have an allowlist `skip`
    entry, and every test that runs part of its checks under `go test`
    only to live under `./scripts`.
  - Package gates on a covered PR in a non-remote mode (the farm switch off)
    fail in their own "Check the Bazel-built bd exists" step, naming
    `BAZEL_PR_LANES_RETIRED`, instead of on a missing artifact.
  - Every artifact `bazel.yml` uploads sets `overwrite: true`, so
    "Re-run failed jobs" of a lane (the recovery for an eviction or a flake)
    does not fail on the upload with a 409 (policy-tested).
  - PR Core's other work: `scripts/ci/pr-core.sh` is the one `go test` (plus
    a timing summary); its `BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION=1` is
    `test:prcore`'s too. `bazel-test`'s equivalence step compares Bazel's
    tests with `go list`, not with PR Core's run, so it is unaffected;
    `nightly.yml` still runs PR Core's `go test -json` for the skip-parity
    check.
  - `--config=sole-run` (`--experimental_remote_cache_eviction_retries=0`,
    the step 1 and 2 hardening) is added to every `bazel test` of the three
    lanes wherever they execute remotely (`BAZEL_SOLE_RUN`: modes `remote`,
    `fork-ro` and `fork-rw`), which every covered PR runs in. It used to
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
- Switch: one committed workflow env flag per step (table above), each set
  to the same literal (`"true"` or `"false"`) in `pr.yml` and `pr-risk.yml`
  (policy-tested). They are deliberately not the `RBE_WEST_WORKERS` repo
  variable. A variable is read again by every run and re-run, and the two
  workflows are separate runs, so one could see it on and the other off;
  see "Why a committed flag" below.
- Who: `pull_request` runs from same-repo branches (not forks) whose
  `github.actor` is not `dependabot[bot]`, for each step whose flag is
  `"true"`. The executor secret is deliberately not part of the decision: a
  same-repo PR whose run lacks it (secret deleted or emptied) is still
  covered, takes Bazel mode `cache` (no embedded, proxied or server-storage
  lane) and so turns
  `CI Gate / Required` red, instead of quietly moving back to a legacy tier
  that one of its runs may already have skipped.
  Fork and Dependabot PRs too, but only while the committed
  `BAZEL_COVERS_FORKS` flag (the same literal in both workflows,
  policy-tested) is `"true"`: their lanes then run remotely through
  rbe-fork (modes `fork-ro`/`fork-rw`), and a run rbe-fork does not serve
  (mode `cache`) turns `CI Gate / Required` red rather than falling back.
  It is `"true"` (ga-96smfk.15): fork and Dependabot PRs run only the Bazel
  lanes for the retired tiers.
- Everyone else keeps the legacy tiers unchanged:
  - fork PRs, while `BAZEL_COVERS_FORKS` is `"false"` (their Bazel lanes
    run beside the legacy tiers: remotely while rbe-fork is open, else in
    mode `cache`);
  - Dependabot PRs, likewise (no Actions secrets; `github.actor`, unlike
    `github.triggering_actor`, stays `dependabot[bot]` when someone else
    re-runs them);
  - every PR while that step's flag is `"false"`.

  Every `merge_group` run (merge queue) is covered like a same-repo PR: it
  runs on this repository's `gh-readonly-queue/*` branch with the CI
  secrets, its actor is `github-merge-queue[bot]`, and `bazel.yml`'s `rbe`
  job takes mode `remote` for it, so the Bazel lanes are the queue entry's
  only run of each retired tier and `CI Gate / Required` is red unless they
  ran remotely and passed (see [Merge Queue](#merge-queue)).

  On push to `main`, `bazel.yml`'s push run is the only run of these tiers
  (`main.yml`'s legacy embedded, proxied, integration, domain+uow and Linux
  unit jobs were removed, ga-96smfk.14).
- How:
  - `pr-risk.yml` and `pr.yml` each run the identical `bazel-coverage` job
    (policy-tested). The job does no checkout and runs no repository code.
    It reads the flags, the event, the fork flag and the actor (no secret,
    no variable), and outputs `embedded` (step 1), `dolt_server`
    (step 2) and `pr_lanes` (step 3).
  - Each legacy test job adds
    `needs.bazel-coverage.outputs.<its step's output> != 'true'` to its
    `if`; `build-embedded` adds
    `(embedded != 'true' || dolt_server != 'true')`.
  - PR Risk's gate accepts a legacy test job's skip only when its step's
    output is `true`, and `BUILD_EMBEDDED`'s only when both are.
  - `pr.yml`'s gate requires `BAZEL_COVERAGE` (the job's result) and one
    `BAZEL_*_RETIRED` id per step. Such an id is red when its step's output
    is `true` and the Bazel call's mode is not `remote` or any of the step's
    lanes (`BAZEL_EMBEDDED`; `BAZEL_PROXIED` and `BAZEL_SERVER_STORAGE`;
    `BAZEL_TEST`, `BAZEL_PURE` and `BAZEL_DOLTSERVER`) is not `success`. In particular, mode `skip` (the farm switch off) and
    mode `cache` (the executor secret missing) are red there, although
    `bazel-gate.sh` alone would accept them.
  - A failed, cancelled or missing decision is red in both gates.
- Why a committed flag:
  - The outputs depend on nothing a re-run can change (flags, event, fork,
    actor). So any run or re-run of PR Risk that skips a legacy tier is
    matched by `pr.yml` runs that compute the same outputs.
  - Each such `pr.yml` run is green only if that tier's Bazel lanes ran
    remotely and passed in that run.
  - Flipping `RBE_WEST_WORKERS` either way, plus "Re-run all jobs" of
    either workflow, can no longer leave both required checks green with
    neither the legacy tier nor its lanes run.
  - `scripts/pr_risk_bazel_coverage_test.go` checks this by running both
    workflows' actual decision steps, `bazel.yml`'s `rbe` step and `pr.yml`'s
    actual gate step over every event, variable value, secret, fork, actor
    and flag combination, with the variable and the secret each differing
    between the two runs. It also requires the two workflows to share their
    `pull_request` triggers, and simulates PR Risk's actual gate step over
    every risk tier and decision.
- Cost of failing closed: while a flag is `"true"`, a same-repo PR without
  the executor secret (secret deleted or emptied, or a non-Dependabot bot
  whose runs get no secrets) is red until the secret is restored or the
  flag is committed `"false"`.
- Revert (per step):
  1. Commit that step's flag `"false"` in both workflows on `main`. To turn
     remote execution off, commit both flags `"false"`.
  2. Every PR needs a new push or a merge of `main` to pick it up: a
     pull_request run uses the workflow files of the PR's merge commit, and
     a re-run reuses that commit.
  3. Only then unset `RBE_WEST_WORKERS`, if remote execution should be off.

  Reverting the D2 commits removes the decision job entirely.
- Re-runs: "Re-run all jobs" (or "Re-run failed jobs") of one workflow
  re-evaluates variables and secrets for that workflow's run only. The
  other workflow's last result stays on the head SHA. With the committed
  flags this cannot drop a tier silently: the outputs depend on the flags
  (fixed by the merge commit), the event, the fork flag and the actor, and
  on no variable or secret. Re-running "failed jobs" keeps the earlier
  jobs' outputs, including the decision and the `rbe` mode.
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
    "Test result caching"). Only `test:docker` and `test:fresh` set result
    caching in `.bazelrc`.
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
    - every Bazel shard of `//cmd/bd:bd_embedded_test` (50; PR Risk's own
      legacy `test-embedded-cmd` fork/push jobs still run 20 shards of
      their own, frozen manifest block — a different split, not this one,
      F1), `//internal/storage/embeddeddolt:embeddeddolt_embedded_test`
      (15; legacy `test-embedded-storage` still runs 5 of its own, same
      reasoning, F1), `//cmd/bd:bd_proxied_test` (30; PR Risk's own legacy
      `test-proxied-cmd` fork/push jobs still run 15 shards of their own,
      frozen manifest block — a different split, not this one, F2) and
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
- Not changed: `conformance.yml`'s Tier 1 (`scripts/conformance.sh`) runs the
  embedded-Dolt `TestConformance` again (non-race, unsharded), duplicating
  `test-embedded-conformance` and the Bazel lane. It is not part of either
  required gate; retiring it is a separate decision.

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
  `check-doc-flags` (4 vCPU), `pr-policy-wrapper` (4 vCPU),
  `test-dolt-server-fingerprint`; pr-risk.yml's `test-nix` (4 vCPU). Every
  other job keeps the default 2 vCPU label. Forks and Dependabot PRs fall back
  to `ubuntu-latest`, as F3's `bazel-coverage`/`ci-gate`/`detect-ci-tier` jobs
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
  log instead of needing a per-target re-run.
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

### Same-Repo Blacksmith macOS Legs

pr.yml's two mixed-OS matrix jobs, `pr-preflight-platforms` and
`check-doc-freshness-platforms`, run their macOS leg on
`blacksmith-6vcpu-macos-26` (Apple Silicon) for same-repo PRs and
merge_group, through the same `runner: same-repo-macos` matrix marker and
same-repo expression their Linux/Windows legs use. Forks, Dependabot and
every other event keep GitHub-hosted `macos-latest`. They are the only PR
macOS jobs; `release.yml`, `nightly.yml` and `ci-measurements.yml` stay on
`macos-latest`.

- **Label.** Pinned to `macos-26` because GitHub's `macos-latest` resolves to
  `macos-26-arm64` today, so both paths run the same OS and architecture and
  the PR legs and their saver never straddle a Blacksmith `-latest` alias
  move. Bump it when GitHub moves `macos-latest`. 6 vCPU is already twice
  `macos-latest`'s 3 vCPU; 12 vCPU is not justified for legs dominated by one
  incremental `./cmd/bd` test compile.
- **Caches.** The legs stay restore-only (`setup-go` `cache: false`,
  `actions/cache/restore`). Blacksmith cannot see GitHub-saved caches, so
  main.yml's `blacksmith-macos-go-build-cache` job is their seeder: same
  label, push-to-main-only job guard, module cache plus a non-race GOCACHE
  keyed by `go.sum` and UTC day, warmed by the shared
  `scripts/ci/warm-non-race-cache.sh`. Its `github` venue leg seeds the
  fork path (`macos-latest`) the same way; main.yml's `test` job (the macOS
  full suite) runs on the same Blacksmith label and restores the Blacksmith
  leg's caches.
- **Pins.** `TestSameRepoPlatformsMatrixMarkerRunsOnExpressionSemantics`,
  `TestBlacksmithMacOSSaverMatchesPRLegs`,
  `TestBlacksmithSaverJobsGuardedAgainstPullRequest`,
  `TestBlacksmithSaverCacheKeysAreNotPerCommit`,
  `TestBlacksmithReachablePRJobsDisableDefaultCachingActions` and
  `TestGoCacheOwnershipTopology`.

### Server Dolt Storage Matrix

`test-server-storage-full` mirrors `test-embedded-storage`'s sharding, one
tier down in the same workflow:

- Job-level `if` uses the same `detect-ci-tier` gate as the embedded matrix,
  and both server jobs stand down where the Bazel `server-Dolt storage tier`
  lane covers them (D2 step 2, [Legacy Tier Retirement](#legacy-tier-retirement-d2)).
- `.github/scripts/server-storage-test-shard.sh` discovers top-level
  `Test*` functions from `internal/storage/dolt/*_test.go` (excluding
  `TestConformance`, which keeps its own `test-server-storage` job), assigns
  known-heavy tests via the committed
  `.github/scripts/server-storage-test-shards.txt` manifest, and
  hash-distributes everything else — the same manifest-plus-fallback
  mechanism `embedded-storage-test-shard.sh` uses.
- 16 shards (vs. embedded's 5): server-mode tests are real socket
  round-trips against a containerized Dolt server with a per-test
  CREATE/DROP DATABASE, and the package has 3.5x as many top-level tests
  (1126 vs. 324) as the embedded suite. An earlier unsharded single job
  (15m Go timeout, `timeout-minutes: 20`) never finished — it died at
  256/1126 tests with zero failures, just out of time. One test,
  `TestCloudAuthCLIRouting`, alone costs ~9.5 minutes and is pinned alone
  on shard 1 in the manifest so it cannot delay any other shard.
- `fail-fast: false`, matching every other matrix job in this workflow: one
  slow or flaky shard should not cancel its siblings.

### Regression Tests

`Regression Tests` can stay visible as a non-required workflow. If regression
becomes branch-protection relevant, do not require
`Differential Regression (v0.49.6 baseline)` directly.

Use one of these narrow changes instead:

1. Move the regression detector and regression job into the required PR
   topology, wire them into the relevant aggregate gate, and add `merge_group`
   behavior that defaults to running regression.
2. Keep `regression.yml` separate, remove any workflow-level skip filters, add
   `merge_group`, add a final `Regression Gate / Informational` aggregate, and
   leave it non-required unless branch protection is intentionally expanded.

The preferred required-check topology keeps only aggregate gates required.

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

`Cross-Version Smoke Tests` should remain non-required for ordinary PRs unless
maintainers explicitly choose to pay that cost in the aggregate gate. If it
becomes required, add `merge_group` and put it behind a detector plus aggregate
inside the required topology. Do not require matrix-expanded
`Upgrade smoke (chunk N)` jobs directly (F7c folded the old one-job-per-version
matrix into one job per 5-version chunk; the per-chunk job name changed but
the "do not require individually" guidance is unchanged).

## Merge Queue

`pr.yml` and `pr-risk.yml` run on `merge_group` (`types: [
checks_requested ]`) and report `CI Gate / Required` and `PR Risk Gate /
Required` on each queue entry's merge-group commit, so the ruleset's two
required contexts work unchanged once the queue rule is on.
`scripts/ci_merge_queue_test.go` simulates a `merge_group` run through the
shared `evalGHExpr` evaluator and the workflows' own step scripts.

### Queueing a PR

```bash
gh pr merge <number> --auto --squash --repo gastownhall/beads
```

On a green PR this adds it to the queue; while the PR's own required
checks are still running, `--auto` queues it once they pass (this needs
the repository's "Allow auto-merge" setting). The queue's own merge
method (squash) decides how it lands. `gh pr merge <number>
--disable-auto` (or "Remove from queue" in the UI) takes it out; `--admin`
(bypass actors only) merges directly, skipping the queue.

Planned queue settings (`merge_queue` rule in the `beads main: required
CI gates` ruleset): merge method `SQUASH`, grouping strategy `ALLGREEN`,
`max_entries_to_merge` 5, `max_entries_to_build` 3, `min_entries_to_merge`
1, `min_entries_to_merge_wait_minutes` 2 (it only delays merging while
fewer than `min_entries_to_merge` entries are queued, so at 1 it never
waits), `check_response_timeout_minutes` 60. A same-repo `pr.yml` run takes
about 10-11 minutes today. The build limit (3) and the timeout (60) leave
headroom while the farm scales from zero, so a backlog does not time
entries out and set off a rebuild cascade. Watch the first days'
`merge_group` durations before raising the build limit. The required status checks
stay exactly `CI Gate / Required` and `PR Risk Gate / Required` (GitHub
Actions).

### What runs on a merge group

Each queue entry gets a commit on `gh-readonly-queue/main/pr-<n>-<sha>`:
`main` plus every PR ahead of it in the queue plus this PR. Both
workflows run on that commit like a same-repo PR:

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
- D2: `bazel-coverage` covers every merge group, so the retired legacy
  tiers (embedded, proxied/server Dolt, pr.yml's PR Core/build/pure-Go/
  domain+uow/contract jobs) stay retired and the gate requires their
  Bazel lanes to have run remotely and passed. With the farm switch off
  (mode `skip`) or a missing executor secret (mode `cache`) the gate is
  red, exactly as on a same-repo PR: turn the flags off first (runbook in
  [Current State](#current-state)).
- `bazel-cmd-dolt` (required while `BAZEL_CMD_DOLT_REQUIRED` is `"true"`),
  `bazel-integration`, the package gates and the F4 prebuilt Windows pair
  run as on a PR. `detect-ci-tier` reports full embedded coverage (moot
  while the tiers are retired).
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
  (dolt and every input are runfiles). The docker-backed lane, whose
  result depends on host state outside the key, keeps
  `--nocache_test_results` and is dispatch-only.
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

Non-Bazel required jobs re-run in full on every merge group (approximate
PR timings, 2026-10): `fast-checks` (~40 s), `pr-policy-wrapper`
(~2.5 min), `scripts-go-checks` (3 legs, up to ~3.5 min), `pr-lint-wrapper`
(native/darwin/windows, up to ~4 min), `check-doc-flags` (~1.7 min),
`check-doc-freshness-platforms` and `pr-preflight-platforms` (Linux,
Windows and macOS legs, up to ~5 min on Windows),
`check-release-target-cross-compilation` (~5 min),
`test-dolt-server-fingerprint` (~1 min), `windows-make-shell` (~2.7 min),
`windows-test-binaries` plus the prebuilt Windows pair (~7 min end to
end), the advisory native Windows pair (~5 min; the gate waits for them),
`test-nix` (~3 min) and the package gates (seconds unless their paths
changed). None caches results the way Bazel does; most are cheap or
already path- or tier-gated. Candidates to skip on `merge_group`, not
done here: the advisory native Windows pair (`test-windows-liveness`,
`worktree-remove-windows`, advisory while `WINDOWS_PREBUILT_REQUIRED` is
`"true"`), and the macOS legs of the preflight/doc-freshness matrices
(platform behavior the PR run already checked on the same inputs). With
result reuse the merge group's critical path is the Windows
cross-compile and the Windows matrix legs (~5-7 min), not the Bazel
lanes once their tests are cached. An entry's first build after a base
change still compiles what changed.

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
both gates normally finish in about 10-15 minutes.

Rollback: delete the `merge_queue` rule from the ruleset. Queued PRs fall
back to the normal merge button; the `merge_group` triggers are inert
without a queue.

Policy for `merge_group`:

- `PR` and `PR Risk` must include `merge_group` (`checks_requested`), and
  no job or step condition in them may key on the event: decisions belong
  in detector jobs (`TestMergeQueueRequiredGatesRunOnMergeGroup`).
- Any risk detector added to the required topology should default to run on
  `merge_group`, because the merge group commit may combine individually safe
  PRs into a risky integration state.
- The aggregate gates evaluate merge group results exactly like PR results;
  no skip is excused for `merge_group` alone.

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

1. **F3's gate runners** — pr.yml's and pr-risk.yml's `bazel-coverage`,
   `ci-gate` and `detect-ci-tier` jobs.
2. **bazel.yml's `rbe` job** runner.
3. **F7a** — pr.yml's `fast-checks`, `advisory-reports`,
   `check-release-target-cross-compilation`, `check-doc-flags`,
   `pr-policy-wrapper`, `test-dolt-server-fingerprint`; pr-risk.yml's
   `test-nix`.
4. **F7b** (cache-dependent moves, once that slice lands) — main.yml's
   Blacksmith cache seeds and the `scripts-go-checks`/`pr-lint-wrapper`/
   preflight/doc-freshness runner moves, including the preflight/
   doc-freshness macOS legs and `blacksmith-macos-go-build-cache`
   ([Same-Repo Blacksmith macOS Legs](#same-repo-blacksmith-macos-legs)).

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
