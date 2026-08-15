---
name: ship-spec
description: Implement a parent spec's child issues serially and ship each to one target branch.
---

# Ship a spec

Require a parent spec.

Before starting, read [`/implement`](../implement/SKILL.md) completely. Apply
it in full to every child issue.

Resolve the target branch before starting:

1. Use the branch provided by the user.
2. Otherwise, use the repository's obvious default or development branch.
3. If ambiguous, ask the user once, proposing `origin/main`.

Then repeat:

1. Refresh the parent spec and its linked child issues.
2. Select one ready, unfinished child issue. Order by dependencies, then issue
   number.
3. Create a dedicated branch from the latest target branch.
4. Apply `/implement` to that issue only.
5. Complete the issue only when:
   - its acceptance criteria are satisfied;
   - `/implement`'s completion criteria are satisfied;
   - review findings are resolved;
   - required checks pass.
6. Integrate the completed branch into the latest target branch and push it.
7. Confirm the completed commit is reachable from the remote target branch
   before selecting another issue.

Complete only when every linked child issue and the parent spec's acceptance
criteria are satisfied on the remote target branch, with the full test suite
passing there.
