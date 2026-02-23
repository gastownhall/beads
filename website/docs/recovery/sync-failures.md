---
sidebar_position: 5
title: Sync Failures
description: Recover from bd sync failures
---

# Sync Failures Recovery

This runbook helps you recover from `bd sync` failures.

## Symptoms

- `bd sync` hangs or times out
- Network-related error messages
- "failed to push" or "failed to pull" errors
- Dolt server not responding

## Diagnosis

```bash
# Check Dolt server health
bd doctor

# Check sync state
bd status

# View Dolt server logs
tail -50 .beads/dolt/sql-server.log
```

## Solution

**Step 1:** Stop the Dolt server
```bash
bd dolt stop
```

**Step 2:** Check for lock files
```bash
ls -la .beads/*.lock
# Remove stale locks if Dolt server is definitely stopped
rm -f .beads/*.lock
```

**Step 3:** Force a fresh sync
```bash
bd doctor --fix
```

**Step 4:** Restart the Dolt server
```bash
bd dolt start
```

**Step 5:** Verify sync works
```bash
bd sync
bd status
```

## Common Causes

| Cause | Solution |
|-------|----------|
| Network timeout | Retry with better connection |
| Stale lock file | Remove lock after stopping Dolt server |
| Corrupted state | Use `bd doctor --fix` |
| Git conflicts | See [Merge Conflicts](/recovery/merge-conflicts) |

## Prevention

- Ensure stable network before sync
- Let sync complete before closing terminal
- Use `bd dolt stop` before system shutdown
