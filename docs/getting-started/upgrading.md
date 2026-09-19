---
title: Upgrading
description: Upgrade the bd binary, refresh git hooks, run schema migrations, and handle remote-backed and cross-era databases
---

How to upgrade bd and keep your projects in sync.

## Checking for Updates

```bash
# Current version
bd version

# What changed since the version you were running — the delta, not the archive
bd upgrade review
bd upgrade review --json  # Machine-readable

# The full release history (large; every version bd knows about)
bd info --whats-new
```

## Short Version

1. With your current `bd`, sync remote-backed databases before installing the
   new binary:
   `bd dolt push`
   `bd dolt pull`
2. Back up **with that same current binary**, before installing:
   `bd export --all -o .beads/backup/pre-migrate-$(date +%Y%m%d).jsonl`
3. Upgrade using the command that matches your install method.
4. After upgrading:
   `bd upgrade review`
   `bd hooks install`
   `bd version`
5. If crossing a schema migration on a remote-backed database, only the
   designated migrator runs:
   `bd migrate`
   `bd dolt push`

Other clones should install the new binary and run `bd bootstrap`, not
independently migrate. The full procedure is below.

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

{/* Canonical Homebrew tap-migration snippet. The installation page links
    here; keep the two in sync. */}
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
bd migrate --yes
```

### Upgrading to 1.3.0

A 1.2.2 or 1.1.x database sits at schema v53, and 1.3.0 knows v66. That is more
than a routine upgrade: 1.2.2 was a recovery release that shipped the 1.1 code
under a higher version number, so there is a release line's worth of schema
between the two.

#### Back up first, with the binary you have now

Do this **before** you install 1.3.0. Under the new binary, `bd export` triggers
the migration before it exports, so a snapshot taken afterwards is a
post-migration snapshot and cannot protect you against the migration going
wrong. The same ordering rule as the section below applies: do all syncing with
your **current** binary, since once 1.3.0 is installed the pending-migration
gate refuses `bd dolt push` and `bd dolt pull` too.

```bash
# with your CURRENT bd:
bd dolt push                                                   # remote-backed stores only
bd export --all -o .beads/backup/pre-1.3.0-$(date +%Y%m%d).jsonl
```

For a Dolt-native snapshot that keeps history and config, configure a
destination and sync it. Bare `bd backup` takes no backup — it is a command
group that prints help and exits 0:

```bash
bd backup init <path-or-dolthub-url>   # once, to configure a destination
bd backup sync                         # take the snapshot
```

#### What the migration looks like

On an embedded or local store, the first command you run after installing
applies the whole set, in place. (A shared `dolt sql-server` is never
auto-migrated — see [Shared servers](#shared-servers) below.) The main series
runs 0054 → 0066 (13 migrations), and then the clone-local series runs through
the same printer with its own numbering, 0012 → 0026 — so it is about
**28 migrations**, and the counter visibly restarts partway through:

```
Applying migration 0065: widen_wisp_comments_text…
Applying migration 0066: add_events_journal_actor…
Applying migration 0012: create_leases…      ← clone-local series, not a restart
```

A counter jumping backwards looks exactly like a loop. It is not one — let it
finish. Expect the run to take noticeably longer than the commands after it
(two passes rewrite rows rather than reshaping tables). It is crash-resumable
and picks up where it left off.

Those lines go to stderr, and only when stderr is a terminal, so a piped or CI
upgrade prints nothing at all. Silence there is not a stall either.

#### Upgrade every client that shares a store, together

A bd binary refuses a database migrated past the schema it knows, rather than
proceeding blind. One machine upgrading takes the shared store forward and every
client still on 1.2.2 stops working. Check for a second binary earlier in your
`PATH` with `which -a bd`, and restart any long-running `bd serve` — noting that
`bd --readonly serve` is now refused outright, so a server scripted with that
flag will not come back up until you drop it.

If the store is a shared `dolt sql-server` rather than a local one, follow
[Shared servers](#shared-servers) below: 1.3.0 will not migrate it without
explicit consent, precisely so the fleet upgrade can come first.

If you need to go back, the rollback is a schema-cursor rollback rather than a
downgrade of the data — see
[Accidental v1.2.1 Release](/recovery/accidental-1-2-1-release), whose
procedure is the same for any cursor rollback even though its worked example is
that release.

Once you are on the new binary, `bd upgrade review` prints exactly the changes
between the version you were running and this one. Several commands changed
defaults, so read it before your first session.

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
  `bd dolt push` afterwards. This applies to embedded mode only: a shared
  server always stops for consent, because migrating it changes the schema
  every connected client sees (see [Shared servers](#shared-servers) below).
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
bd migrate                                # 4. migrate as the designated migrator
bd dolt push                              # 5. publish the migrated schema
bd version                                # 6. confirm the new version is active
```

If `bd`'s remote-migrate gate blocks the run, it prints the available options —
migrating here as the designated migrator, adopting the remote's already-migrated
database, or recovering a fork — and asks for an explicit operator decision.
Follow the guidance it prints.

For scripted or CI upgrades where nobody reads the output, run `bd migrate` as
an explicit step in exactly one job, never in all of them. If the gate blocks a
run it prints both the available options and the scripted override that fits
the situation.

**Multiple clones sharing one remote:**

```bash
# 1. With your CURRENT (old) binary, on EVERY clone: publish all work and get in
#    sync, then stop editing until the upgrade is done.
bd dolt push
bd dolt pull

# 2. Designated migrator ONLY: back up, install the new binary, then migrate
#    and publish.
bd export --all -o .beads/backup/pre-migrate.jsonl
bd migrate
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
[the pk-fork-refused runbook](/recovery/init-safety#pk-fork-refused).

<Note>
`bd doctor` includes a migration-content-skew check that flags a forked
schema against the cached remote ref — a useful post-upgrade verification.
It runs in both server and embedded modes.
</Note>

### Shared servers

A server-mode database is served to every `bd` client connected to that
`dolt sql-server`, so a schema migration is not a local event: it promotes the
schema version for **all** of them at once, and clients still running an older
`bd` refuse the database until they are upgraded too. `bd` therefore never
auto-migrates a shared database on a version bump — with or without a remote
configured, though the two cases consent differently
([#5920](https://github.com/gastownhall/beads/issues/5920)).

Upgrade one server's clients like this:

```bash
# 1. Upgrade bd on every client of the server. Reads keep working throughout —
#    an upgraded client reads the old schema, it just cannot write to it.
bd version                     # on each client, confirm the new version

# 2. Once, from a workspace already set up against this server: consent.
bd migrate schema              # add --global for the shared global database

# 3. Confirm.
bd doctor
```

Between steps 1 and 2, an upgraded client reads normally and its writes are
refused with the gate's guidance. Nothing is silently promoted, so there is no
deadline — but the window is a degraded one, so keep it short.

**If the shared server also has a Dolt remote**, step 2 is not enough. Two
hazards now apply at once — the co-resident lockout above and the cross-clone
fork of [Remote-backed databases](#remote-backed-databases-and-multiple-clones)
— so `bd` requires the stronger designated-migrator consent it describes:
`bd migrate --force`, from exactly one machine, followed by `bd dolt push`. If
another clone has already migrated and pushed, adopt its database with
`bd bootstrap` instead of migrating. On a shared server, adopting also promotes
the schema for every client of that server, so step 1 still comes first either
way.

<Warning>
Migrating is one-way for the fleet: after step 2, a client still on the older
`bd` refuses the database until it is upgraded. Do step 1 first, and confirm it
— the gate cannot see the other clients' versions and will take your word for
it.
</Warning>

A new client **joining** a server whose schema is behind is refused before its
workspace is written, so there is nothing to run `bd migrate schema` from
there: do step 2 from a client that is already set up, or join and migrate in
one step with `BD_ALLOW_REMOTE_MIGRATE=1 bd init …`.

The same rules apply in **proxied-server mode**, which is a shared server
reached through a local proxy. Two differences worth knowing:

- read commands print a warning and keep serving the current schema, rather
  than the read simply not touching it;
- `bd serve` refuses to start against a database with pending migrations,
  because a daemon has no operator to consent for it. Reconcile the schema
  first with step 2, then start the daemon — or, for an unattended service,
  put `BD_ALLOW_REMOTE_MIGRATE=1` in its environment as an explicit, auditable
  standing consent.

## Cross-era Upgrades

If you're upgrading from a much older version of bd, inspect the storage layout
and metadata before running the current binary. A `.beads/dolt/` directory alone
does not identify a legacy workspace: supported current server mode uses that
directory too. Current `bd` evaluates explicit server metadata, the presence of
that local root, and the bounded `.local_version` witness together. An explicit
server selection is not overridden by a stale `.beads/embeddeddolt/` repository.

| Storage layout | Upgrade path |
|---|---|
| Current embedded metadata with `.beads/embeddeddolt/` and no explicit server selection | Direct current-era upgrade |
| Explicit server metadata plus `.local_version` from v0.55.4 through v0.62.0, whether or not `.beads/dolt/` exists | Explicit legacy Dolt export/import |
| Explicit server metadata plus `.beads/dolt/` and a witness whose major version is 1 or newer | Normal current server-mode upgrade |
| Explicit server metadata plus `.beads/dolt/` and a missing or pre-v1 witness | Explicit legacy Dolt export/import |
| Explicit server metadata plus `.beads/dolt/` and a witness that is present but unreadable | Normal current server-mode upgrade, with a warning |
| Explicit server metadata without `.beads/dolt/`, and a missing, malformed, or non-historical witness | Normal current server-mode compatibility path |
| `.beads/dolt/` with missing metadata or persisted `dolt_mode` blank/`embedded` | Explicit legacy Dolt export/import, except for the configured shared-server compatibility path described below |
| One `.beads/*.db` file, such as `beads.db` or `vc.db` | Sealed SQLite bridge |

The witness is whatever `bd` held in its own version string when it last touched
the workspace, so it may be a plain release, a release candidate, a build
carrying metadata, or a Go pseudo-version; all of those are read as the version
they name. A witness that is present but unreadable is not treated as a legacy
marker — no pre-v1 `bd` could have written one — so `bd` warns and continues
rather than refusing every command. A *missing* witness stays ambiguous and is
still refused.

Current `bd` refuses recognized historical SQLite and legacy Dolt layouts before
opening storage or rewriting metadata. This is intentional: preserve the source
and complete the matching explicit migration below.

PostgreSQL and MySQL are removed backends, not supported cross-era upgrade
paths. Current `bd` refuses metadata that selects either backend, and the sealed
bridge below accepts SQLite sources only.

### `.beads/embeddeddolt/`: direct upgrade

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

### Historical Dolt server mode: explicit migration

Do not run current `bd init --force` when `.beads/dolt/` has missing metadata
or persisted `dolt_mode` is blank/`embedded`. Those old embedded layouts are
never current embedded storage. The same explicit path applies when metadata
selects `backend: dolt`, `dolt_mode: server` and `.local_version` records
v0.55.4 through v0.62.0.

First take a native snapshot while every writer is stopped. Restore that
snapshot to a disposable workspace, export it with the verified historical
binary, then import the export into a fresh current project. Keep the original
and snapshot unchanged until the cutover has been reviewed. The SQLite
sealed-copy helper below does not start or manage a Dolt SQL server.

Explicit server metadata with a v0.55.4–v0.62.0 witness is always refused,
including when there is no local `.beads/dolt/` root. When that root does exist,
the guard admits explicit server mode only with a syntactically valid witness
whose major version is 1 or newer; a missing, malformed, or pre-v1 witness fails
closed. Without the local root, a missing, malformed, or non-historical witness
is admitted only as a compatibility layout.

The configured shared-server compatibility path applies only when persisted
metadata is missing or leaves `dolt_mode` blank/`embedded`; it does not override
an explicit server selection with a local root. Compatibility admission cannot
prove that a workspace is modern. If you know it was created by v0.55.4 through
v0.62.0, use this explicit bridge even when its witness was lost or damaged.
Otherwise, follow the normal `bd migrate --dry-run` and `bd migrate` flow for
an admitted server workspace.

### One `.beads/*.db` file: sealed SQLite bridge

The old binary stored data in SQLite. The new binary uses Dolt.

**Recommended: use the sealed-copy bridge** (requires `jq`):

Stop every process that can write the old workspace before starting.
Run this from a source checkout at the exact commit you intend to run; installed
binaries do not include repository scripts. Record that commit with
`git rev-parse HEAD` before executing the script. Download the old `bd` asset
only from the official `gastownhall/beads` release and verify the asset with its
published SHA-256:

```bash
sha256sum -c checksums.txt --ignore-missing
```

On macOS or BSD, use `shasum -a 256` on the downloaded archive and compare
the result with that archive's entry in `checksums.txt`.

```bash
scripts/migrate-legacy-to-current.sh \
  --source /absolute/path/to/old-project \
  --destination /absolute/path/to/old-project-cutover \
  --source-version v0.50.3 \
  --old-bd /absolute/path/to/verified-old-bd \
  --new-bd /absolute/path/to/current-bd \
  --prefix beads
```

For a source older than v0.49.6, also supply an authenticated v0.49.6 binary:

```bash
scripts/migrate-legacy-to-current.sh \
  --source /absolute/path/to/old-project \
  --destination /absolute/path/to/old-project-cutover \
  --source-version v0.17.0 \
  --old-bd /absolute/path/to/verified-old-bd \
  --canonicalizer-bd /absolute/path/to/verified-v0.49.6-bd \
  --new-bd /absolute/path/to/current-bd \
  --prefix beads
```

Verify the historical binary against its official release checksum before
running the bridge. The script verifies each binary's reported version, rejects
Dolt and ambiguous layouts, retains a sealed source copy, and compares the
candidate export with the canonical historical export. Activate the cutover
manually only after reviewing those retained artifacts.

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
