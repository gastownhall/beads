# Deletion Tracking

This document describes how bd tracks and propagates deletions across repository clones.

## Overview

When issues are deleted in one clone, those deletions need to propagate to other clones. Without this mechanism, deleted issues would "resurrect" when another clone's database is imported.

**Beads uses inline tombstones** - deleted issues are converted to a special `tombstone` status and remain in `issues.jsonl`. This provides:

- Full audit trail (who, when, why)
- Atomic sync with issue data (no separate manifest to merge)
- TTL-based expiration (default 30 days)
- Proper 3-way merge conflict resolution

## How Tombstones Work

When you delete an issue:

1. The issue's status changes to `tombstone`
2. Deletion metadata is recorded (`deleted_at`, `deleted_by`, `delete_reason`)
3. The original issue type is preserved in `original_type`
4. All dependencies are removed (tombstones don't block anything)
5. The tombstone syncs via git like any other issue

### Tombstone Fields

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | Always `"tombstone"` |
| `deleted_at` | ISO 8601 | When the issue was deleted |
| `deleted_by` | string | Actor who performed the deletion |
| `delete_reason` | string | Optional context (e.g., "duplicate", "cleanup") |
| `original_type` | string | Issue type before deletion (task, bug, etc.) |

### Example Tombstone in JSONL

```json
{"id":"bd-42","status":"tombstone","title":"Original title","deleted_at":"2025-01-15T10:00:00Z","deleted_by":"stevey","delete_reason":"duplicate of bd-xyz","original_type":"task"}
```

## Commands

### Deleting Issues

```bash
bd delete bd-42                    # Delete single issue (preview mode)
bd delete bd-42 --force            # Actually delete
bd delete bd-42 bd-43 bd-44 -f     # Delete multiple issues
bd delete bd-42 --cascade -f       # Delete with all dependents
bd delete --from-file ids.txt -f   # Delete from file (one ID per line)
bd delete bd-42 --dry-run          # Preview what would be deleted
```

### Viewing Deleted Issues

```bash
bd list --status=tombstone         # List all tombstones
bd show bd-42                      # View tombstone details (if you know the ID)
```

## TTL and Expiration

Tombstones expire after a configurable TTL (default: 30 days). This prevents unbounded growth while ensuring deletions propagate to all clones.

### How Expiration Works

1. Tombstones older than TTL + 1 hour grace period are eligible for pruning
2. `bd admin compact` removes expired tombstones from `issues.jsonl`
3. Git history fallback handles edge cases where pruned tombstones are needed

### Configuration

```yaml
# .beads/config.yaml
tombstone:
  ttl_days: 30        # Default: 30 days
```

Or via CLI:
```bash
bd config set tombstone.ttl_days 60
```

### Manual Pruning

```bash
bd admin compact                   # Prune expired tombstones (and other compaction)
```

## Conflict Resolution

When the same issue is modified in one clone and deleted in another:

1. Both changes sync via git
2. 3-way merge detects the conflict
3. Resolution rules:
   - If tombstone is expired → live issue wins (resurrection)
   - If tombstone is fresh → tombstone wins (deletion propagates)
   - `updated_at` timestamps break ties

This ensures deletions propagate reliably while handling clock skew and delayed syncs.

## Migration from Legacy Format

Prior to v0.30, beads used a separate `deletions.jsonl` manifest. To migrate:

```bash
bd migrate tombstones              # Convert deletions.jsonl to inline tombstones
bd migrate tombstones --dry-run    # Preview changes first
```

The migration:
1. Reads existing deletions from `deletions.jsonl`
2. Creates tombstone entries in `issues.jsonl`
3. Archives the old file as `deletions.jsonl.migrated`

After migration, run `bd sync` to propagate tombstones to other clones.

## Troubleshooting

### Deleted Issue Reappearing

If a deleted issue reappears after sync:

```bash
# Check if it's a tombstone
bd list --status=tombstone | grep bd-xxx

# Check tombstone details
bd show bd-xxx

# Force re-import from JSONL
bd import --force
```

If the issue keeps reappearing, the tombstone may have expired. Re-delete it:

```bash
bd delete bd-xxx --force
bd sync
```

### Tombstones Not Syncing

Ensure tombstones are being exported:

```bash
# Check if tombstone is in JSONL
grep '"id":"bd-xxx"' .beads/issues.jsonl

# Force export
bd export --force
bd sync
```

### Too Many Tombstones

If you have many old tombstones:

```bash
# Check tombstone count
bd list --status=tombstone | wc -l

# Prune expired tombstones
bd admin compact
```

## Design Rationale

### Why Inline Tombstones?

The previous `deletions.jsonl` manifest had issues:

- **Wild poisoning**: Stale clone's manifest could delete issues incorrectly
- **Merge inconsistency**: Separate file meant separate merge logic
- **Two sources of truth**: Issue data and deletion data could diverge

Inline tombstones solve these by:

- Single source of truth (`issues.jsonl`)
- Same merge semantics as regular issues
- Atomic with issue data
- Full audit trail preserved

### Why TTL-Based Expiration?

- Bounds storage growth (tombstones eventually pruned)
- Git history fallback handles edge cases
- 30-day default handles typical sync scenarios
- Configurable for teams with longer sync cycles

### Why 1-Hour Grace Period?

Clock skew between machines can cause issues:

- Machine A deletes issue at 10:00 (its clock)
- Machine B's clock is 30 minutes ahead
- Without grace period, B might see tombstone as expired immediately

The 1-hour grace period ensures tombstones propagate even with minor clock drift.

## Wisps: Intentional Tombstone Bypass

**Wisps** (ephemeral issues created by `bd mol wisp`) are intentionally excluded from tombstone tracking.

### Why Wisps Don't Need Tombstones

Tombstones exist to prevent resurrection during sync. Wisps don't sync:

| Property | Regular Issues | Wisps |
|----------|---------------|-------|
| Exported to JSONL | Yes | No |
| Synced to other clones | Yes | No |
| Can resurrect | Yes | No |
| Tombstone on delete | Yes | No (hard delete) |

Since wisps never leave the local SQLite database, they cannot resurrect from remote clones. Creating tombstones for them would be unnecessary overhead.

### How Wisp Deletion Works

When `bd mol squash` compresses wisps into a digest:

1. The digest issue is created (permanent, syncs normally)
2. Wisp children are **hard-deleted** via `DeleteIssue()`
3. No tombstones are created
4. The wisps simply disappear from local SQLite

This is intentional, not a bug. See [ARCHITECTURE.md](ARCHITECTURE.md#wisps-and-molecules) for the full design rationale.

### If You Need Wisp History

Wisps are stored in the main database with `Wisp=true` flag and are not exported to JSONL. They exist in local SQLite until garbage collected or squashed. Future enhancements may include:

- Configurable wisp retention policies
- Automatic staleness detection based on dependency graph pressure

## Related

- [ARCHITECTURE.md](ARCHITECTURE.md) - Overall architecture including Wisps and Molecules
- [CONFIG.md](CONFIG.md) - Configuration options
- [DAEMON.md](DAEMON.md) - Daemon auto-sync behavior
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) - General troubleshooting
