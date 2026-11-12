---
title: Merge Conflicts
description: Resolve Dolt merge conflicts
---

This runbook helps you resolve merge conflicts that occur during Dolt sync operations.

## Symptoms

- `bd dolt pull` (or `bd sync`) reports merge conflicts
- `bd dolt pull` refuses because a conflicted merge is already open on the database
- Different issue states between clones

## What a declined conflict already does

A pull whose conflict the auto-resolver declines (for example, both clones
edited the same issue since the last sync) does not leave a half-open merge
behind. The pull aborts its merge, restores the branch to its pre-pull state,
and reports the conflicted issues. The store keeps accepting writes, and the
local edits are preserved.

Because that merge was aborted, `bd conflicts list` shows nothing afterwards
and per-row `bd conflicts resolve` does not apply. The pull did fetch the
remote branch, so you resolve by merging it again with a strategy.

## Solution: after a pull reported conflicts

**Step 1:** Merge the fetched remote branch, choosing which side wins

```bash
bd vc merge <remote>/<branch> --strategy theirs  # take the remote edits
bd vc merge <remote>/<branch> --strategy ours    # keep the local edits
```

For example, `bd vc merge origin/main --strategy theirs`.

**Step 2:** Verify and publish the resolved state

```bash
bd list
bd dolt push
```

## Solution: a merge that is still open

If a pull refuses because a conflicted merge is already open (left by an
older `bd`, or opened deliberately), that merge is still live and can be
handled row by row.

**Step 1:** Inspect the conflicts

```bash
bd conflicts list
```

**Step 2:** Resolve each row, or drop the merge

```bash
bd conflicts resolve <id> --ours    # keep the local edit
bd conflicts resolve <id> --theirs  # take the remote edit
```

Or abandon the merge entirely, keeping the local branch as it was:

```bash
bd sql "CALL DOLT_MERGE('--abort')"
```

**Step 3:** Pull again, then verify and publish

```bash
bd dolt pull
bd list
bd dolt push
```

## Prevention

- Sync before and after work sessions using `bd dolt pull` / `bd dolt push`
- Avoid concurrent modifications from multiple clones without the Dolt server running
