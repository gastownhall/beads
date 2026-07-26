# Contributor PR Guidelines

This is the source of truth for agents and humans filing pull requests to beads.

## Philosophy

beads is small and clean. PRs that respect the existing layering land fastest. The codebase has clear layers — schema, storage/issueops, application (cmd/bd) — and the reviewer optimizes for keeping those layers clean and the diff reviewable. The most common rejection reasons are fixable before opening a PR.

## Before Opening a PR

**Prove the bug in beads itself.** Include a beads-only minimal repro test or benchmark that demonstrates the bug or performance regression exists with stock beads and no orchestrator (no gascity, no factory, no external agent framework). Without this, the PR will likely be rejected as an orchestrator bug.

**Verify with stock beads.** Before claiming any bug, reproduce it with no orchestrator running. Many apparent beads bugs turn out to be orchestrator-layer issues.

**Add benchmarks for performance claims.** If your PR claims a speedup, include a bench test that shows it. The reviewer will not assume the new code is faster.

## Sizing and Scoping

**One layer per PR.** If a fix needs changes at both the storage/issueops layer and the application (cmd/bd) layer, file them as separate stacked PRs: the storage primitive first, then the application wiring on top.

**Stack when a fix spans layers.** A PR that touches issueops primitives AND cmd/bd code at the same time is hard to review. Split it. The primitive PR unblocks the application PR.

**Keep diffs reviewable.** If your PR is over a few hundred lines, look for ways to split it. A 13K-line diff will not be reviewed.

## Layering Rules

**Primitives at the lowest layer first.** New capabilities belong at storage/issueops before the application calls them. If you are adding a new query, add it to issueops first, expose it on the storage.Storage interface, and implement it there. The application layer (cmd/bd) then calls the new method without reaching across layers itself.

**Do not reach across layers from above.** If cmd/bd is doing SQL-like work that belongs in issueops, that is a sign the abstraction boundary is wrong. File a storage primitive PR first.

## Code Style

**Minimize inline code comments.** The reviewer routinely asks for inline comments to be removed. Let the commit message and PR description carry the why. Use comments only for non-obvious invariants that cannot be expressed in the code itself.

**Commit messages over inline comments.** If you feel the need to explain a block of code with a comment, write that explanation in the commit message instead.

## When in Doubt

Read PR_MAINTAINER_GUIDELINES.md to understand what the maintainer optimizes for — it explains the review philosophy, triage groups, and outcome types. Understanding the maintainer perspective makes it easier to anticipate feedback before opening a PR.
