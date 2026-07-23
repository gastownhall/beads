---
description: Export issues to JSONL format
argument-hint: [-o output-file]
---

Export all issues to JSON Lines format (one JSON object per line).

## Usage

- **To stdout**: `bd export`
- **To file**: `bd export -o issues.jsonl`
- **Include all records**: `bd export --all -o full.jsonl`

Issues are sorted by ID for consistent diffs, making git diffs readable.

## When to Use

Dolt is the primary storage backend, so manual export is rarely needed. Use `bd export` when you need:
- A JSONL snapshot of issue records
- Data migration to another system
- Sharing issues outside the Dolt workflow

`bd export` is not a full database backup. It does not capture Dolt branches,
commit history, working-set state, or non-issue tables. Use `bd backup` for a
restorable Dolt-native database backup.
