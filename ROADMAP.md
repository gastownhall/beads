# Beads Roadmap

Last updated: 2026-10-05 · Owner: [@julianknutsen](https://github.com/julianknutsen)

This page lists the areas the maintainers are prioritizing. It reflects
current thinking, not commitments or dates: priorities change as we learn,
and the live state of each item is its tracking issue on GitHub.

## How this roadmap is used

- **Priority areas get expedited review.** Issues and PRs that advance a
  priority area below are triaged and reviewed first.
- **Propose work with an issue when it warrants discussion.** For work
  inside or outside a priority area that needs context or a design decision,
  open an issue with the feature form, before or alongside your PR; small,
  self-explanatory changes can go straight to a PR. See
  [CONTRIBUTING.md](CONTRIBUTING.md#issues-and-pull-requests).
- **Scope.** Proposals must fit the product boundary in
  [engdocs/PROJECT_CHARTER.md](engdocs/PROJECT_CHARTER.md).
- **Updates.** The owner rewrites this page at each minor release; the date
  above says when it was last reviewed.

## Priority areas for 1.4

### Versioned beads and history

- **Goal:** every change to a bead is recorded as a version. Users can read
  a bead's history and past versions, and writers can use `expectedRevision`
  compare-and-swap to avoid lost updates. Everything ships behind a flag, off
  by default, until the conformance suite and a production-corpus soak say
  otherwise.
- **This period:** all six phases, enabled from the CLI by the end of the
  period: conformance suite
  ([#6133](https://github.com/gastownhall/beads/issues/6133)), additive
  schema ([#6134](https://github.com/gastownhall/beads/issues/6134)),
  dual-write history behind a flag
  ([#6135](https://github.com/gastownhall/beads/issues/6135)), versioned
  reads and CAS ([#6136](https://github.com/gastownhall/beads/issues/6136)),
  production-corpus validation
  ([#6137](https://github.com/gastownhall/beads/issues/6137)), and CLI
  surfacing and enablement
  ([#6138](https://github.com/gastownhall/beads/issues/6138)).
- **Tracking issue:** [#6132](https://github.com/gastownhall/beads/issues/6132);
  design discussion stays on
  [#5898](https://github.com/gastownhall/beads/issues/5898).

### Beads Graph Preview 2

- **Goal:** ship Beads Graph Preview 2, the next public checkpoint for the
  generic Bead-and-Link graph model: a coherent daily-use CLI journey in
  graph mode that behaves like ordinary `bd`, backed by a complete,
  reviewable CLI specification.
- **This period:** the reviewed graph CLI specification, a complete
  daily-use Issue journey proven through the installed CLI on embedded and
  shared-server Dolt, and editing around that Issue (labels, notes, Memory
  and Link edits).
- **Tracking issue:** [#7170](https://github.com/gastownhall/beads/issues/7170)

### Contributor onboarding

- **Goal:** a new contributor, human or agent, finds the rules for the code
  they are changing next to that code, understands its intent and
  invariants, and lands a well-explained PR, backed by an issue when the
  change warrants one.
- **This period:** contributor-only agent instructions and documented-issue
  intake ([#7223](https://github.com/gastownhall/beads/pull/7223));
  per-package `AGENTS.md` files for the storage packages, naming their
  guard tests and lint rules; a package-doc linter and package docs for the
  packages that have none; and a curated set of `help wanted` starter
  issues.
- **Tracking issue:** [#7228](https://github.com/gastownhall/beads/issues/7228)
