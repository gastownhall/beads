---
sidebar_position: 5
title: Sync Failures
description: Recover from Dolt sync failures
---

# Sync Failures Recovery

This runbook helps you recover from Dolt sync failures.

## Symptoms

- `bd dolt push` or `bd dolt pull` hangs or times out
- Network-related error messages
- "failed to push" or "failed to pull" errors
- Dolt server not responding

## Diagnosis

```bash
# Check Dolt server health
bd doctor
bd dolt show

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

**Step 3:** Back up and preview fixes
```bash
cp -r .beads .beads.backup
bd doctor --dry-run
```

**Step 4:** Apply fixes if needed
```bash
bd doctor --fix
```

**Step 5:** Restart the Dolt server
```bash
dolt sql-server
```

**Step 6:** Verify sync works
```bash
bd dolt push
bd doctor
```

## Common Causes

| Cause | Solution |
|-------|----------|
| Network timeout | Retry with better connection |
| Stale lock file | Remove lock after stopping Dolt server |
| Corrupted state | Back up, then `bd doctor --fix` |
| Merge conflicts | See [Merge Conflicts](/recovery/merge-conflicts) |

## Prevention

- Ensure stable network before sync
- Let sync complete before closing terminal
- Use `bd dolt stop` before system shutdown
