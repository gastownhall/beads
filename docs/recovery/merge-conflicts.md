---
title: Merge Conflicts
description: Resolve Dolt merge conflicts
---

This runbook helps you resolve merge conflicts that occur during Dolt sync operations.

## Symptoms

- `bd dolt pull` (or `bd sync`) reports merge conflicts
- Different issue states between clones

## What a declined conflict already does

A pull whose conflict the auto-resolver declines — for example, both clones
edited the same issue since the last sync — never leaves a half-open merge
behind. The pull aborts its merge, restores the branch to its pre-pull
state, and reports the conflicts for manual resolution. The store keeps
accepting writes, and the local edits are preserved.

## Diagnosis

```bash
# List the conflicted rows the reported conflicts refer to
bd conflicts list

# Check database health
bd doctor
```

## Solution

**Step 1:** Inspect the conflicts

```bash
bd conflicts list
```

**Step 2:** Choose a resolution

Resolve the reported conflicts per row:

```bash
bd conflicts resolve <id> --ours    # keep the local edit
bd conflicts resolve <id> --theirs  # take the remote edit
```

Or re-run the merge with a conflict strategy on the refs the pull fetched:

```bash
bd vc merge <remote>/<branch> --strategy ours
bd vc merge <remote>/<branch> --strategy theirs
```

Or drop the merge entirely, keeping the local branch as it was:

```bash
bd sql "CALL DOLT_MERGE('--abort')"
```

**Step 3:** Verify and publish the resolved state

```bash
bd list
bd dolt push
```

## Prevention

- Sync before and after work sessions using `bd dolt pull` / `bd dolt push`
- Avoid concurrent modifications from multiple clones without the Dolt server running
