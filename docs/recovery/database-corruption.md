---
title: Database Corruption
description: Tell an unavailable or busy database from a damaged one, and recover a Beads database that will not open
---

This runbook covers a Beads database that will not open, in embedded mode (the
default, data in `.beads/embeddeddolt/`) and in server mode (data in
`.beads/dolt/` by default). Many errors that look like corruption are not, so
diagnose first and change nothing on disk until you know which case you have.

## Symptoms

- **The server is unavailable** (server mode only): `bd` reports that the Dolt
  server is not running or not accepting connections. Check `bd dolt status`
  and the server log, `.beads/dolt-server.log` (in shared server mode,
  `~/.beads/shared-server/dolt-server.log`). A stopped server is not
  corruption: start it with `bd dolt start`. If the log reports a corrupted
  journal, continue with [Engine won't open](#engine-wont-open).
- **The database is locked or busy**: `bd` reports `database is locked`, or
  waits, after another command was interrupted or while agents are running.
  See [Lock errors after an interrupted command](#lock-errors-after-an-interrupted-command).
- **The engine reports a journal or open error**: every `bd` command fails
  while opening the database, for example with `corrupted journal` or
  `possible data loss detected in journal file`, even though no other `bd`
  process is running. Continue with [Engine won't open](#engine-wont-open).

## Lock errors after an interrupted command

After an interrupted or killed command you may find a lock file left behind
(in embedded mode a `LOCK` pathname inside the database's `.dolt/`
directory), or see `database is locked` errors on the next command.

A leftover lock file is not evidence of a held lock. Dolt takes its locks
through the operating system, and the kernel releases a process's file locks
when the process dies, so a lock left behind by a killed command is normally
already released: an existing `LOCK` file alone diagnoses nothing.

Live contention means another process is still using the database right now:
in embedded mode another `bd` process (often an agent or a script still
running a batch) working in the same workspace, in server mode the Dolt
server. It is resolved by waiting for or stopping the actual holder, not by
rebuilding the database. `bd dolt stop` stops a server that bd manages; it
cannot stop an arbitrary embedded `bd` process. End that process yourself
after confirming it is really yours to stop:

```bash
ps aux | grep '[b]d '     # find the bd process still working in this workspace
kill <pid>                # only once you know it is yours and safe to stop
bd dolt stop              # server mode: stop the managed server
```

Never delete files inside `.dolt/` (including `noms/LOCK`) to clear a lock.
Lock files are part of the engine's on-disk format; deleting engine internals
is what actually risks corruption, which is why `bd doctor` refuses to touch
Dolt-internal lock files. Retry once the holder is gone. Only if every `bd`
command still fails to open the database with no holder left, continue with
[Engine won't open](#engine-wont-open).

## Engine won't open

`bd doctor --fix` does not rebuild a journal the engine cannot open, and
general bd doctor checks are not supported in embedded mode (only
`bd doctor --check=artifacts`, `--check=conventions` and `--check=pollution`
run there). Recovery means building a new database from a copy of your data
that still opens, while keeping the damaged store intact.

### 1. Stop everything that uses the store

Stop every bd process, agent and script that works in this workspace. In
server mode also stop the managed server with `bd dolt stop`; in shared server
mode that server serves every project on the machine, so check what else
depends on it first.

### 2. Preserve the damaged store

Copy the whole store and its configuration before you change anything. Find
the store first instead of assuming the default path. `bd context --json`
reads only configuration files, so it answers even when the database will not
open. Its `beads_dir` is the `.beads` directory bd actually uses: in a git
worktree or jj workspace, or behind a `.beads/redirect` file, it can be
another checkout's.

- **Embedded mode:** `embeddeddolt/` inside `beads_dir`.
- **Server mode:** `dolt/` inside `beads_dir` by default, or the `data_dir`
  that `bd context` reports (set by `dolt_data_dir` in `metadata.json` or by
  `BEADS_DOLT_DATA_DIR`). In shared server mode it is
  `~/.beads/shared-server/dolt/` (or `$BEADS_SHARED_SERVER_DIR/dolt/`) and
  holds every project's database.

```bash
beads_dir=$(bd context --json | jq -r .beads_dir)   # or set it from bd context by hand
ts=$(date +%Y%m%dT%H%M%S)
mkdir -p ../beads-damaged-$ts
cp -a "$beads_dir" ../beads-damaged-$ts/   # store, metadata.json, config.yaml, backups
# If the store lives outside beads_dir, copy that directory as well.
```

Keep the preserved copy outside any server data_dir: the sql-server loads every
subdirectory of its data_dir as a database, and a damaged copy there
crash-loops it (see [re-clone gotchas](/recovery/init-safety#re-clone-gotchas)).
Do not delete or edit files inside `.dolt/`, and do not run Dolt's data-loss
repair (`dolt fsck --revive-journal-with-data-loss`) on your only copy.

### 3. Recover into a fresh store

Each route below builds a new database; none repairs the damaged one in place.
Run it in a fresh recovery workspace (for example a new clone of the
repository), or in this workspace after moving the damaged store out of the
way, outside `.beads/` and outside any server data_dir:

```bash
mv "$beads_dir/embeddeddolt" ../embeddeddolt.damaged.$ts   # embedded mode
# Server mode: move the database directory under your data_dir the same way.
```

Pick the newest source you trust. A remote, backup or export holds only what
it held when it was made, so a stale source silently drops later work; compare
it with what you expect before you rely on it. If you have no recovery source,
stop here and keep the preserved copy: it is the only copy of your data. Do
not run `bd init` over it or expect an empty database to bring the issues back.

If `bd bootstrap` or `bd init` refuses on any route (for example because the
remote already has history or the configured server database is missing),
follow the [recovery playbook](/recovery/init-safety) for the code it prints
rather than forcing it.

### From a Dolt remote

Use this when a Dolt remote (`sync.remote`, or Dolt data on the git origin) is
current:

```bash
bd bootstrap --dry-run    # inspect the plan first
bd bootstrap --yes
```

Read the dry-run plan before you run it. It should clone the remote or, with no
usable remote, restore `.beads/backup/issues.jsonl` or import a git-tracked
`.beads/issues.jsonl`. A plan that creates an empty database is not a recovery:
stop and use another route. `bd dolt pull` does not repair a local engine
that will not open.

### From a Dolt-native backup

Use this only for a Dolt-native backup: the directory you registered with
`bd backup init` and filled with `bd backup sync`, or an
[auto-backup](/reference/configuration#auto-backup) snapshot in
`.beads/backup/` (take it from your preserved copy) or in the `backup/`
directory inside `backup.git-repo`. `bd backup restore` needs an initialized
database and does not read a `bd export` JSONL file, so run it only in an
initialized recovery workspace, never over the damaged store:

```bash
bd init
bd backup restore /path/to/backup --force   # replaces whatever bd init created
```

### From a JSONL export

A JSONL file from `bd export` is an issue export, not a Dolt backup: it has no
Dolt history and `bd backup restore` cannot read it. Keep the original file
and import a copy into an initialized recovery workspace:

```bash
bd init
bd import /path/to/export.jsonl
```

Or let `bd init` import it: place a copy under the recovery workspace's
`.beads/` and point `import.path` at it (`bd init --from-jsonl` reads the
configured `import.path`, `issues.jsonl` by default, not a filename you pass):

```bash
cp /path/to/export.jsonl .beads/recovered.jsonl
bd config set import.path recovered.jsonl
bd init --from-jsonl
```

### 4. Verify the recovered data

Compare the recovered issues with your source, not with a doctor run:

```bash
bd list --all
bd stats
```

In server mode `bd doctor` can also check the new database. In embedded mode
run only the supported `bd doctor --check=...` variants.

## Prevention

- **Batch repeated writes.** bd batch runs a limited set of write commands
  (`close`, `update`, `create`, `dep add`, `dep remove`; see `bd batch --help`)
  in one transaction and one `bd` process, instead of starting the embedded
  engine once per call. Anything outside that grammar still needs its own `bd`
  call.
- **Use a server for sustained concurrent agents.** In server or proxied-server
  mode (`bd init --server`, `bd init --proxied-server`) one Dolt server keeps
  the engine running for every client. Switching an existing workspace is a
  [migration](/architecture/dolt#migrating-between-backends), not a repair.
- Neither repairs a damaged journal: recover first with
  [Engine won't open](#engine-wont-open).
- Keep a current recovery source: push to a Dolt remote (`bd dolt push`) or
  take Dolt-native backups (`bd backup init <path>` once, then
  `bd backup sync`).
- In server mode, use `bd dolt stop` before system shutdown.
