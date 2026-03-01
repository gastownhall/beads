---
sidebar_position: 3
title: Merge Conflicts
description: Resolve Dolt merge conflicts
---

# Merge Conflicts Recovery

This runbook helps you resolve merge conflicts that occur during Dolt sync operations.

## Symptoms

- `bd dolt pull` fails with conflict errors
- Different issue states between clones

## Diagnosis

```bash
# Check database health
bd doctor

# Preview what fixes would be applied
bd doctor --dry-run
```

## Solution

**Step 1:** Back up current state
```bash
cp -r .beads .beads.backup
```

**Step 2:** Check for conflicts
```bash
bd doctor
```

**Step 3:** Fix to reconcile
```bash
bd doctor --fix
```

**Step 4:** Verify state
```bash
bd list
bd stats
```

**Step 5:** Push resolved state
```bash
bd dolt push
```

## Prevention

- Sync before and after work sessions using `bd dolt pull` / `bd dolt push`
- Avoid concurrent modifications from multiple clones without the Dolt server running
