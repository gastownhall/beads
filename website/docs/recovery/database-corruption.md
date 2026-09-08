---
sidebar_position: 2
title: Database Corruption
description: Recover from Dolt database corruption
---

# Database Corruption Recovery

This runbook helps you recover from database corruption in Beads.

## Symptoms

- Error messages during `bd` commands
- "database is locked" errors that persist
- Missing issues that should exist
- Inconsistent database state

## Diagnosis

```bash
# Check database integrity
bd doctor

# Check Dolt server health
bd dolt show
```

## Solution

**Step 1:** Stop the Dolt server
```bash
bd dolt stop
```

**Step 2:** Back up current state
```bash
cp -r .beads .beads.backup
```

**Step 3:** Preview what doctor would fix
```bash
bd doctor --dry-run
```

**Step 4:** Rebuild database
```bash
bd doctor --fix
```

**Step 5:** Verify recovery
```bash
bd doctor
bd list
```

**Step 6:** Restart the Dolt server
```bash
dolt sql-server
```

## Prevention

- Let the Dolt server handle synchronization
- Use `bd dolt stop` before system shutdown
- Run `bd doctor` periodically to catch issues early
