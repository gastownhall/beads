---
sidebar_position: 3
title: Merge Conflicts
description: Resolve Dolt merge conflicts
---

# Merge Conflicts Recovery

This runbook helps you resolve merge conflicts that occur during Dolt sync operations.

## Symptoms

- `bd sync` fails with conflict errors
- Different issue states between clones

## Diagnosis

```bash
# Check database health
bd doctor

# Check for Dolt conflicts
bd doctor --fix
```

## Solution

**Step 1:** Check for conflicts
```bash
bd doctor
```

**Step 2:** Force rebuild to reconcile
```bash
bd doctor --fix
```

**Step 3:** Verify state
```bash
bd list
bd stats
```

**Step 4:** Sync resolved state
```bash
bd sync
```

## Prevention

- Sync before and after work sessions
- Use `bd sync` regularly
- Avoid concurrent modifications from multiple clones without the Dolt server running
