---
description: How to resolve merge conflicts in the beads Dolt database
---

# Resolving Beads Merge Conflicts

Beads uses Dolt as its storage backend. Dolt handles merges natively using its built-in three-way merge, similar to git.

## 1. Check for Conflicts

```bash
bd doctor
bd sync
```

If `bd sync` reports merge conflicts, Dolt will list the conflicting tables and rows.

## 2. Resolve Conflicts

Dolt provides SQL-based conflict resolution:

```bash
# View conflicts
bd sql "SELECT * FROM dolt_conflicts"

# Resolve by accepting ours or theirs
bd sql "CALL dolt_conflicts_resolve('--ours')"
# OR
bd sql "CALL dolt_conflicts_resolve('--theirs')"
```

## 3. Verify and Complete

```bash
# Verify the resolution
bd list --json | head

# Complete the sync
bd sync
```

## Legacy: JSONL Merge Conflicts

If you encounter merge conflicts in `.beads/issues.jsonl` from a legacy setup, import the resolved file:

```bash
# Resolve the git conflict in the JSONL file manually, then:
bd import -i .beads/issues.jsonl
git add .beads/issues.jsonl
git merge --continue
```
