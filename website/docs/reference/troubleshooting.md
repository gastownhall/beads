---
id: troubleshooting
title: Troubleshooting
sidebar_position: 4
---

# Troubleshooting

Common issues and solutions.

## Installation Issues

### `bd: command not found`

```bash
# Check if installed
which bd
go list -f {{.Target}} github.com/steveyegge/beads/cmd/bd

# Add Go bin to PATH
export PATH="$PATH:$(go env GOPATH)/bin"

# Or reinstall
go install github.com/steveyegge/beads/cmd/bd@latest
```

### `zsh: killed bd` on macOS

CGO/SQLite compatibility issue:

```bash
CGO_ENABLED=1 go install github.com/steveyegge/beads/cmd/bd@latest
```

### Permission denied

```bash
chmod +x $(which bd)
```

## Database Issues

### Database not found

```bash
# Initialize beads
bd init --quiet

# Or specify database
bd --db .beads/beads.db list
```

### Database locked

```bash
# Stop the Dolt server if running
bd dolt stop

# Try again
bd list
```

### Corrupted database

```bash
# Check and fix database
bd doctor --fix

# Or pull from Dolt remote
bd dolt pull

# Or restore from a JSONL backup if available
bd import -i backup.jsonl
```

## Dolt Server Issues

### Server not starting

```bash
# Check server health
bd doctor

# Check server logs
cat .beads/dolt/sql-server.log

# Restart the server
bd dolt stop
bd dolt start
```

### Version mismatch

After upgrading bd:

```bash
bd dolt stop
bd dolt start
```

## Sync Issues

### Changes not syncing

```bash
# Force sync
bd sync

# Check hooks
bd hooks status
```

### Import errors

```bash
# Allow orphans
bd import -i backup.jsonl --orphan-handling allow

# Check for duplicates after
bd duplicates
```

### Merge conflicts

```bash
# Check for and fix Dolt conflicts
bd doctor --fix

# Re-sync
bd sync
```

## Git Hook Issues

### Hooks not running

```bash
# Check if installed
ls -la .git/hooks/

# Reinstall
bd hooks install
```

### Hook errors

```bash
# Check hook script
cat .git/hooks/pre-commit

# Run manually
.git/hooks/pre-commit
```

## Dependency Issues

### Circular dependencies

```bash
# Detect cycles
bd dep cycles

# Remove one dependency
bd dep remove bd-A bd-B
```

### Missing dependencies

```bash
# Check orphan handling
bd config get import.orphan_handling

# Allow orphans
bd config set import.orphan_handling allow
```

## Performance Issues

### Slow queries

```bash
# Check database size
ls -lh .beads/beads.db

# Compact if large
bd admin compact --analyze
```

### High memory usage

```bash
# Reduce cache
bd config set database.cache_size 1000
```

## Getting Help

### Debug output

```bash
bd --verbose list
```

### Logs

```bash
cat .beads/dolt/sql-server.log
```

### System info

```bash
bd info --json
```

### File an issue

```bash
# Include this info
bd version
bd info --json
uname -a
```

Report at: https://github.com/steveyegge/beads/issues
