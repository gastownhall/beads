# Maintainer PR Guidelines

This is the source of truth for agents triaging, reviewing, landing, closing, or otherwise maintaining pull requests for beads.

## Philosophy

Help contributors get to the finish line. Optimize for community throughput.

For every PR, look for the value in it and choose the action that moves useful work into the codebase with the least contributor starvation. If a PR contains something worth keeping, absorb that value directly when practical: accept it as-is, fix bugs, improve the architecture, rename things, turn it into a plugin, cherry-pick parts, or reject the parts that do not fit.

The goal is not to block contributors unnecessarily. The goal is to identify useful work, preserve it, and keep the project moving.

Read [docs/PROJECT_CHARTER.md](docs/PROJECT_CHARTER.md) when a PR changes
Beads' product surface area. Scope boundaries should guide where value lands:
core, metadata, integration, plugin, orchestration layer, or external tool.

## Contributor Protection

External contributor PRs have priority. Before implementing related work, opening a competing PR, or closing a PR, check whether an existing contributor PR already addresses the same area.

- Review contributor work first. Read the PR description, changed files, linked issues, tests, CI status, and latest discussion.
- Build on the contributor branch by default. If the PR branch allows maintainer edits, push maintainer fix commits directly to that branch instead of opening a replacement PR.
- Preserve contributor tests unless they are actually wrong.
- Preserve attribution with original commits when possible. Maintainer commits on a contributor branch should keep the contributor's original commits intact; transformed local commits must use `Co-authored-by:` and PR references.
- Never close, supersede, or replace a contributor PR silently. Explain what was preserved, what changed, and why.
- Open a replacement PR only when in-place maintainer edits are not possible or would create a larger risk, such as when the contributor branch is not writable, the branch history is unusable, or the accepted change must be substantially reimplemented. Document that reason in both PR threads.
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
- **Fix-merge**: Pull the PR locally, make substantial fixes on the contributor branch, then push the branch so the original PR can merge. Use when the PR is busted but valuable and maintainer edits are possible.
- **Cherry-pick**: Keep only selected items from a PR with multiple features or fixes. Commit the useful parts locally with attribution, then close the PR with an explanation.
- **Split-merge**: Split a multi-concern PR into separate commits, then push all accepted parts with attribution to the original contributor.
- **Replacement PR**: Carry useful work into a new maintainer PR only after confirming the original branch cannot reasonably be fixed in place. Preserve attribution with original commits where possible, otherwise use `Co-authored-by:` trailers and PR references, and explain the reason replacement was necessary.
- **Redesign/reimplement**: Reject the submitted design but solve the underlying problem another way. Close the PR with thanks and an explanation.
- **Retire**: Close an obsolete PR with thanks because it was superseded or already fixed elsewhere.
- **Reject**: Close politely when the feature does not pay its weight in tech debt, is too niche for core, or the design does not meet project standards.
- **Request changes**: Last resort. Avoid this when the maintainer or agents can reasonably absorb, transform, or land the useful parts directly.

Other outcomes are possible, including rerouting a PR to the right project or banning a contributor, but the list above covers the normal cases.

## Merge Discipline and Review Requirements

These rules apply to everyone who can merge — human maintainers and agents alike. They exist because a two-month audit of 440 merged PRs (epic bd-6dnrw) found the project's worst defects entered through merges that skipped review, hid their real contents, or overrode an outstanding objection. A merge is an irreversible act of trust; treat it as one.

### Self-merge and outstanding objections

- Do not self-merge a nontrivial PR. "Nontrivial" is anything beyond a typo, comment, or pure-docs fix: any code, schema, migration, build, CI, dependency, or sync-path change. Nontrivial work authored by a maintainer or agent must be merged by a different reviewer.
- Never merge over an unresolved `CHANGES_REQUESTED` review. The requested changes must be addressed and the reviewer's objection withdrawn (or explicitly overridden by the project owner, recorded in the thread) before merge. A new approval does not erase a standing change request from someone else.
- A WIP- or draft-titled branch is not mergeable. Finish it, retitle it, and get it reviewed.

### The PR body must match the diff

- The title and body must describe the riskiest thing the diff actually does. A docs or test title may not carry a storage-, schema-, migration-, or sync-layer change underneath it.
- One PR, one coherent concern. Do not bundle unrelated commits behind a small-sounding claim ("19-line fix" that drags in six commits). Split grab-bags before merge.
- If the body and the diff disagree, the PR is not ready — fix the body or shrink the diff. Reviewers and future auditors rely on the body being true.

### Review must be real and accountable

- A merge needs a substantive human review, not a rubber stamp. An approval that engaged with the change is required; an empty approval or a bot-only approval does not satisfy the review requirement for nontrivial PRs.
- Reviews posted by an automated agent must say so plainly and must not impersonate a maintainer. If a review was posted in error by an agent under a human's account, it does not count and must be retracted.
- Approvals from unidentified or unestablished accounts do not satisfy the review requirement. The reviewer must be a known, accountable participant.

### Schema, migration, and sync paths are protected

- Any change touching `migrations/`, the schema, destructive data paths, or the Dolt sync/clone/merge paths requires a filled-out PR template and a real human review before merge — no exceptions, no self-merge, no bot-only approval.
- These paths should be enforced by branch protection (review-required), not just convention. (Configuring branch protection and defining which agent identities may merge are owner actions tracked in bd-6dnrw.23.)

### Revert ping-pong

- If a change to a sync-critical file is reverted, stop merging in that area. Do not re-merge, re-revert, and re-merge in a tight loop; that pattern (five merges in two hours on the same sync files) is how regressions get laundered into `main`.
- After a revert, open an issue, diagnose the root cause, and land a single reviewed fix. Escalate to the project owner rather than continuing a revert cycle.

## Operating Rules

- Prefer landing or transforming useful work over asking the contributor to do more rounds.
- Preserve contributor attribution when absorbing, fixing, cherry-picking, splitting, or reimplementing PR value.
- Before opening a competing or replacement PR, attempt the contributor-branch path first: fetch the PR, test it, make maintainer fix commits on that branch when permitted, and push back to the same PR.
- Be explicit when closing a PR: thank the contributor, state the outcome, and explain what was accepted, rejected, superseded, or implemented differently.
- Treat request-changes as exceptional because it can strand contributor work.
- Consider the entire PR thread. Valuable clarifying info are often in the comments.
- File follow-up work as beads issues instead of hidden notes.
- When code changes result from PR maintenance, follow repo quality gates and session completion rules in `AGENTS.md`.
- Post multi-line PR comments from a real Markdown body file or a shell heredoc, not from strings with escaped `\n` sequences. Run `scripts/gh-body-lint <body-file>` before posting body files; after posting or editing, verify the rendered body with `gh pr view --comments --json comments --jq ...` before moving on.
- Sign agent-written GitHub comments, reviews, and commits using [docs/AGENT_SIGNING.md](docs/AGENT_SIGNING.md).
- Before finishing, re-read the PR, latest comments, review threads, and linked issues; address or explicitly note any unresolved action items.
