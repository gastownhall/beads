---
title: Database Corruption
description: Recover a Beads database that will not open
---

This runbook recovers a Beads database that will not open — after an
interrupted or killed command, a crash, or a reported journal error — in
embedded or server mode.

## Engine won't open

`bd` fails while opening the database: a journal or manifest error from the
storage engine, or a refused open, before any command logic runs.

**First rule out the causes that are not corruption:**

- **Server mode — connection refused / server not running.** The database is
  fine; the server is unreachable. Check `bd dolt status` and start the
  server. This is an availability problem, not corruption.
- **A live process is using the embedded database.** Embedded mode is
  single-writer: while another `bd` process holds the database, opens wait
  or fail with lock errors. That is live contention, not corruption — see
  [Locks and live holders](#locks-and-live-holders).
- **An actual open or journal error from the engine itself** (embedded mode,
  or a server reporting journal damage in its log). Continue with the steps
  below.

**Step 1 — quiesce and preserve everything.** Recovery on a store someone is
still writing is not recovery, and an unopenable store must survive the
recovery attempt:

1. Stop every user of the damaged store: `bd` commands, scripts, agents, and
   in server mode the `dolt sql-server` serving it.
2. Identify the configured data path instead of moving directories blindly.
   The defaults are `.beads/embeddeddolt/` (embedded mode) and `.beads/dolt/`
   (server mode), but a configured `dolt_data_dir` in `.beads/config.yaml`
   relocates the database — find the real location before copying anything.
3. Preserve the complete damaged store *and* the configuration: copy the
   whole database directory and `.beads/metadata.json` / `.beads/config.yaml`
   aside. A recovery step that goes wrong must not destroy the last copy.
4. Keep the preserved copy outside any active server `data_dir`: a
   sql-server treats every subdirectory of its data directory as a database,
   so a set-aside store left inside it crash-loops the server — see
   [Re-clone gotchas](/recovery/init-safety#re-clone-gotchas).

Recovery happens in a **fresh recovery workspace** — a new clone, or a new
directory you initialize deliberately — never as an in-place journal repair
of the damaged directory. `bd doctor --fix` repairs configuration and
consistency issues; it is not an unopenable-journal rebuild.

**Step 2 — recover into the fresh target from the best source you have.**
Take the first route whose source you can verify is current.

*Route A — a verified-current remote.* If the project pushes to a Dolt remote
(`sync.remote`), preview the recovery plan before executing it:

```bash
bd bootstrap --dry-run
```

Read the plan: it must actually repopulate your issues (clone the remote, or
restore a local backup). An empty-init plan — a fresh database with nothing
in it — is not data recovery; it is the outcome you accept only after every
source below is exhausted. If the plan recovers your data:

```bash
bd bootstrap --yes
```

A stale remote silently loses everything since the last push: if work
happened after it, keep the preserved store and weigh Route B and Route C as
competing sources instead.

*Route B — a Dolt-native backup snapshot.* `bd backup sync` writes a
Dolt-native snapshot; `bd export` writes a JSONL issue export. They are not
interchangeable: `bd backup restore` reads a `bd backup` snapshot and will
not read a `bd export` JSONL file. In the fresh recovery target:

```bash
bd init
bd backup restore            # reads .beads/backup/ by default; or pass a snapshot path
```

`bd backup restore` requires an initialized database — that is why
`bd init` comes first — and restores the snapshot you name, so choose the
newest snapshot you trust.

*Route C — an existing JSONL export.* A `bd export` JSONL file carries issue
data, not Dolt history. You cannot export from an engine that will not open,
so this route needs a file that already exists. Either import it into an
initialized recovery target:

```bash
bd init
bd import /path/to/export.jsonl
```

or place a copy under the recovery workspace's `.beads/` and name it through
the configured import path:

```bash
cp /path/to/export.jsonl .beads/recovered.jsonl
bd config set import.path recovered.jsonl
bd init --from-jsonl
```

`bd init --from-jsonl` reads the configured `import.path` (default
`issues.jsonl`, relative to `.beads/`), not a positional filename. Keep the
original export untouched while you work; a stale export silently loses
everything after it was written.

**Step 3 — verify against the source.**

```bash
bd list --all
bd stats
```

Compare the counts and spot-check recent issues against your recovery source.
General `bd doctor` still requires server mode — embedded mode supports only
`bd doctor --check=artifacts`, `--check=conventions`, and
`--check=pollution` — so `bd list --all` and `bd stats` are the
verification, independent of doctor support. Keep the preserved copy until
this passes.

## Locks and live holders

After an interrupted or killed command you may find a lock file left behind
(in embedded mode a `LOCK` pathname inside the database's `.dolt/`
directory), or see "database is locked" errors on the next command.

A leftover lock file is not evidence of a held lock. The kernel releases a
process's file locks when the process dies, so a lock left behind by a killed
command is normally already released — an existing `LOCK` file alone
diagnoses nothing. Live contention means another process is *still* using
the database right now, and is resolved by waiting for or stopping the actual
holder, not by rebuilding the database. `bd dolt stop` stops a server that bd
manages; it cannot stop an arbitrary embedded `bd` process — end that
process yourself (close its terminal, or kill its PID) after confirming it
is really yours to stop.

Never delete files inside `.dolt/` to "clear" a lock. Lock files are part of
the engine's on-disk format; deleting engine internals is what actually
risks corruption, which is why `bd doctor` refuses to touch Dolt-internal
lock files.

## Prevention

- **Scripts and agents making many writes should use `bd batch`.** It runs a
  limited write grammar (`close`, `update`, `create`, `dep add`,
  `dep remove`) in one transaction with one commit, instead of one engine
  start-up and one commit per `bd` invocation — the aggregate cost that
  times agent-driven batches out. Other commands still pay per-invocation
  engine start-up; batching collapses a script's writes, not the CLI.
- **Sustained concurrent access belongs in server mode or proxied-server
  mode**, where one persistent engine serves every invocation. Switching is
  a configured migration (see
  [Migrating between backends](/architecture/dolt#migrating-between-backends)),
  not a repair: neither `bd batch` nor a mode switch repairs a damaged
  journal.
- Run `bd doctor` periodically to catch issues early
