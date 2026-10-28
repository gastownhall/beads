---
title: Dolt Backend for Beads
description: "How beads uses Dolt for versioned issue storage: embedded vs server mode, remotes, sync, and backups"
---

Beads uses Dolt as its storage backend. Dolt provides a version-controlled SQL database with cell-level merge, native branching, and two deployment modes.

## Why Dolt?

- **Native version control** — cell-level diffs and merges, not line-based
- **Multi-writer support** — server mode enables concurrent agents
- **Built-in history** — every write creates a Dolt commit
- **Native branching** — Dolt branches independent of git branches
- **Single-binary option** — embedded mode for solo users (no server needed)

## Getting Started

### Install Dolt (Server Mode Only)

Embedded mode includes everything in the `bd` binary; no separate Dolt install
is needed. Install the standalone `dolt` CLI only when you want to run server
mode or work directly with the database via `dolt sql`.

Install a specific version. Do not install `releases/latest` — see
[Which Dolt version to install](#which-dolt-version-to-install) for the
version to use and why the pin exists.

```bash
DOLT_VERSION=2.6.0   # see "Which Dolt version to install" below

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m | sed -e 's/^x86_64$/amd64/' -e 's/^aarch64$/arm64/')
curl -fsSL "https://github.com/dolthub/dolt/releases/download/v${DOLT_VERSION}/dolt-${os}-${arch}.tar.gz" \
  | tar -xz -C /tmp
sudo install -m 0755 "/tmp/dolt-${os}-${arch}/bin/dolt" /usr/local/bin/dolt

# Verify you got the version you asked for
dolt version
```

`brew install dolt` installs whatever version the Homebrew formula currently
points at, which is not pinned. If you install that way, check `dolt version`
against the pin below.

### Which Dolt version to install

Beads pins Dolt to **2.6.0**. CI installs that same pin with
`scripts/ci/install-dolt.sh`, which records the per-version measurements and
the criterion for raising it; raise the pin here and in that script together.

This pin is about the standalone `dolt` CLI, which only server and
proxied-server mode use. Embedded mode is unaffected either way: it links the
Dolt 2.6.0 engine into `bd` (`dolthub/driver` v2.4.1 with
`github.com/dolthub/dolt/go` at the v2.6.0 release, in `go.mod`), no matter
which `dolt` CLI is on your PATH. The CLI pin tracks that engine, so every mode
runs the same Dolt.

**`bd` requires Dolt 2.6.0 or newer** outside embedded mode, and refuses to
run against anything older:

- `bd dolt start`, and the auto-start behind server mode, refuse to launch a
  `dolt` binary older than 2.6.0.
- Managed proxied-server mode (`bd init --proxied-server`, and every command
  after it) refuses a `dolt` binary older than 2.6.0 before it launches it.
- Every server-mode and proxied-server store open asks the server it
  reached for `SELECT dolt_version()` and refuses a server older than 2.6.0,
  whoever started it, before it reads or writes through that open. That
  includes an external server and a shared one.
- `bd bootstrap` and `bd init --remote` check the server, or the `dolt`
  CLI, before they clone; `bd init` checks a shared server before it
  creates `beads_global` on it; and `bd dolt clean-databases` refuses an
  older server before it drops anything.
- A `bd dolt push`, `pull` or fetch (federation sync included) that runs
  through the `dolt` binary on your `PATH` (git-protocol, credentialed,
  cloud-auth and local remotes) refuses that binary when it is older than
  2.6.0, like every other `dolt` `bd` runs. When `bd`'s own server holds the
  database, dolt hands the transfer to that server; otherwise the binary's
  own engine runs it. The check comes as soon as `bd` chooses the CLI route,
  before it runs anything through the binary.
- The remote-repo cache (`bd create --repo <url>`, `bd repo sync` for a
  remote-URL repo) refuses the `dolt` on your `PATH` when it is older than
  2.6.0, before it clones, pulls or pushes. No server holds the cache, so
  that binary's own engine would run the transfer.
- `bd doctor --server` reports a server older than 2.6.0 as an error.
- `bd doctor`, `bd doctor --fix` included, is exempt, so you can still
  diagnose and repair a workspace on an older server: its own checks and
  repairs connect without the floor. Its checks that open the workspace's
  store fail: the ✖ Database and Permissions errors only point at
  `bd dolt status`, and the refusal itself is printed by the ⚠ checks
  further down (Peer Connectivity, Sync Staleness, Federation Conflicts,
  Dolt Mode). Run `bd doctor --server` to see the version.
- `bd admin compact --dolt` is exempt: its `dolt gc` runs in an owned or
  shared server's database, which the store open has already checked, and
  dolt hands the GC to that running server. Read-only probes
  (`bd dolt status`, `bd init`'s existing-database check) and the
  working-set commit `bd dolt stop` makes before it stops a server `bd` runs
  are not checked either.
- Federation peers are not checked: `bd` checks only its own server and its
  own `dolt` binary, so a peer on an older engine can still merge the same
  criss-cross history differently (see below).

The error names what was checked (the binary's path or the server's
address), the version it reported, the 2.6.0 minimum, and where to get Dolt.
To fix it, install 2.6.0 with the steps above (or `brew upgrade dolt`, then
check `dolt version`) and restart any `dolt sql-server` on the new binary:
for a server `bd` manages, `bd dolt stop` and `bd dolt start`; in a managed
proxied-server workspace, `bd dolt stop` alone, since the proxy starts its
server again on the next command and refuses `bd dolt start`. There is no
override. A version `bd` cannot determine is not refused: a `dolt` binary
whose `dolt version` output does not parse runs with a warning (proxied-server
mode) and a server that does not answer `dolt_version()` with a version is
used without the check (logged under `BD_DEBUG`). The
[Dolt 2.6.0 release notes](https://github.com/dolthub/dolt/releases/tag/v2.6.0)
list what changed. Two changes matter to `bd` users moving a server up to
2.6.0:

- When two histories have more than one merge base (criss-cross merges,
  e.g. federation peers that pull from each other, or branches merged into
  each other), Dolt 2.6.0 merges against a virtual base like git's ORT
  strategy (new in 2.6.0, dolthub/dolt#12125). Some merges that 2.5.x
  completed silently now conflict, and some spurious conflicts no longer
  occur. Pull/push through a single remote is unaffected.
- Dolt reports MySQL 8.0's strict default `sql_mode`, which includes
  `NO_ZERO_IN_DATE`, `NO_ZERO_DATE` and `ERROR_FOR_DIVISION_BY_ZERO`, so
  anything that reads `@@sql_mode` sees it. Dolt has done so since 2.3.3,
  so this is new only to a server coming from a release before 2.3.3.

Dolt 2.3.3 is the first release that stores a descending index column as
descending (dolthub/dolt#11746), and older releases cannot read a table that
holds one: even a plain `INSERT` fails with `table has unknown fields`, and an
`UPDATE` or `DELETE` on `issues` fails with `cannot add or update a child row:
a foreign key constraint fails` on `fk_comp_snap_issue`. Migration 0010
declares `compaction_snapshots.idx_comp_snap_issue` with `created_at DESC`. So
every database `bd` creates in embedded mode from this release on, and every
database whose schema a `dolt sql-server` 2.3.3 or newer created, holds one.
**This is one-way: `bd` does not convert the index back**, so such a database
needs Dolt 2.3.3 or newer, and a `bd` built on Dolt 2.3.3 or newer, for good.
Other databases keep an ascending index and stay readable and writable by
older Dolt even after this release migrates and writes to them (measured
2026-10-21 with Dolt 2.2.0 and 2.3.2).

Dolt 2.3.0 (released 2026-08-13) regressed `CALL DOLT_RESET('--hard')`. A few
percent of freshly created databases come up with that procedure unusable —
every call answers `Error 1105 (HY000): context canceled`, from any session
and any new connection, for the life of that server process. Nothing else
about the database looks wrong: `SELECT 1`, `CALL DOLT_CLEAN()`,
`CALL DOLT_CHECKOUT('.')`, `CALL DOLT_COMMIT()` and soft resets all work
normally, so the damage is invisible until something needs a hard reset.

The table records, per release, whether it opens a database `bd` creates,
whether its engine answers an ungrouped `MAX` or `MIN` over an unmatched
primary-key prefix correctly (the shape of dolthub/dolt#12093), and the
`DOLT_RESET('--hard')` regression, measured by creating fresh databases and
immediately calling the procedure:

| Dolt | Opens a database `bd` creates | Ungrouped `MAX`/`MIN` over an unmatched primary-key prefix | Fresh databases with `DOLT_RESET('--hard')` broken |
|---|---|---|---|
| 2.1.8 | no | correct | 0 / 40 |
| 2.2.0 | no | correct | 0 / 60 |
| 2.3.0 | no | correct | 3 / 60 |
| 2.3.1 | no | correct | 3 / 100 (2026-08-20); 8 / 150 (2026-10-10); 4 / 300 (2026-10-21) |
| 2.3.2 | no | correct | not measured (carries the fix, dolthub/dolt#11652) |
| 2.3.3 – 2.3.5 | **yes (the floor)** | correct | not measured |
| 2.4.0 | yes | **no row** (dolthub/dolt#12093) | not measured |
| 2.4.1 | yes | **no row** | 0 / 250 |
| 2.4.2 | yes | **no row** | 0 / 250 |
| **2.5.0** | yes | correct | 0 / 500 |
| **2.5.1** | yes | correct | 0 / 500 |
| **2.6.0** (pinned) | yes | correct | 0 / 500; 0 / 1000 across fresh servers (2026-10-24) |

"Opens a database `bd` creates" means `information_schema` reads and writes
don't fail with `table has unknown fields`. Every row of that column is
measured (2026-10-23, on a database `bd` created in embedded mode); plain
reads of `issues` still succeed on the releases marked "no".

- The embedded engine measured 0 / 100 on 2.5.1 and 0 / 100 on 2.4.1
  (2026-10-21), and 0 / 60 on 2.4.1 (2026-10-10). The 2026-10-10 and
  2026-10-21 runs re-measured 2.3.1 as a positive control, confirming the
  method still reproduces the regression.
- 2.6.0 (2026-10-24): 0 / 500 on one server, and 0 / 1000 across 50 fresh
  servers of 20 databases each; the embedded engine (`go.mod`'s
  `dolthub/dolt/go` at the v2.6.0 release) 0 / 100 on one open and 0 / 400
  across 20 fresh opens. The regression hits databases created soon after a
  server starts, so one long-running server under-reports it: that day 2.3.1,
  the positive control, measured 0 / 300 on one server but 31 / 600 across 30
  fresh servers of 20 databases each. Measure across fresh servers.
- 2.6.0: when two histories have more than one merge base (criss-cross
  merges, e.g. federation peers that pull from each other, or branches merged
  into each other), it merges against a virtual base like git's ORT strategy
  (dolthub/dolt#12125). Some merges that 2.5.x completed silently now
  conflict, and some spurious conflicts no longer occur. Pull/push through a
  single remote is unaffected.
- The `MAX`/`MIN` column is bee-ghosttrack's reproduction from
  [#7646](https://github.com/gastownhall/beads/pull/7646), reproduced on
  linux/amd64 and darwin/arm64. `bd`'s one query of that shape, the
  versioned-history revision read, no longer depends on it.
- 2.4.0 through 2.4.2 also panic on an ungrouped `MAX` of a non-key column under
  a fully bound primary key (dolthub/go-mysql-server#4037).
- "Correct" is for the shape the column names. Every release before 2.5.0,
  2.2.0 included, returns no row for an ungrouped `MAX` over the whole primary
  key of an empty table (`SELECT COALESCE(MAX(pk), 0) FROM t`); 2.5.0 and
  later return one row. `bd`'s production queries of that shape accept either
  answer.
- Releases before 2.4.0 drop a join condition when an `ON` clause has an `OR`
  over indexed columns (dolthub/dolt#11886).

Raise the pin only once a newer release is confirmed clean by that same
measurement — not because it is newer.

Pin rather than track `latest` for a second, independent reason: the upstream
`releases/latest` URL resolves to the most recently *created* release, not the
highest version, so it can move backwards — v1.88.2 was created on
2026-08-17, after both v2.2.4 and v2.3.0.

#### If you are already running 2.3.x

Whether a database is affected is decided per database, when it is created —
two databases on the same server can differ — so check each one you care
about, against the server that is actually serving it:

```bash
# 1. The working set must be clean first: on a dirty working set a hard
#    reset discards uncommitted changes. If this returns any rows, commit
#    or clean up before step 3.
dolt sql -q "SELECT * FROM dolt_status"

# 2. Control — this must succeed. If it does not, you have a different
#    problem and step 3 proves nothing.
dolt sql -q "SELECT 1"

# 3. On a clean working set this is a no-op on a healthy database.
dolt sql -q "CALL DOLT_RESET('--hard')"
```

Step 2 succeeding while step 3 answers `Error 1105 (HY000): context canceled`
is the discriminating result — that database is affected. Both succeeding
means it is not.

The breakage lives in the running server process, not on disk, so restarting
`dolt sql-server` clears it — verified by re-running the check on an affected
database after a restart, with a healthy database on the same server as the
control. A restart re-rolls the dice for every database, though, so the
durable fix is to move to the pinned version.

This matters because `bd flatten` and the Dolt-history compaction in
`bd admin compact` both finish by hard-resetting `main` onto a temporary
branch, and the merge-settle path behind `bd dolt pull` / `bd sync` falls
back to a hard reset when it has to abandon a merge. See
[Maintenance](#maintenance--bd-prune-and-bd-purge).

### New Project

```bash
# Embedded mode (single writer, no server — default for standalone)
bd init

# Server mode (multi-writer, e.g. orchestrator)
gt dolt start           # Start the Dolt server
bd init --server        # Initialize with server mode
```

### Migrate from SQLite (Legacy)

If upgrading from an older version that used SQLite:

> **Note:** The `bd migrate --to-dolt` command was removed in v0.58.0.
> For pre-0.50 installations with JSONL data, use the migration script:
>
> ```bash
> scripts/migrate-jsonl-to-dolt.sh
> ```
>
> See [Troubleshooting](/reference/troubleshooting#circuit-breaker-server-appears-down-failing-fast) if you encounter connection errors after migration.

Migration creates backups automatically. Your original SQLite database is preserved as `beads.backup-pre-dolt-*.db`.

## Modes of Operation

```mermaid
flowchart LR
    subgraph embedded["Embedded mode (default) — bd init"]
        bd1["bd process<br/>Dolt runs in-process"] --> d1[(".beads/embeddeddolt/<br/>single writer, file-locked")]
    end
    subgraph server["Server mode — bd init --server"]
        bd2["bd (agent 1)"] --> srv["dolt sql-server"]
        bd3["bd (agent 2)"] --> srv
        srv --> d2[(".beads/dolt/<br/>concurrent writers")]
    end
```

### Embedded Mode (Solo / Standalone)

In-process Dolt engine — no separate server needed. This is the default for
standalone Beads users. The `bd` binary includes everything; just `bd init` and go.

- Single-writer (one process at a time)
- Data lives in `.beads/embeddeddolt/` alongside your code
- Push to GitHub with `bd dolt push` — code and issues in one repo
- Zero ops: no server, no ports, no PID files

### Server Mode (Multi-Writer / Orchestrator)

Connects to a running `dolt sql-server` for multi-client access.

```bash
# Start the server (orchestrator)
gt dolt start

# Or manually
cd ~/.dolt-data/beads && dolt sql-server --port 3307
```

```bash
# Initialize in server mode
bd init --server

# Or switch via environment variable
export BEADS_DOLT_SERVER_MODE=1
```

```yaml
# .beads/config.yaml (server mode settings)
dolt:
  mode: server
  host: 127.0.0.1
  port: 3307
  user: root
```

Configure the connection with flags or environment variables:

| Flag | Env Var | Default |
|------|---------|---------|
| `--server-host` | `BEADS_DOLT_SERVER_HOST` | `127.0.0.1` |
| `--server-port` | `BEADS_DOLT_SERVER_PORT` | `3307` |
| `--server-socket` | `BEADS_DOLT_SERVER_SOCKET` | (none; uses TCP) |
| `--server-user` | `BEADS_DOLT_SERVER_USER` | `root` |
| | `BEADS_DOLT_PASSWORD` | (none) |

**Host order (GH#5961):** `BEADS_DOLT_SERVER_HOST`, then `dolt.host` from
config (`BD_DOLT_HOST`, `.beads/config.local.yaml`, `.beads/config.yaml`, the
user-level config.yaml), then `dolt_server_host` in `.beads/metadata.json`,
then `127.0.0.1`. When a `dolt.host` outranks a different metadata host, `bd`
warns once and names how to keep either. `bd dolt set host` writes both files.

**`BEADS_DOLT_SERVER_PORT` also declares the server externally managed.**
Setting it — or the legacy `BEADS_DOLT_PORT` — makes `ResolveServerMode`
classify the workspace as an externally-managed server, so bd will not
auto-start a server for it: if nothing is listening on that port you get
`BEADS_DOLT_SERVER_PORT declares the Dolt server at 127.0.0.1:<port>
externally managed, so bd did not start one` rather than a server started on
it. Use these vars to point bd at a server you already run; they are not a way
to choose the port of a bd-owned server. A
`dolt_mode: proxied-server` workspace is exempt — it reaches its server
through the proxy, so an ambient port does not describe its lifecycle — and
a shared-server workspace is already classified external on its own.

**Unix domain sockets:** Use `--server-socket` to connect via a Unix socket
instead of TCP. This avoids port conflicts between concurrent projects and is
useful in sandboxed environments (e.g., Claude Code) where file-level access
control is simpler than network allowlists. The Dolt server must be started
with `dolt sql-server --socket <path>`. Auto-start is not supported in socket
mode.

Switch to server mode when you need:
- Multiple agents writing simultaneously
- Orchestrator multi-rig setups
- Federation with remote peers

## Maintenance — `bd prune` and `bd purge`

`bd prune` permanently deletes closed non-ephemeral beads to reclaim storage
and shrink auto-exports. `bd purge` does the same for ephemeral beads (wisps,
transient molecules). Both require `--force` to execute.

```bash
bd prune --older-than 30d              # Preview closed beads >30d old
bd prune --older-than 30d --force      # Delete them
bd prune --older-than 90d --dry-run    # Detailed preview with stats
bd purge --force                       # Delete all closed ephemeral beads
```

**Reference-aware protection:** `bd prune` automatically skips closed beads
whose ID appears in the description, notes, or comments of any open or
in-progress bead. This prevents accidental deletion of ADR, decision, and
verification beads that downstream work still cites. Use
`--ignore-references` to override when cleaning up known-stale references:

```bash
bd prune --older-than 90d --ignore-references --force
```

`bd purge` is unaffected — ephemeral beads' references are themselves
transient. For full Dolt storage reclaim after deleting many rows, follow
with `bd flatten`.

**On Dolt 2.3.x, storage-reclaim operations can fail partway.** `bd flatten`
and the Dolt-history compaction in `bd admin compact` both build a temporary
branch and then hard-reset `main` onto it, and the merge-settle path behind
`bd dolt pull` / `bd sync` falls back to a hard reset when it abandons a
merge. On an affected database that hard reset returns
`Error 1105 (HY000): context canceled`: `bd flatten` and `bd admin compact`
stop at that step, and an abandoned merge is left without its rollback. See
[Which Dolt version to install](#which-dolt-version-to-install) for the check
and the fix.

### Why a garbage collection can reclaim nothing

Dolt's garbage collector is **generational**. Every pass moves the data
reachable at that moment into an *old generation*, and a default pass only
examines the *new generation* — data written since the last collection. So the
second collection on a store never revisits what the first one kept, however
much of it has since become unreachable. Squashing history and then running a
default `dolt gc` is the case where this bites: the orphaned commit chain
usually sits in the old generation, and the pass frees almost nothing.

`dolt gc --full` collects both generations. It frees everything a default pass
frees, plus the old generation, so it is never worth running a default pass
first to save time. If a full collection frees nothing, the remaining bytes are
still referenced — by a branch, a tag, or a cached remote-tracking ref — and
the fix is to remove the reference, not to collect again. The
[History Bloat runbook](/recovery/history-squash) walks through that diagnosis;
[`bd flatten`](/cli-reference/flatten), [`bd compact`](/cli-reference/compact),
and [`bd gc`](/cli-reference/gc) document the collection each one runs.

## Migrating Between Backends

You can migrate data between embedded mode and server mode using `bd backup`.
Both directions preserve full Dolt commit history.

`bd export` is not a substitute for this flow. JSONL exports contain issue
records from the issues table for migration and interoperability; they do not
capture Dolt branches, full commit history, working-set state, or non-issue
tables. Use `bd backup` or a manual Dolt backup when you need a restorable
database backup.

### Server → Embedded

1. **Create a backup from the server-mode project:**

   ```bash
   # In the server-mode project directory
   bd backup init /path/to/backup-dir
   bd backup sync
   ```

2. **Create a new embedded-mode project and restore:**

   ```bash
   mkdir new-project && cd new-project
   bd init                  # creates an embedded-mode project by default
   bd backup restore --force /path/to/backup-dir
   ```

   `--force` overwrites the freshly-initialized database with the backup
   contents. The restore automatically:
   - Updates `metadata.json` to match the restored project identity
   - Registers the backup directory for future `bd backup sync`
   - Backfills the embedded migration tracker (`schema_migrations`)

3. **Verify:**

   ```bash
   bd list
   bd backup status
   ```

### Embedded → Server

1. **Create a backup from the embedded-mode project:**

   ```bash
   # In the embedded-mode project directory
   bd backup init /path/to/backup-dir
   bd backup sync
   ```

2. **Create a new server-mode project and restore:**

   ```bash
   mkdir new-project && cd new-project
   bd init --server         # creates a server-mode project
   bd backup restore --force /path/to/backup-dir
   ```

3. **Verify:**

   ```bash
   bd list
   bd backup status
   ```

### Backup Commands Reference

| Command | Description |
|---------|-------------|
| `bd backup init <path>` | Register a backup destination (filesystem or DoltHub URL) |
| `bd backup sync` | Push database to the configured backup destination |
| `bd backup restore [path]` | Restore from a backup directory (`--force` to overwrite) |
| `bd backup remove` | Unregister the backup destination |
| `bd backup status` | Show backup configuration and last sync time |

### Notes

- Data locations differ between modes: `.beads/embeddeddolt/` (embedded) vs `.beads/dolt/` (server)
- The backup directory is a full Dolt backup, not an `issues.jsonl` export — it can be on a local drive, NAS, or DoltHub
- You can also migrate via Dolt remotes (`bd dolt push` / `bd dolt pull`) if both projects share a remote

The sections below are the canonical backend migration reference.

## Federation (Peer-to-Peer Sync)

Federation lets independent Dolt-backed workspaces ("towns") sync issues
directly with each other via `bd federation add-peer`/`sync`/`status`,
without a central hub. Credentials are AES-256 encrypted and stored locally.

See [Federation Setup Guide](/multi-agent/federation) for the full setup guide, including
peer configuration, sovereignty tiers, sync/status/topology details, and
troubleshooting.

## Dolt Remotes

Use `bd dolt remote add` to configure remotes. This ensures the running Dolt SQL
server sees the remote immediately. Remotes added directly with the `dolt` CLI
are written to filesystem config and may not be visible to the server until
restart.

```bash
# DoltHub (public or private)
bd dolt remote add origin https://doltremoteapi.dolthub.com/org/beads

# S3
bd dolt remote add origin aws://[bucket]/path/to/repo

# GCS
bd dolt remote add origin gs://[bucket]/path/to/repo

# Git SSH (GitHub, GitLab, etc.)
bd dolt remote add origin git+ssh://git@github.com/org/repo.git

# Local file system
bd dolt remote add origin file:///path/to/remote
```

### Push/Pull

```bash
bd dolt push
bd dolt pull
```

`bd dolt remote add` registers the remote through the Dolt store API. SQL
remotes are the source of truth for `bd dolt remote list`, `bd dolt push`, and
`bd dolt pull`.

For git-protocol remotes, credentialed external-server remotes, and cloud
remotes whose credentials are only present in the current shell, `bd dolt push`
and `bd dolt pull` automatically materialize a matching local CLI remote before
using the `dolt` CLI transport. The CLI remote is a local transport mirror, not
a separate configuration source.

If you are upgrading from an older beads version and previously added remotes
with raw `dolt remote add`, re-register them with `bd dolt remote add <name>
<url>` so they are visible through SQL. `bd doctor` reports legacy CLI-only or
mismatched CLI remotes under `Dolt Remote Migration`.

> **Sharing a Git repo**: Dolt stores data under `refs/dolt/data`, separate
> from standard Git refs (`refs/heads/`, `refs/tags/`). You can safely point a
> `git+ssh://` remote at the same repository as your project source code. See
> [Dolt Git Remotes](https://docs.dolthub.com/concepts/dolt/git/remotes).

### List/Remove Remotes

```bash
bd dolt remote list            # Shows SQL-configured remotes
bd dolt remote remove origin   # Removes the remote
```

## Contributor Onboarding (Clone Bootstrap)

When someone clones a repository that uses Dolt backend:

1. Run `bd bootstrap` in the clone
2. If the git remote has `refs/dolt/data` (pushed via `bd dolt push`),
   `bd bootstrap` auto-detects it and clones the database from the remote
3. Work continues normally — all existing issues are available

**No manual steps required** beyond `bd bootstrap`. The auto-detect:
- Probes `origin` for `refs/dolt/data`
- Clones the Dolt database from the remote (instead of creating a fresh one)
- Configures the Dolt remote for future `bd dolt push`/`pull`

If `sync.remote` is set in `.beads/config.yaml`, that takes precedence
over auto-detection. Any Dolt-compatible remote URL is supported (DoltHub,
S3, GCS, file, or git). On brand-new projects, `bd init` auto-detects
`git origin` and persists it as `sync.remote`, so the first `bd dolt push`
publishes Dolt history to `refs/dolt/data` on the same git remote.

### Verifying Bootstrap Worked

```bash
bd list              # Should show issues
bd vc status         # Should show the current branch, no uncommitted changes
```

## Troubleshooting

### Server Not Running

**Symptom:** Connection refused errors when using server mode.

```
failed to create database: dial tcp 127.0.0.1:3307: connect: connection refused
```

**Fix:**
```bash
gt dolt start        # Orchestrator command
# Or
gt dolt status       # Check if running
```

### Bootstrap Not Running

**Symptom:** `bd list` shows nothing on fresh clone.

**Check:**
```bash
ls .beads/dolt/            # Should NOT exist (pre-bootstrap)
BD_DEBUG=1 bd list         # See bootstrap output
```

**Force bootstrap:**
```bash
rm -rf .beads/dolt         # Remove broken state
bd list                    # Re-triggers bootstrap
```

### Database Corruption

**Symptom:** Queries fail, inconsistent data.

**Diagnosis:**
```bash
bd doctor                  # Basic checks
bd doctor --deep           # Full validation
bd doctor --server         # Server mode checks (if applicable)
```

**Recovery options:**

1. **Repair what's fixable:**
   ```bash
   bd doctor --fix
   ```

2. **Rebuild from remote:**
   ```bash
   rm -rf .beads/dolt
   bd list                  # Re-triggers bootstrap
   ```

### Already Committed `.beads/dolt/` to Git

If you accidentally committed a Dolt data directory:

1. Update gitignore: `bd doctor --fix`
2. Remove it from git tracking: `git rm --cached -r .beads/dolt/` (or `.beads/embeddeddolt/`)
3. Commit the removal: `git commit -m "fix: remove accidentally committed dolt data"`
4. To purge from history, use [BFG Repo-Cleaner](https://rtyley.github.io/bfg-repo-cleaner/) or `git filter-repo`

### Lock Contention (Embedded Mode)

**Symptom:** "database is locked" errors.

Embedded mode is single-writer (enforced via file lock). If you need concurrent
access, switch to server mode. See [Migrating Between Backends](#migrating-between-backends).

## Configuration Reference

```yaml
# .beads/config.yaml

# Dolt settings
dolt:
  # Auto-commit Dolt history after writes (default: on for embedded, off for server)
  auto-commit: on        # on | off

  # Storage mode (default: embedded)
  mode: embedded         # embedded | server
  # Server mode settings (only used when mode: server)
  host: 127.0.0.1
  port: 3307
  user: root
  # Password: env var or credentials file (see below)

  # Shared server mode (GH#2377): all projects share a single Dolt server
  # at ~/.beads/shared-server/. Each project uses its own database (prefix-based).
  # Eliminates port conflicts and reduces resource usage on multi-project machines.
  shared-server: false   # true | false
```

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `BEADS_DOLT_PASSWORD` | Server mode password (highest priority) |
| `BEADS_CREDENTIALS_FILE` | Path to credentials file (overrides default location) |
| `BEADS_DOLT_SERVER_MODE` | Enable server mode (set to "1") |
| `BEADS_DOLT_SERVER_HOST` | Server host (default: 127.0.0.1) |
| `BEADS_DOLT_SERVER_PORT` | Server port (default: 3307, or 3308 in shared mode) |
| `BEADS_DOLT_SERVER_TLS` | Enable TLS (set to "1" or "true") |
| `BEADS_DOLT_SERVER_USER` | MySQL connection user |
| `BEADS_DOLT_SHARED_SERVER` | Enable shared server mode (set to "1" or "true") |
| `DOLT_REMOTE_USER` | Clone/push/pull auth user |
| `DOLT_REMOTE_PASSWORD` | Clone/push/pull auth password |
| `BD_DOLT_AUTO_COMMIT` | Override auto-commit setting |

**Note:** `BEADS_DOLT_SERVER_PORT` (and the legacy `BEADS_DOLT_PORT`) do more
than name a port — setting either also marks the workspace as an
externally-managed server and suppresses auto-start. See **Server Mode**
above.

### Credentials File

For multi-server setups, you can store passwords in an INI-style credentials file
instead of juggling environment variables per project. Passwords are looked up by
`[host:port]` section, so each project automatically gets the right password based
on its configured server.

**Password resolution order:**
1. `BEADS_DOLT_PASSWORD` env var (highest priority, existing behavior)
2. Credentials file lookup by `[host:port]` (using the resolved runtime port)
3. Empty string (no password)

**Port resolution note:** The `[host:port]` used for credential lookup matches the
resolved runtime port (from the port file, env var, or config — in that priority
order), not necessarily the port stored in `metadata.json`. This matters when using
IAP tunnels: if your tunnel maps remote:3307 to localhost:3308, store your password
under `[127.0.0.1:3308]` and the credentials file will match the actual connection.

**Default location:** `~/.config/beads/credentials` (Linux/macOS), `%APPDATA%\beads\credentials` (Windows)

**Override location:** Set `BEADS_CREDENTIALS_FILE` env var.

**File format:**

```ini
# ~/.config/beads/credentials
[127.0.0.1:3307]
password=localDevPassword

[beads.company.com:3307]
password=teamServerPassword

[10.0.1.50:3308]
password=officePassword
```

**Permissions:** On Linux/macOS, a warning is printed to stderr if the file is
readable by group or others (mirrors ssh behavior). Set permissions with:

```bash
chmod 600 ~/.config/beads/credentials
```

## Dolt Version Control

Dolt maintains its own version history, separate from Git:

```bash
# View an issue's version history across Dolt commits
bd history bd-42

# Show current branch and uncommitted changes
bd vc status

# Create manual checkpoint
bd vc commit -m "Checkpoint before refactor"
```

### Auto-Commit Behavior

In **embedded mode** (standalone default), each `bd` write command creates a Dolt commit:

```bash
bd create "New issue"    # Creates issue + Dolt commit
```

In **server mode** (orchestrator), auto-commit defaults to OFF because the server
manages its own transaction lifecycle. Firing `DOLT_COMMIT` after every write
under concurrent load causes 'database is read only' errors.

Override for batch operations (embedded) or explicit commits (server):

```bash
bd --dolt-auto-commit off create "Issue 1"
bd --dolt-auto-commit off create "Issue 2"
bd vc commit -m "Batch: created issues"
```

## Server Management (Orchestrator)

The orchestrator provides integrated Dolt server management:

```bash
gt dolt start            # Start server (background)
gt dolt stop             # Stop server
gt dolt status           # Show server status
gt dolt logs             # View server logs
gt dolt sql              # Open SQL shell
```

Server runs on port 3307 (avoids MySQL conflict on 3306).

### Standalone-to-managed-city handoff

When an existing standalone project is later added to a managed city or
orchestrator, avoid letting two Dolt servers become sources of truth for the
same beads database name. A common split-brain symptom is that `.beads/dolt-server.port`
points at the old standalone server while the shell environment points `bd` at
the managed server with `BEADS_DOLT_PORT` or `BEADS_DOLT_SERVER_PORT`.

Check before migrating:

```bash
bd doctor
bd dolt status
```

`bd doctor` warns when the runtime managed port differs from the local port
file. The warning is intentionally diagnostic only; do not delete the local port
file until the standalone store has been exported and imported into the managed
server.

Safe manual handoff:

```bash
# From the standalone project, without managed-city port overrides:
unset BEADS_DOLT_PORT BEADS_DOLT_SERVER_PORT
bd backup
bd export > /tmp/beads-standalone.jsonl
bd dolt stop

# Then enter the managed-city environment and import into its Dolt server:
bd import /tmp/beads-standalone.jsonl
bd doctor
```

After `bd doctor` shows one healthy store and the imported issue count is
correct, archive the old local Dolt data directory instead of deleting it
immediately. Keep the backup until the managed city has been pushed or otherwise
snapshotted.

### Shared Server Mode

On machines with multiple beads projects, each project normally starts its own Dolt server.
Shared server mode runs a single Dolt server at `~/.beads/shared-server/` that serves all projects:

```bash
# Enable for this project (config.yaml key)
bd config set dolt.shared-server true

# Or enable machine-wide via environment variable
export BEADS_DOLT_SHARED_SERVER=1

# Or enable during init
bd init --prefix myproject --shared-server
```

**Benefits:**
- No port conflicts between projects (single server on port 3308, avoids orchestrator on 3307)
- Reduced resource usage (one process instead of many)
- Automatic database isolation (each project uses its own database name)

**How it works:**
- Server state files (PID, port, lock, log) live in `~/.beads/shared-server/`
- Dolt data directory: `~/.beads/shared-server/dolt/`
- Each project's database is stored as a subdirectory (e.g., `~/.beads/shared-server/dolt/myproject/`)
- The file lock mechanism ensures safe concurrent access from multiple projects
- Default port is 3308 (not 3307) to avoid conflict with the orchestrator. Override with `BEADS_DOLT_SERVER_PORT` or `dolt.port` in config.yaml

**Important:** Each project on a shared server **must have a unique prefix** (database name).
Two projects with the same prefix share the same database — if this happens accidentally,
the project identity check will detect the mismatch and refuse to connect, preventing
silent data corruption. Always use distinct prefixes when running `bd init --shared-server`.

```bash
# Check shared server status from any project
bd dolt status

# Show full configuration including shared mode
bd dolt show
```

### Data Location (Orchestrator)

```
<town-root>/.dolt-data/
├── hq/                  # Town beads (hq-*)
├── my-project/          # Project rig (mp-*)
├── beads/               # Beads rig (bd-*)
└── other-project/       # Other rig (op-*)
```

### Central Dolt Server (macOS LaunchAgent)

If you do not use the orchestrator but still want a single persistent Dolt
server for multiple projects on macOS, run a custom `LaunchAgent` instead of
spawning per-project embedded instances.

#### Why Not `brew services start dolt`?

After installing Dolt with `brew install dolt`, the natural next step is
`brew services start dolt`. However, the Homebrew formula runs
`dolt sql-server` without the `--config` flag, and Dolt does not auto-discover
`config.yaml` from its working directory. The config file must be passed
explicitly with `--config <file>`.

#### Setup with a Custom LaunchAgent

Install Dolt and initialize its data directory. Check the installed version
against [the pin](#which-dolt-version-to-install) — Homebrew's formula is not
pinned:

```bash
brew install dolt
dolt version
cd /opt/homebrew/var/dolt && dolt init
```

Configure Dolt for port 3307:

```yaml
# /opt/homebrew/var/dolt/config.yaml
log_level: info

listener:
  host: 127.0.0.1
  port: 3307
  max_connections: 100

behavior:
  autocommit: true
```

Create the LaunchAgent plist:

```bash
cat > ~/Library/LaunchAgents/com.local.dolt-server.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.local.dolt-server</string>
    <key>ProgramArguments</key>
    <array>
        <string>/opt/homebrew/bin/dolt</string>
        <string>sql-server</string>
        <string>--config</string>
        <string>/opt/homebrew/var/dolt/config.yaml</string>
    </array>
    <key>WorkingDirectory</key>
    <string>/opt/homebrew/var/dolt</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/opt/homebrew/var/log/dolt.log</string>
    <key>StandardErrorPath</key>
    <string>/opt/homebrew/var/log/dolt-error.log</string>
</dict>
</plist>
EOF
```

Load and verify the service:

```bash
launchctl load ~/Library/LaunchAgents/com.local.dolt-server.plist
mysql -h 127.0.0.1 -P 3307 -u root -e "SELECT 1"
```

Point beads at the central server:

```bash
export BEADS_DOLT_SERVER_MODE=1
export BEADS_DOLT_SERVER_PORT=3307
```

Manage the service:

```bash
# Stop
launchctl unload ~/Library/LaunchAgents/com.local.dolt-server.plist

# Restart
launchctl unload ~/Library/LaunchAgents/com.local.dolt-server.plist
launchctl load ~/Library/LaunchAgents/com.local.dolt-server.plist

# Check logs
tail -f /opt/homebrew/var/log/dolt.log
```

## Advanced Dolt Usage

The `dolt` CLI lets you operate directly on the database for power-user
workflows. The data directory depends on your mode: `.beads/embeddeddolt/`
(embedded) or `.beads/dolt/` (server).

### Branching

```bash
cd .beads/dolt   # or .beads/embeddeddolt for embedded mode
dolt branch feature-x
dolt checkout feature-x
```

### Time Travel

```bash
dolt log
dolt checkout <commit-hash>
dolt sql -q "SELECT * FROM issues"
```

### Diff and Blame

```bash
dolt diff main feature-x
dolt blame issues
```

## Migration Cleanup

After successful migration from SQLite, you may have backup files:

```
.beads/beads.backup-pre-dolt-20260122-213600.db
.beads/sqlite.backup-pre-dolt-20260123-192812.db
```

These are safe to delete once you've verified Dolt is working:

```bash
# Verify Dolt works
bd list
bd doctor

# Then clean up (after appropriate waiting period)
rm .beads/*.backup-*.db
```

**Recommendation:** Keep backups for at least a week before deleting.

## See Also

- [Sync Concepts](/core-concepts/sync-concepts) - The conceptual model behind cross-machine sync (Dolt source of truth, wire format, anti-patterns)
- [Sync Setup Guide](/getting-started/sync-setup) - Setting up sync across multiple computers
- [Federation Setup Guide](/multi-agent/federation) - Peer-to-peer federation setup
- [Configuration](/reference/configuration) - Full configuration reference
- [Dependencies and Gates](/core-concepts/dependencies) - Dependencies and gates
- [Git Integration](/reference/git-integration) - Git worktrees and protected branches
- [Troubleshooting](/reference/troubleshooting) - General troubleshooting
