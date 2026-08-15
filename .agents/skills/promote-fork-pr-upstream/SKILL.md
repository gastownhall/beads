---
name: promote-fork-pr-upstream
description: Promote or repair changes from a GitHub fork as a clean upstream pull request. Use for fork branches, fork-local pull requests, commit sets, or upstream pull requests carrying fork-only history.
---

# Promote a Fork PR Upstream

## 1. Resolve the route

Resolve with `gh`:

- ordered intended commits: source PR first, otherwise the supplied commit set;
- exact fork `<owner>:<branch>@<SHA>`;
- upstream parent and target base.

Read the parent with `gh repo view`. Never infer it from names or remotes. Stop
only when the route or intended patch set cannot be established.

## 2. Find the upstream PR

Search all upstream PR states for the exact `<fork-owner>:<head-branch>`, then
validate the target base. Reuse the open exact match. Reopen a closed match when
possible; otherwise create its replacement. Titles are not identity.

## 3. Audit and rebuild

Fetch the exact base and head without changing the working tree. Compare the
commit patches and final diff with the intended changes. Any extra
upstream-visible commit, patch, or path is contamination.

For a contaminated head, create a disposable worktree at the fetched upstream
base and cherry-pick only the intended commits in order. Resolve conflicts from
source evidence; report any semantic conflict the evidence cannot resolve.

Before publishing, verify:

- rebuilt patches match their source patches, with inspected conflict
  resolutions accounting for any difference;
- the final diff contains only intended changes;
- affected and repository-required checks pass;
- the remote fork head still equals the captured SHA.

Push the rebuilt SHA to the exact branch with:

```bash
git push --force-with-lease=refs/heads/<branch>:<captured-sha> \
  <fork> <rebuilt-sha>:refs/heads/<branch>
```

Read the remote ref back and require the rebuilt SHA. Remove the disposable
worktree and branch.

## 4. Publish and verify

Use the upstream PR template. Create or update the selected PR with verified
scope and observed test results only. Read it back until its base, head SHA,
commits, and changed files match the route.

Report the PR URL, mergeability, and observed checks.
