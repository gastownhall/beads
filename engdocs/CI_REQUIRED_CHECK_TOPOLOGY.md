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
  decides the execution mode once (`remote`, `local` for fork and Dependabot
  PRs, or `skip` while the `RBE_WEST_WORKERS` repo variable is unset) and
  exports it as the `rbe-mode` / `rbe-enabled` outputs; every lane exports its
  `job.status` as an output named after the job. `pr.yml`'s gate requires the
  call's result (`BAZEL`) and `BAZEL_TEST`, `BAZEL_PURE`, `BAZEL_EMBEDDED`,
  `BAZEL_DOLTSERVER`, `BAZEL_PROXIED` and `BAZEL_SERVER_STORAGE`. The legacy jobs these mirror stay required in `pr.yml`
  and `pr-risk.yml`.
  `.github/scripts/bazel-gate.sh` reads the exported mode, never the variable
  or the fork flag: mode `skip` allows every Bazel id to skip, mode `local`
  allows only the remote-only `BAZEL_EMBEDDED`, `BAZEL_PROXIED` and
  `BAZEL_SERVER_STORAGE` (fork and Dependabot PRs rely on `pr-risk.yml`'s
  legacy tiers for those), and mode `remote` allows none.
  A lane that should run and fails, is cancelled, or reports no result fails
  the gate, and so does a missing or invalid mode.
  `bazel-integration` is not part of the PR call (`pr.yml` passes
  `integration: "off"`) and has no gate id: its legacy counterparts
  (`main.yml`'s integration jobs) run only on push to `main`, and so does it
  (plus dispatch and nightly). `scripts/ci_workflow_test.go` fails when a new
  `bazel.yml` job has neither a gate id nor an advisory entry.
  Runbook: if `main`'s Bazel BUILD files drift (every PR's Bazel lanes go
  red, and autofix only patches packages the PR itself changed), the RBE
  farm is down, or the beads CI RBE client certificate expires (about
  2027-09-27; a partial secret set also fails every same-repo run), fix
  `main` (`make bazel-sync`) or renew the secrets, or unset the
  `RBE_WEST_WORKERS` repo variable: same-repo runs then take mode `skip`,
  which the gate accepts (fork PRs still run locally, so drift on `main`
  still reaches them).
- `.github/workflows/bazel-farm.yml`: `Bazel Farm (trusted forks)`
  Runs on `pull_request_target` for fork PRs to `main` whose author and
  triggering user are on `.github/bazel-farm-allowlist.txt`, and calls
  `bazel.yml` with the RBE secrets and the PR's pinned head SHA. Advisory:
  no ruleset requires it and nothing reads its results; see
  [Trusted-Author Fork PRs](#trusted-author-fork-prs-bazel-farm).
- `.github/workflows/pr-risk.yml`: `PR Risk`
  Runs on `pull_request` and `merge_group`. Contains embedded Dolt risk
  detection, embedded build/test shards, the Nix flake smoke check, and the
  risk aggregate gate `PR Risk / PR Risk Gate / Required`.
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
may bypass it. There is no merge queue, so the checks run on
`pull_request`.

## Trusted-Author Fork PRs (Bazel Farm)

Fork PRs get no Actions secrets, so `pr.yml`'s Bazel call runs them in mode
`local` (no remote-only lanes, slower). `bazel-farm.yml` gives the
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
  `integration: "off"`. `bazel.yml` checks out that SHA in every lane with
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
  it honours the read-only token. The canary run in the rollout notes
  (`~/beads-bazel-plan/vip-forks-design.md`) or an answer from Blacksmith
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
  local even if the fork's `pr.yml` passes `fork-farm`, because the event is
  not `pull_request_target`.

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
- Required on: pull requests and merge queue groups targeting `main`

Do not require these existing check names directly:

- `Detect CI tier`
- `Check build-tag policy`
- `Check pure-Go and js/wasm boundaries (CGO_ENABLED=0)`
- `Check version consistency`
- `Check doc flags freshness`
- `Check for .beads changes`
- `Test (ubuntu-latest)`
- `Test (macos-latest)`
- `Test (storage domain + uow)`
- `Build (Embedded Dolt)`
- `Test (Embedded Dolt Storage 1/5)` through `Test (Embedded Dolt Storage 5/5)`
- `Test (Embedded Dolt Cmd 1/20)` through `Test (Embedded Dolt Cmd 20/20)`
- `Test (Windows - smoke)`
- `Check formatting`
- `Lint`
- `Test Nix Flake`
- `Differential Regression (v0.49.6 baseline)`
- `Upgrade smoke (<version> -> candidate)`
- `Resolve versions to test`
- `nix build .#default`
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
`build-embedded`, `test-embedded-storage`, `test-embedded-cmd`, and `test-nix`.

`.github/scripts/ci-gate.sh` is a small shell evaluator. It fails on any
`failure` or `cancelled` result. It accepts `skipped` only for jobs that are
intentionally absent for that event or risk tier:

- `CHECK_NO_BEADS_CHANGES=skipped` is acceptable on `merge_group` because the
  job is PR-only.
- In the risk aggregate, `BUILD_EMBEDDED`, `TEST_EMBEDDED_STORAGE`, and
  `TEST_EMBEDDED_CMD` may be `skipped` only when `FULL_EMBEDDED != true`.
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
- `build-embedded`, `test-embedded-storage`, and `test-embedded-cmd` use
  job-level `if`.
- `.github/scripts/ci-embedded-tier.sh` runs full embedded coverage for
  `push`, `merge_group`, unavailable PR diff bounds, and risky paths.
- Docs-only PRs can skip the embedded matrix without leaving the required gate
  pending, because the aggregate job still runs.

### Server Dolt Storage Matrix

`test-server-storage-full` mirrors `test-embedded-storage`'s sharding, one
tier down in the same workflow:

- Job-level `if` uses the same `detect-ci-tier` gate as the embedded matrix.
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

`.github/workflows/nix-build.yml` currently uses workflow-level `paths` filters.
Keep `nix build .#default` non-required.

If the full Nix build must affect mergeability, move it into an unfiltered
required PR workflow behind a detector and job-level `if`, then teach the
aggregate gate when a skipped Nix build is acceptable. Do not make the
path-filtered `nix build` workflow or `nix build .#default` job directly
required.

### Cross-Version Smoke

`Cross-Version Smoke Tests` should remain non-required for ordinary PRs unless
maintainers explicitly choose to pay that cost in the aggregate gate. If it
becomes required, add `merge_group` and put it behind a detector plus aggregate
inside the required topology. Do not require matrix-expanded
`Upgrade smoke (<version> -> candidate)` jobs directly.

## Merge Queue Behavior

Required aggregate checks must be reported for `merge_group`.
Otherwise, GitHub can enqueue a PR and then fail to merge because the required
check was never reported for the synthetic merge group commit.

Policy for `merge_group`:

- `PR` and `PR Risk` must include `merge_group`.
- `detect-ci-tier` should keep treating `merge_group` as full embedded coverage.
- Any risk detector added to the required topology should default to run on
  `merge_group`, because the merge group commit may combine individually safe
  PRs into a risky integration state.
- The aggregate gate should evaluate the merge group results exactly like PR
  results, except PR-only hygiene checks such as `Check for .beads changes` may
  be skipped by design.

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
