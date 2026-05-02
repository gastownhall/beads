# Maintainer PR Guidelines

This is the source of truth for agents triaging, reviewing, landing, closing, or otherwise maintaining pull requests for beads.

## Philosophy

Help contributors get to the finish line. Optimize for community throughput.

For every PR, look for the value in it and choose the action that moves useful work into the codebase with the least contributor starvation. If a PR contains something worth keeping, absorb that value directly when practical: accept it as-is, fix bugs, improve the architecture, rename things, turn it into a plugin, cherry-pick parts, or reject the parts that do not fit.

The goal is not to block contributors unnecessarily. The goal is to identify useful work, preserve it, and keep the project moving.

## Contributor Protection

External contributor PRs have priority. Before implementing related work, opening a competing PR, or closing a PR, check whether an existing contributor PR already addresses the same area.

- Review contributor work first. Read the PR description, changed files, linked issues, tests, CI status, and latest discussion.
- Build on the contributor branch when practical instead of rewriting the same work in parallel.
- Preserve contributor tests unless they are actually wrong.
- Preserve attribution with original commits when possible, or with `Co-authored-by:` and PR references when transforming the work locally.
- Never close, supersede, or replace a contributor PR silently. Explain what was preserved, what changed, and why.
- If a rewrite is unavoidable, credit the contributor's design, tests, bug report, or use case in the replacement commit or PR.

## Triage Groups

Classify each PR into one of these groups:

- **Easy win**: Targeted bug fixes, documentation updates, dependency bot upgrades, drafts to close, PRs from banned contributors, and other low-risk cases.
- **Fix-merge candidate**: A PR that otherwise fits easy-win criteria but has a simple blocker, such as failed CI, a needed rebase, or a small implementation error.
- **Needs review**: A PR that looks suspicious, complex, broad, risky, or otherwise requires deeper investigation.

Easy wins can be handled automatically during a PR review run and by recurring patrols. Fix-merge candidates can also be handled automatically when the maintainer determines the repair is simple enough to make locally.

Needs-review PRs require a deeper agent review and a concrete report. The maintainer can summarize those reports or inspect the agent sessions directly.

## Outcomes

Use these recommendations after review:

- **Easy win**: The PR turns out to fit easy-win criteria after all.
- **Merge**: Recommend merge. The PR is well-tested, broadly useful, well-documented, and ready as-is.
- **Merge-fix**: Merge the PR as-is, then push a follow-up fix to `main`. Use when the remaining issues are safe to repair afterward.
- **Fix-merge**: Pull the PR locally, make substantial fixes, then push with contributor attribution. Use when the PR is busted but valuable.
- **Cherry-pick**: Keep only selected items from a PR with multiple features or fixes. Commit the useful parts locally with attribution, then close the PR with an explanation.
- **Split-merge**: Split a multi-concern PR into separate commits, then push all accepted parts with attribution to the original contributor.
- **Redesign/reimplement**: Reject the submitted design but solve the underlying problem another way. Close the PR with thanks and an explanation.
- **Retire**: Close an obsolete PR with thanks because it was superseded or already fixed elsewhere.
- **Reject**: Close politely when the feature does not pay its weight in tech debt, is too niche for core, or the design does not meet project standards.
- **Request changes**: Last resort. Avoid this when the maintainer or agents can reasonably absorb, transform, or land the useful parts directly.

Other outcomes are possible, including rerouting a PR to the right project or banning a contributor, but the list above covers the normal cases.

## Operating Rules

- Prefer landing or transforming useful work over asking the contributor to do more rounds.
- Preserve contributor attribution when absorbing, fixing, cherry-picking, splitting, or reimplementing PR value.
- Be explicit when closing a PR: thank the contributor, state the outcome, and explain what was accepted, rejected, superseded, or implemented differently.
- Treat request-changes as exceptional because it can strand contributor work.
- File follow-up work as beads issues instead of hidden notes.
- When code changes result from PR maintenance, follow repo quality gates and session completion rules in `AGENTS.md`.
