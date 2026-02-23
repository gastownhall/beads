---
id: configuration
title: Configuration
sidebar_position: 1
---

# Configuration

Complete configuration reference for beads.

## Configuration Locations

1. **Project config**: `.beads/config.toml` (highest priority)
2. **User config**: `~/.beads/config.toml`
3. **Environment variables**: `BEADS_*`
4. **Command-line flags**: (highest priority)

## Managing Configuration

```bash
# Get config value
bd config get import.orphan_handling

# Set config value
bd config set import.orphan_handling allow

# List all config
bd config list

# Reset to default
bd config reset import.orphan_handling
```

## Configuration Options

### Database

```toml
[database]
path = ".beads/beads.db"     # Database file location
```

### ID Generation

```toml
[id]
prefix = "bd"                 # Issue ID prefix
hash_length = 4               # Hash length in IDs
```

### Import

```toml
[import]
orphan_handling = "allow"     # allow|resurrect|skip|strict
dedupe_on_import = false      # Run duplicate detection after import
```

| Mode | Behavior |
|------|----------|
| `allow` | Import orphans without validation (default) |
| `resurrect` | Restore deleted parents as tombstones |
| `skip` | Skip orphaned children with warning |
| `strict` | Fail if parent missing |

### Export

```toml
[export]
path = ".beads/issues.jsonl"  # Default export file path (for bd export command)
```

### Git

```toml
[git]
auto_commit = true            # Auto-commit on sync
auto_push = true              # Auto-push on sync
commit_message = "bd sync"    # Default commit message
```

### Hooks

```toml
[hooks]
pre_commit = true             # Enable pre-commit hook
post_merge = true             # Enable post-merge hook
pre_push = true               # Enable pre-push hook
```

### Deletions

```toml
[deletions]
retention_days = 30           # Keep deletion records for N days
prune_on_sync = true          # Auto-prune old records
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `BEADS_DB` | Database path |
| `BEADS_LOG_LEVEL` | Log level |
| `BEADS_CONFIG` | Config file path |

## Per-Command Override

```bash
# Override database
bd --db /tmp/test.db list
```

## Example Configuration

`.beads/config.toml`:

```toml
[id]
prefix = "myproject"
hash_length = 6

[import]
orphan_handling = "resurrect"
dedupe_on_import = true

[git]
auto_commit = true
auto_push = true

[deletions]
retention_days = 90
```

## Viewing Active Configuration

```bash
bd info --json | jq '.config'
```
