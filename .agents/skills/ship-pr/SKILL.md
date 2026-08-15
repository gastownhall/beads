---
name: ship-pr
description: "Ship current work through a pull request: create or reuse it, pass required checks, merge it, close linked issues, and verify the remote result."
disable-model-invocation: true
---

# Ship a PR

## 1. Resolve or create

Use an explicitly supplied PR. Otherwise treat the current branch as the work to
ship.

Reuse the branch's open PR. When none exists, commit the intended work, push the
branch, and create a PR against the user-named target or the repository default
branch. Use the repository PR template.

An explicit PR is authoritative: report it when it is missing or not open rather
than selecting another PR.

Treat an existing PR's base as the target. When the user names another target,
retarget the PR before continuing.

Record the repository, base, head branch and SHA, draft and review state,
required checks, and host-native closing issue links.

## 2. Gate

Resolve linked issues from explicit arguments, otherwise the PR's host-native
closing links. Bare mentions do not count. Add missing closing links to the PR
body when it targets the default branch.

Mark a draft ready. Watch every required check on the recorded head SHA and fail
fast on failure or cancellation. If the head changes, repeat the gate on the new
SHA.

Require approvals, rulesets, and other branch protections. Never bypass them
with administrator privileges.

## 3. Merge

Use the user's merge method, otherwise the repository convention, otherwise
squash.

Merge through the host. On GitHub, pass `--match-head-commit <sha>` and
`--delete-branch`. A head mismatch restarts the gate.

Wait through any merge queue until the PR state is `MERGED`.

## 4. Verify

Read the PR back. Fetch the target from the base repository and verify the
host-reported merge commit is reachable from that remote branch.

Close any selected issue still open, linking the merged PR. Verify the remote
head branch was deleted where permitted; otherwise report why it remains.

Report the PR URL, merge commit, target, issue results, and branch result.
