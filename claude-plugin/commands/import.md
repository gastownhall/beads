---
description: Import issues from JSONL format
argument-hint: [-i input-file]
---

Import issues from JSON Lines format (one JSON object per line).

## Usage

- **From stdin**: `bd import` (reads from stdin)
- **From file**: `bd import -i issues.jsonl`
- **Preview**: `bd import -i issues.jsonl --dry-run`

## Behavior

- **Existing issues** (same ID): Updated with new data
- **New issues**: Created
- **Same-ID scenarios**: With hash-based IDs (v0.20.1+), same ID = same issue being updated (not a collision)

## Preview Changes

Use `--dry-run` to see what will change before importing:

```bash
bd import -i issues.jsonl --dry-run
# Shows: new issues, updates, exact matches
```

## Automatic Import

The Dolt server automatically imports from `.beads/issues.jsonl` when it's newer than the database (e.g., after `git pull`). Manual import is rarely needed.

## Options

- **--skip-existing**: Skip updates to existing issues
- **--strict**: Fail on dependency errors instead of warnings
