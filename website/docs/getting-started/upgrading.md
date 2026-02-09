---
id: upgrading
title: Upgrading
sidebar_position: 4
---

# Upgrading bd

How to upgrade bd and keep your projects in sync.

## Checking for Updates

```bash
# Current version
bd version

# What's new in recent versions
bd info --whats-new
bd info --whats-new --json  # Machine-readable
```

## Upgrading

### Homebrew

```bash
brew upgrade beads
```

### go install

```bash
go install github.com/steveyegge/beads/cmd/bd@latest
```

### From Source

```bash
cd beads
git pull
go build -o bd ./cmd/bd
sudo mv bd /usr/local/bin/
```

## After Upgrading

**Important:** After upgrading, update your hooks and restart daemons:

```bash
# 1. Check what changed
bd info --whats-new

# 2. Update git hooks to match new version
bd hooks install

# 3. Restart all daemons
bd daemons killall

# 4. Check for any outdated hooks
bd info  # Shows warnings if hooks are outdated
```

**Why update hooks?** Git hooks are versioned with bd. Outdated hooks may miss new auto-sync features or bug fixes.

## Database Migrations

After major upgrades, check for database migrations:

```bash
# Inspect migration plan (AI agents)
bd migrate --inspect --json

# Preview migration changes
bd migrate --dry-run

# Apply migrations
bd migrate

# Migrate and clean up old files
bd migrate --cleanup --yes
```

## Daemon Version Mismatches

If you see daemon version mismatch warnings:

```bash
# List all running daemons
bd daemons list --json

# Check for version mismatches
bd daemons health --json

# Restart all daemons with new version
bd daemons killall --json
```

## Troubleshooting Upgrades

### Old daemon still running

```bash
bd daemons killall
```

### Hooks out of date

```bash
bd hooks install
```

### Database schema changed

```bash
bd migrate --dry-run
bd migrate
```

### Import errors after upgrade

Check the import configuration:

```bash
bd config get import.orphan_handling
bd import -i .beads/issues.jsonl --orphan-handling allow
```
