---
sidebar_position: 1
title: Recovery Overview
description: Diagnose and resolve common Beads issues
---

# Recovery Overview

This section provides step-by-step recovery procedures for common Beads issues. Each runbook follows a consistent format: Symptoms, Diagnosis, Solution (5 steps max), and Prevention.

## Common Issues

| Issue | Symptoms | Runbook |
|-------|----------|---------|
| Database Corruption | SQLite errors, missing data | [Database Corruption](/recovery/database-corruption) |
| Merge Conflicts | JSONL conflicts during sync | [Merge Conflicts](/recovery/merge-conflicts) |
| Circular Dependencies | Cycle detection errors | [Circular Dependencies](/recovery/circular-dependencies) |
| Sync Failures | `bd sync` errors | [Sync Failures](/recovery/sync-failures) |

## Quick Diagnostic

Before diving into specific runbooks, try these quick checks:

```bash
# Check Beads status
bd status

# Verify Dolt server is running
bd doctor

# Check for blocked issues
bd blocked
```

:::tip
Most issues can be diagnosed with `bd status`. Start there before following specific runbooks.
:::

## Getting Help

If these runbooks don't resolve your issue:

1. Check the [FAQ](/reference/faq)
2. Search [existing issues](https://github.com/steveyegge/beads/issues)
3. Open a new issue with diagnostic output
