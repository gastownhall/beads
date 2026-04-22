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

Use the command that matches your install method.

| Install method | Platforms | Command |
|---|---|---|
| Quick install script | macOS, Linux, FreeBSD | `curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh \| bash` |
| PowerShell installer | Windows | `irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 \| iex` |
| Homebrew | macOS, Linux | `brew upgrade beads` |
| go install | macOS, Linux, FreeBSD, Windows | `go install github.com/steveyegge/beads/cmd/bd@latest` |
| npm | macOS, Linux, Windows | `npm update -g @beads/bd` |
| bun | macOS, Linux, Windows | `bun install -g --trust @beads/bd` |
| From source (Unix shell) | macOS, Linux, FreeBSD | `git pull && go build -o bd ./cmd/bd` |

### Quick install script (macOS/Linux/FreeBSD)

```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### PowerShell installer (Windows)

```pwsh
irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex
```

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

**Important:** After upgrading, update your hooks:

```bash
# 1. Check what changed
bd info --whats-new

# 2. Update git hooks to match new version
bd hooks install

# 3. Check for any outdated hooks
bd info  # Shows warnings if hooks are outdated

# 4. If using Dolt backend, restart the server
bd dolt stop && bd dolt start
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

## Cross-era Upgrades

If you're upgrading from a much older version of bd, your project may use a different storage backend. bd has gone through several storage eras:

| Era | Versions | Storage | 
|---|---|---|
| SQLite | v0.30–v0.50 | `.beads/beads.db` |
| Dolt server | v0.50–v0.58 | `.beads/dolt/` (external server) |
| Embedded Dolt (old) | v0.59–v0.63.2 | `.beads/dolt/` (in-process) |
| Embedded Dolt (current) | v0.63.3+ | `.beads/embeddeddolt/` |

### From v0.63.3+ (current era)

No special steps needed. Just upgrade the binary and run:

```bash
bd migrate
```

### From v0.59–v0.63.2 (old embedded)

Direct upgrade works automatically:

```bash
# Just use the new binary — it handles the conversion
bd list
```

### From v0.50–v0.58 (Dolt server era)

The old binary used an external Dolt SQL server. The new binary uses an embedded engine.

```bash
# 1. Export your data while the old binary still works
bd list --json -n 0 --all > .beads/issues.jsonl

# 2. Stop the Dolt server
dolt sql-server --stop  # or kill the process

# 3. Remove stale server metadata and old storage directories
rm -f .beads/metadata.json .beads/config.json
rm -rf .beads/dolt .beads/embeddeddolt

# 4. Initialize with the new binary
bd init --from-jsonl --quiet

# 5. Verify
bd list --all
```

### From v0.30–v0.50 (SQLite era)

The old binary stored data in SQLite. The new binary uses Dolt.

**Recommended: use the migration script** (requires `sqlite3` and `jq`):

```bash
# Download the script from the beads repo
curl -fsSLO https://raw.githubusercontent.com/gastownhall/beads/main/scripts/migrate-sqlite-to-current.sh
chmod +x migrate-sqlite-to-current.sh

# Run it in your project directory
./migrate-sqlite-to-current.sh
```

The script exports issues, dependencies, and labels from SQLite, handles type normalization, and imports everything into the new Dolt backend.

**Alternative: manual export with the old binary.** Old binaries are always available on [GitHub Releases](https://github.com/gastownhall/beads/releases). Download the version that matches your project, then:

```bash
# 1. Export with the old binary
./bd-old list --json -n 0 --all > .beads/issues.jsonl

# 2. Import with the current binary
bd init --from-jsonl --quiet

# 3. Verify
bd list --all
```

> **Note:** The manual export preserves issue content but not dependencies or labels. Use the migration script for a more complete transfer.

## Troubleshooting Upgrades

### Hooks out of date

```bash
bd hooks install
```

### Database schema changed

```bash
bd migrate --dry-run
bd migrate
```

### Recovery after upgrade

If you need to restore from a backup:

```bash
bd init
bd backup restore [path] --force
```

Or pull from a Dolt remote:

```bash
bd dolt pull
```
