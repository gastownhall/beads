# Release Channels

Four channels, modelled on Chrome: **Canary**, **Dev**, **Beta**, **Stable**.
A channel is a branch, and promotion moves a branch pointer.

This exists so that cutting a release is a decision rather than a ceremony. The
work of a release — the suite, the upgrade matrix, the regression baseline, the
package gates, writing the evidence into `release-gates/` — happens on a
schedule, in CI, ahead of anyone being asked anything. What is left for a human
is one page to read and one button to press.

See [RELEASING.md](../RELEASING.md) for the manual process, which is unchanged
and remains correct for hotfixes and out-of-band releases.

## The channels

| Channel | Cut from | Trigger | Version | Published as | Audience |
|---|---|---|---|---|---|
| **Canary** | `main` HEAD | every push to `main` | none | one rolling prerelease, assets replaced | maintainers, bots |
| **Dev** | `main` HEAD | nightly, after the nightly suite is green | `X.Y.Z-dev.<YYYYMMDD>` | GitHub prerelease | contributors |
| **Beta** | a Dev commit | weekly (Mon 03:00 UTC) + dispatch | `X.Y.Z-beta.N` | GitHub prerelease | early adopters, soak |
| **Stable** | a Beta commit | dispatch + approval | `X.Y.Z` | GitHub release → PyPI, npm, homebrew-core | everyone |

Branches: `channel/canary`, `channel/dev`, `channel/beta`, `channel/stable`.

Code flows one way — `main → Dev → Beta → Stable`. **A channel branch never
merges from `main` after it is cut.** Fixes reach Beta by deliberate
cherry-pick, one at a time. That is what makes Stable promotable without
re-litigating everything on `main`, and it means there is always a maintained
stable line to build a hotfix on.

## Why the stable channels are safe

Nothing here changes `release.yml`, because nothing needed to. A tag carrying a
prerelease identifier already publishes a GitHub prerelease and reaches none of
the stable package channels:

- `.goreleaser.yml` sets `prerelease: auto`.
- `publish-pypi` and `publish-npm` are both gated
  `!contains(github.ref_name, '-')`.
- homebrew-core autobumps from non-prerelease GitHub releases only.

So `v1.3.0-dev.20260826` and `v1.3.0-beta.1` publish binaries for people who
want them and cannot touch Homebrew, PyPI or npm. `v1.3.0` does. That
separation is pre-existing and was exercised by `v1.2.2-rc.1`.

## Canary carries no version

A canary is `main`, built. It is not a candidate for anything, so it carries no
version and mutates no version-bearing file; only `channel/canary` moves, and a
single rolling prerelease has its assets replaced in place.

That is also the only option available. A canary identifier is not
representable in PEP 440, so `integrations/beads-mcp` could not carry one:

| Identifier | PEP 440 |
|---|---|
| `1.3.0-dev.20260826` | valid → `1.3.0.dev20260826` |
| `1.3.0-beta.1` | valid → `1.3.0b1` |
| `1.3.0-canary.abc1234` | **invalid** |
| `1.3.0-canary.20260826` | **invalid** — a numeric suffix does not help; PEP 440 admits only `a`/`b`/`rc`/`post`/`dev` labels |

Canary builds on one runner, so it covers linux and windows. The full platform
matrix starts at Dev, which goes through the ordinary tag pipeline.

## One schema step per Beta

`scripts/channel-promote.sh` refuses a Beta whose span carries more than one
migration, overridable with `--allow-multi-migration`.

Migrations are applied forward-only on first access, so the number of steps in
a release is the distance a user is from the last schema their installed binary
can read. One step is recoverable. Twelve is the shape that required
`docs/RECOVERY-1.2.1.md`.

The cost is close to zero: the large majority of merged work touches no
migration at all, so most Betas are unaffected by the rule and the ones that
are should be cut at the boundary anyway.

## Which gates run where

The split is whatever the tag does not already trigger. Nothing is duplicated.

**Pre-tag, in `channel-beta.yml`** — these fire on `push: main` only and would
otherwise never see a candidate:

- `make test` — full suite
- `make test-regression` — differential against the pinned baseline
- `scripts/check-migration-hygiene.sh`
- `make ci-package-mcp`, `make ci-package-npm`

**Post-tag, existing workflows, unchanged:**

- `cross-version-smoke.yml` — the 30-release upgrade matrix, i.e. the
  [Release Stability Gate](RELEASE-STABILITY-GATE.md)
- `migration-test.yml`
- `release.yml` — publishes the prerelease

The pre-tag gates run against the *source* tree, but the Beta commit is that
tree plus a version bump — a different SHA. So `channel-beta.yml` publishes its
verdict as a check run **on the Beta commit** via
`scripts/channel-check-run.py`. Without that, the promotion dossier would read
the Beta commit, miss the pre-tag gates, and report a release as less gated
than it was.

## Promoting to Stable

Run the **Channel — Stable** workflow with the Beta tag. It has two jobs.

`dossier` assembles `release-gates/<tag>.md` in the format this project already
uses, and publishes it as the job summary. It blocks nothing and changes
nothing. Every figure is queried rather than asserted — gate results from the
check runs recorded against the Beta commit, soak from the release's
`published_at`, the shipped-PR manifest and schema delta from git ancestry.

`promote` is gated on the `stable-release` environment. GitHub holds it until a
required reviewer approves and records who did. Only then is anything tagged.

**This does not widen who may release.** `v*` tag creation stays restricted to
release maintainers; the environment's reviewer list is that same set. What
moves is the labour. The job additionally queries the environment and fails
closed if it has no required reviewers, because an unattended stable release is
the outcome this design exists to prevent.

### Three verdicts

| Verdict | Exit | Meaning |
|---|---|---|
| `PASS` | 0 | clean |
| `FAIL` | 2 | an **undeclared** red, or no check runs at all |
| `REVIEW` | 3 | nothing red, but a gate did not reach a verdict |

A cancelled check is not a pass and not a failure. `REVIEW` says so rather than
guessing, and leaves the call to the approver.

Expected reds are declared, not ignored: `--expect-red 'GLOB=reason'` records
the check and the reason in the dossier. This mirrors existing practice — the
`v1.2.2` gate file records a deliberately refused forward-skew as a
"pre-declared expected signature". A declaration is an argument, not an
exemption; if the reason has stopped holding, it is a red again.

## Setup required

The workflows cannot grant themselves these.

1. **`refs/heads/channel/*` writable by `GITHUB_TOKEN`.** All four workflows
   move a channel pointer.
2. **The `v*` tag ruleset must admit the Actions bot** for Dev and Beta tags.
   If you would rather not widen it, run `channel-dev.yml` with `dry_run: true`
   and tag by hand from the prepared `channel/dev` commit — everything up to
   the tag is still automated.
3. **An environment named `stable-release`, with required reviewers** set to the
   release maintainers. Without it `channel-stable.yml` fails closed rather
   than promoting unattended.

## Scripts

| Script | Does |
|---|---|
| `scripts/channel-promote.sh` | version arithmetic and prep for one channel; dry-run by default. Calls `update-versions.sh`, `uv lock`, `check-versions.sh` rather than reimplementing them |
| `scripts/channel-gate-report.sh` | assembles the promotion dossier from live API and git data |
| `scripts/channel-check-run.py` | emits the check-run payload recording a Beta's pre-tag verdict |

None of them tags, pushes, or publishes. That is the workflows' job, and for
Stable it is a human's.
