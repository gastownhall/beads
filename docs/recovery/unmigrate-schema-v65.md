---
title: Un-migrate from Schema v65
description: Move a database that bd 1.2.x auto-migrated back to a 1.1.2-compatible workspace, keeping all data
---

The 1.2.0/1.2.1 releases migrated existing databases to schema v65
automatically, on any command — including reads. If your database was
migrated and you want to go back to bd 1.1.2 (whose binaries refuse a v65
database with `schema version mismatch: database is at v65, binary knows up
to v53`), this runbook rebuilds a 1.1.2-compatible workspace from a full
export. It is written to be executable by an agent as-is.

There is no in-place down-migration. The procedure is export → set aside →
fresh init → import.

**What survives** (verified): issues with statuses and close reasons,
dependencies, comments, notes, labels, memories, and wisps (with
`--all`).
**What does not**: Dolt commit history and the events/telemetry audit
tables. Both remain readable in the set-aside `.beads.v65.bak` directory —
nothing is deleted.

<Warning>
Run every step with the binaries it names: the EXPORT runs under the 1.2.x
binary that matches the database; the INIT and IMPORT run under the 1.1.2
binary you are returning to. Mixing them up fails closed (schema-skew guard),
but re-running the whole sequence from the top is the only supported retry.
</Warning>

## Preconditions

1. **Stop anything that writes to this workspace** — supervisors, watchers,
   hooks on a timer.
2. **Server mode: stop the dolt sql-server** serving this database
   (`bd dolt stop`, or stop the process that owns it). For a SHARED server
   database, coordinate with the other clients first — after this procedure
   they must re-clone or run it too; do not un-migrate a shared database
   unilaterally.
3. Install (or locate) a **bd 1.1.2 binary** alongside the current one. GitHub
   releases: tag `v1.1.2`. Keep both paths handy; the steps name which one to
   use.
4. Run from the directory that CONTAINS `.beads/`.

## Procedure

```bash
# 1. Full export, with the 1.2.x binary the database currently matches.
bd export --all -o snapshot.jsonl
#    Sanity: the file is non-empty and its line count is plausible for
#    your workspace.
wc -l snapshot.jsonl

# 2. Set the migrated workspace aside. Salvage-first: rename, never delete.
mv .beads .beads.v65.bak

# 3. Fresh init under the 1.1.2 binary (creates a v53-schema workspace).
#    Use the same issue prefix as before.
bd-1.1.2 init --prefix <your-prefix>

# 4. Import the snapshot under the 1.1.2 binary.
bd-1.1.2 import snapshot.jsonl
```

## Verify

```bash
# Counts match your expectations (compare against the export):
bd-1.1.2 list -n 0 --status all | tail -3
# A known issue round-trips with its comments/labels/deps:
bd-1.1.2 show <some-known-id>
# Memories survived:
bd-1.1.2 memories | head
```

If anything is missing, the migrated original is intact in `.beads.v65.bak`:
restore it with `mv .beads .beads.failed && mv .beads.v65.bak .beads` and use
a 1.2.x binary with it while you retry.

## Afterwards

- Keep `.beads.v65.bak` until you are confident; it holds the Dolt history
  and audit tables the import does not carry.
- Pin your install to 1.1.2 until a release whose default does not migrate
  your database is available; see the release notes of the version that sent
  you here.
- If this workspace had a Dolt remote, the remote still holds the MIGRATED
  history. Do not push the rebuilt workspace over it without deciding which
  side is canonical; a fresh remote is the simple safe choice.
