---
sidebar_position: 2
title: Database Corruption
description: Recover from SQLite database corruption
---

# Database Corruption Recovery

This runbook helps you recover from SQLite database corruption in Beads.

## Symptoms

- SQLite error messages during `bd` commands
- "database is locked" errors that persist
- Missing issues that should exist
- Inconsistent database state

## Diagnosis

```bash
# Check database integrity
bd status

# Look for corruption indicators
ls -la .beads/beads.db*
```

If you see `-wal` or `-shm` files alongside `beads.db`, a transaction may have been interrupted.

## Solution

:::warning
Back up your `.beads/` directory before proceeding.
:::

**Step 1:** Stop the Dolt server
```bash
bd dolt stop
```

**Step 2:** Back up current state
```bash
cp -r .beads .beads.backup
```

**Step 3:** Rebuild database
```bash
bd doctor --fix
```

**Step 4:** Verify recovery
```bash
bd status
bd list
```

**Step 5:** Restart the Dolt server
```bash
bd dolt start
```

## Prevention

- Avoid interrupting `bd sync` operations
- Let the Dolt server handle synchronization
- Use `bd dolt stop` before system shutdown
