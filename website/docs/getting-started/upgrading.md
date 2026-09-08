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
| go install (server-mode only) | macOS, Linux, FreeBSD, Windows | `CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest` |
| go install (embedded-capable) | macOS, Linux, Windows | `CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest` |
| npm | macOS, Linux, Windows | `npm update -g @beads/bd` |
| bun | macOS, Linux, Windows | `bun install -g --trust @beads/bd` |
| From source (Unix shell) | macOS, Linux, FreeBSD | `git pull && make build` |

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

<!-- Canonical Homebrew tap-migration snippet. The installation page links
     here; docs/INSTALLING.md mirrors this block. Keep all three in sync. -->
If you still have the old tap formula installed as `bd`, switch to the
Homebrew core formula:

```bash
brew uninstall bd
brew untap gastownhall/beads 2>/dev/null || true
brew untap steveyegge/beads 2>/dev/null || true
brew install beads
```

### go install

```bash
# Server-mode only
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest

# Embedded-capable
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

### From Source

```bash
cd beads
git pull
make build
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

**Why update hooks?** Git hooks are versioned with bd. Outdated hooks may miss export refresh, legacy fallback, or safety fixes.

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

### Remote-backed databases and multiple clones

`bd` refuses to silently apply pending schema migrations to a database that has
a Dolt remote configured. Migrating more than one clone of a shared remote
independently forks the schema, after which `bd dolt pull` can no longer merge —
the break is silent and, across a primary-key-reshaping migration, unrecoverable
([#4259](https://github.com/gastownhall/beads/issues/4259)). The supported flow
is: one machine migrates and publishes; every other clone re-clones the migrated
database.

This applies to **every** upgrade that crosses a pending migration on a
remote-backed database — the same procedure whether you are moving to a
prerelease or to a stable release.

The gate is **state-aware by default**
([#4516](https://github.com/gastownhall/beads/issues/4516)): before blocking,
`bd` consults the remote's *cached* schema state and

- **auto-migrates** when the remote is at the same schema version as this
  clone — no one has migrated yet, so this clone is a safe first-mover
  (concurrent first-movers converge to identical tables). It reminds you to
  `bd dolt push` afterwards.
- **stops and directs you to adopt** (`bd bootstrap`) when the remote has
  already been migrated by another clone.
- **stops for a human decision** when this clone and the remote applied
  different content for the same migration (a genuine fork), or when the
  remote's schema state cannot be read from the cached ref.

Set `BD_SMART_GATE=0` to opt out and make the gate block unconditionally.
The recipes below are the explicit path and work the same in either mode.

**Important ordering:** once the new binary is installed, a database with
pending migrations is gated on **every** open — `bd dolt push` and `bd dolt
pull` are refused too, not just `bd migrate`. So do all syncing with your
**current** binary, *before* you install the new one.

**Back up before you migrate.** Schema migrations assume the database matches
the shape the previous migrations left behind; real databases sometimes drift
(interrupted writes, tooling bugs, very old bootstraps). A JSONL export is
cheap, issue-complete, and importable by any bd version:

```bash
bd export --all -o .beads/backup/pre-migrate-$(date +%Y%m%d).jsonl
```

`bd export` captures issues, not Dolt history or config — for a full snapshot
also copy the `.beads` directory (or `dolt backup` in server mode) while no
`bd` command is running.

**Single clone (including a solo user with a remote):**

```bash
bd dolt push                              # 1. CURRENT binary: publish all local work
bd export --all -o .beads/backup/pre-migrate.jsonl   # 2. backup (see above)
# 3. install the new binary (see Upgrading above)
BD_ALLOW_REMOTE_MIGRATE=1 bd migrate      # 4. migrate as the designated migrator
bd dolt push                              # 5. publish the migrated schema
bd version                                # 6. confirm the new version is active
```

**Multiple clones sharing one remote:**

```bash
# 1. With your CURRENT (old) binary, on EVERY clone: publish all work and get in
#    sync, then stop editing until the upgrade is done.
bd dolt push
bd dolt pull

# 2. Designated migrator ONLY: back up, install the new binary, then migrate
#    and publish.
bd export --all -o .beads/backup/pre-migrate.jsonl
BD_ALLOW_REMOTE_MIGRATE=1 bd migrate
bd dolt push

# 3. Every OTHER clone: install the new binary, then ADOPT the migrated database.
#    (bd dolt pull is refused here — the clone still has pending migrations — so
#    re-clone instead. Safe because step 1 already pushed all work.)
bd bootstrap
```

`bd bootstrap` replaces the local database, so any work not pushed in step 1 is
lost — that is why step 1 publishes everything first. If a clone was instead
migrated independently and `bd dolt pull` later fails with `cannot merge because
table dependencies has different primary keys in its common ancestor`, the
schema has already forked — follow the recovery playbook:
[RECOVERY.md#pk-fork-refused](https://github.com/gastownhall/beads/blob/main/docs/RECOVERY.md#pk-fork-refused).

:::note
In **server mode**, `bd doctor` adds a migration-content-skew check that flags a
forked schema against the cached remote ref — a useful post-upgrade
verification. It is not available in embedded mode; there, confirm the upgrade
with `bd version` and a normal read such as `bd ready`.
:::

## Cross-era Upgrades

If you're upgrading from a much older version of bd, your project may use a different storage backend. bd has gone through several storage eras:

| Era | Versions | Storage | 
|---|---|---|
| SQLite | v0.30–v0.50 | `.beads/beads.db` |
| Dolt server | v0.50–v0.58 | `.beads/dolt/` (external server) |
| Embedded Dolt (old) | v0.59–v0.63.2 | `.beads/dolt/` (in-process) |
| Embedded Dolt (current) | v0.63.3+ | `.beads/embeddeddolt/` |

### From v0.63.3+ (current era)

Upgrade the binary and run:

```bash
bd migrate
```

If the project was initialized before `bd init` automatically wired git origin
as the Dolt remote, verify the remote after upgrading:

```bash
bd dolt remote list
```

When the list is empty, fix it on the machine whose local database is
authoritative:

```bash
bd export -o .beads/issues.pre-remote.jsonl   # optional issue audit export
bd dolt remote add origin git+ssh://git@github.com/org/repo.git
bd dolt push
```

Commit the resulting `.beads/config.yaml` change so other clones can run
`bd bootstrap` or `bd dolt pull`.

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
