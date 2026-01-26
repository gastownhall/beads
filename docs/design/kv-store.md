# Key-Value Store for Beads

> Design document for `bd kv` commands
> Status: Draft - pending Dolt team review
> Date: 2026-01-21

## Overview

Add a simple key-value store to beads for persisting lightweight metadata that doesn't fit the issue model. This enables storing things like:

- Feature flags (`bd kv set debug_mode true`)
- Project configuration (`bd kv set entry_point src/main.ts`)
- Workflow state across sessions (`bd kv set current_sprint 42`)
- Agent memory that survives context cycling

## Commands

```
bd kv set <key> <value>   # Set a key-value pair
bd kv get <key>           # Get a value (exit 1 if not found)
bd kv delete <key>        # Delete a key
bd kv list [prefix]       # List all pairs (optionally filtered by prefix)
```

All commands support `--json` for machine-readable output.

### Examples

```bash
# Store project metadata
bd kv set primary_language go
bd kv set entry_point cmd/bd/main.go

# Retrieve values
bd kv get primary_language
# Output: go

# List all pairs
bd kv list
# Output:
# primary_language = go
# entry_point = cmd/bd/main.go

# List with prefix filter
bd kv list entry
# Output:
# entry_point = cmd/bd/main.go

# JSON output
bd kv list --json
# Output: [{"key":"primary_language","value":"go","set_at":"2026-01-21T10:30:00Z","set_by":"beads/crew/collins"},...]

# Delete a key
bd kv delete primary_language
```

## Schema

### Dolt Table: `kv`

```sql
CREATE TABLE kv (
    `key` VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL,
    set_at DATETIME NOT NULL,
    set_by VARCHAR(255) NOT NULL
);
```

| Column | Type | Description |
|--------|------|-------------|
| `key` | VARCHAR(255) | Primary key, the lookup key |
| `value` | TEXT | The stored value (always string) |
| `set_at` | DATETIME | When the value was set (UTC) |
| `set_by` | VARCHAR(255) | Actor who set it (e.g., "beads/crew/collins", "human") |

### Design Decisions

**Why not use the config table?**
- Config is for beads internal settings (sync mode, integrations, etc.)
- Mixing user data with internal config creates namespace collisions
- Separate table is cleaner and avoids future conflicts

**Why not make KV pairs into issues/beads?**
- KV is meant to be lightweight - issues have significant overhead (id, title, description, status, priority, type, etc.)
- Different lifecycle - KV is "set and forget", issues are "open → work → close"
- Would pollute `bd list` with non-work entries

**Why track `set_at` and `set_by`?**
- Attribution: know who set a value and when
- Debugging: trace when configuration changed
- Future: enables conflict resolution in multi-writer scenarios

## Sync Behavior

KV data syncs via the standard beads-sync mechanism:

1. **Export**: On push, KV table exports to `.beads/kv.jsonl`
2. **Import**: On pull, `.beads/kv.jsonl` imports back to KV table
3. **Merge**: Last-write-wins based on `set_at` timestamp

### JSONL Format

`.beads/kv.jsonl` - one JSON object per line:

```jsonl
{"key":"primary_language","value":"go","set_at":"2026-01-21T10:30:00Z","set_by":"beads/crew/collins"}
{"key":"entry_point","value":"cmd/bd/main.go","set_at":"2026-01-21T10:31:00Z","set_by":"human"}
```

This format is:
- Human-readable and diffable in git
- Streaming-friendly (append without rewriting)
- Consistent with `issues.jsonl` pattern

## RPC Operations

For daemon mode, add these operations to the RPC protocol:

| Operation | Args | Response |
|-----------|------|----------|
| `kv_set` | `{key, value}` | `{success: bool}` |
| `kv_get` | `{key}` | `{value: string, found: bool}` |
| `kv_delete` | `{key}` | `{success: bool}` |
| `kv_list` | `{prefix?: string}` | `{items: [{key, value, set_at, set_by}]}` |

## Future Considerations

These are NOT in scope for v1, but the design accommodates them:

1. **Local-only keys**: Convention could be `_local.` prefix for keys that don't sync
2. **TTL/expiration**: Could add `expires_at` column later
3. **Namespaces**: Could add `namespace` column for scoping
4. **Value types**: Currently string-only; could add `--type=json` flag later

## Credit

This feature is inspired by PR #1164 from Piyush Jha (@Hackinet). The original PR proposed `bd set/get/clear` as top-level commands; this design groups them under `bd kv` for cleaner namespacing.

## Questions for Dolt Team

1. Any concerns with the schema design?
2. Preferred approach for the DATETIME column - store as ISO string or native datetime?
3. Any Dolt-specific considerations for the sync/merge behavior?
4. Should we use Dolt's conflict resolution features, or handle last-write-wins in application code?
