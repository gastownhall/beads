# Required Check Topology

Status: initial aggregate gate jobs implemented on branch
`ci/bd-am3.1-wrapper-commands`. Do not change branch protection until the new
checks have appeared and passed on at least one recent commit.

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
  that consume the Linux artifact, focused storage domain/uow coverage, and
  the baseline aggregate gate `PR / CI Gate / Required`.
- `.github/workflows/pr-risk.yml`: `PR Risk`
  Runs on `pull_request` and `merge_group`. Contains embedded Dolt risk
  detection, embedded build/test shards, the Nix flake smoke check, and the
  risk aggregate gate `PR Risk / CI Gate / Required`.
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
protection on the default branch. It does not currently require status checks.

## Required Check Contract

After the aggregate checks are verified on the branch, branch protection or the
default-branch ruleset should require stable aggregate GitHub Actions checks
from unfiltered workflows. The original single-check proposal assumed all PR
jobs lived in one workflow; after the workflow split, a single in-workflow
aggregate can only cover jobs in that same workflow. The implemented first
rollout uses one aggregate per required workflow. An external status aggregator
would only be needed if maintainers still want exactly one required check.

- Baseline aggregate candidate: `PR / CI Gate / Required`
- Risk aggregate candidate: `PR Risk / CI Gate / Required`
- Source: GitHub Actions
- Required on: pull requests and merge queue groups targeting `main`

Do not require these existing check names directly:

- `Detect CI tier`
- `Check build-tag policy`
- `Check cmd/bd pure-Go tests compile (CGO_ENABLED=0)`
- `Check version consistency`
- `Check doc flags freshness`
- `Check for .beads changes`
- `Test (ubuntu-latest)`
- `Test (macos-latest)`
- `Test (storage domain + uow)`
- `Build (Embedded Dolt)`
- `Test (Embedded Dolt Storage)`
- `Test (Embedded Dolt Cmd 1/20)` through `Test (Embedded Dolt Cmd 20/20)`
- `Test (Windows - smoke)`
- `Check formatting`
- `Lint`
- `Test Nix Flake`
- `Differential Regression (v0.49.6 baseline)`
- `Upgrade smoke (<version> -> candidate)`
- `Resolve versions to test`
- `nix build .#default`

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

`.github/workflows/pr.yml` now has one final baseline gate job:

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

## Rollout Steps

1. Add `.github/scripts/ci-gate.sh` and aggregate gate jobs to the required PR
   workflows. Initial implementation exists on branch
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
