---
description: Compact old closed issues using semantic summarization
argument-hint: [--all] [--id issue-id] [--dry-run]
---

Reduce database size by summarizing closed issues no longer actively referenced.

## Compaction Tiers

- **Tier 1**: Semantic compression (30+ days closed, ~70% size reduction)
- **Tier 2**: Ultra compression (90+ days closed, ~95% size reduction)

## Usage

- **Preview candidates**: `bd admin compact --dry-run`
- **Compact all eligible**: `bd admin compact --all`
- **Compact specific issue**: `bd admin compact --id bd-42`
- **Force compact**: `bd admin compact --id bd-42 --force` (bypass age checks)
- **View statistics**: `bd admin compact --stats`

## Options

- **--tier**: Choose compaction tier (1 or 2, default: 1)
- **--workers**: Parallel workers (default: 5)
- **--batch-size**: Issues per batch (default: 10)

## Important

This is **permanent graceful decay** - original content is discarded. Use `bd restore <id>` to view full history from git if needed.

Useful for long-running projects to keep database size manageable.
