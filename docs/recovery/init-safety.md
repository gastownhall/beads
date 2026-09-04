---
title: Recovery Playbooks
description: Step-by-step recovery for bd init and bd dolt push/pull refusals, including the primary-key fork playbook
---

Last reviewed: 2026-09-04

Freshness source: `cmd/bd/init.go`, `cmd/bd/init_safety.go`,
`cmd/bd/init_safety_test.go`, and `cmd/bd/dolt.go`.

This document lives next to the ADRs and matches the structure of `bd`'s
error messages: each named refusal in `bd init` and `bd dolt push`/`pull`
points here to a labeled anchor with step-by-step recovery instructions.

See also: `bd help init-safety`, and
[ADR 0002 — `bd init` safety invariants](https://github.com/gastownhall/beads/blob/main/engdocs/adr/0002-init-safety-invariants.md).

## Table of contents

- [init-force-refused — `bd init --force`/`--reinit-local` refused because origin has Dolt history](#init-force-refused)
- [init-token-missing — `--discard-remote` refused because `--destroy-token` is missing or wrong](#init-token-missing)
- [init-local-exists — `bd init` refused because local data already exists](#init-local-exists)
- [legacy-empty-server-recovery — `bd init` refused on an unwitnessed server workspace whose database was never populated](#legacy-empty-server-recovery)
- [pk-fork-refused — `bd dolt pull`/`push` refused because a table has different primary keys in its common ancestor](#pk-fork-refused)

---

## init-force-refused

**Exit code:** `10` (`ExitRemoteDivergenceRefused`)

**Symptom**

```
bd init refuses: remote 'origin' already has Dolt history (refs/dolt/data).
  Why: this init mode would create or reuse local history instead of
       adopting the remote. ...
```

**Why this happens**

`bd init --force` (or `--reinit-local`) tells `bd` to bypass the local
data-safety guard. `bd init --from-jsonl` selects a local JSONL export as
the source. But the remote already has project history. Proceeding would
create an orphan local Dolt branch with no common ancestor on origin. The
next `bd dolt push` would either fail (no common ancestor) or — worse, if
force-pushed — destroy the team's data.

**Recovery paths**

Pick the one that matches your intent.

### 1. You want to adopt the remote's history (most common)

```
bd bootstrap
```

This clones the remote's Dolt database into a fresh local `.beads/`.
Your local state is ignored; the team's history becomes yours.

### 2. You want to diagnose what went wrong before deciding

```
bd doctor
bd dolt status
```

`bd doctor` walks the local + remote state and names concrete problems.
`bd dolt status` shows the Dolt-level view. Neither modifies anything.

### 3. You intentionally want to overwrite the remote's history (destructive)

This is a cross-boundary operation that affects every collaborator. You
need to pair the local-source init (`--reinit-local` or `--from-jsonl`)
with `--discard-remote`. In interactive mode `bd` will prompt for
confirmation; in non-interactive mode you must supply a `--destroy-token`.
See `bd help init-safety` for the token format.

After `bd init --reinit-local --discard-remote`, your next
`bd dolt push` must be a history-replacing push. Coordinate with your
team before doing this.

---

## init-token-missing

**Exit code:** `12` (`ExitDestroyTokenMissing`)

**Symptom**

```
bd init refuses: --discard-remote requires an explicit destroy-token in non-interactive mode.
```

**Why this happens**

You're running non-interactively (CI, agent, piped input) and passed
`--discard-remote`. Destructive cross-boundary operations cannot be
authorized silently.

**Recovery paths**

### 1. Run interactively

Re-run in a TTY. `bd init --reinit-local --discard-remote` will prompt
you to type the destroy-token at confirmation time.

### 2. Supply the token explicitly (CI/automation)

The token format is `DESTROY-<issue-prefix>`. For a project whose issue
prefix is `bd`:

```
bd init --reinit-local --discard-remote --destroy-token=DESTROY-bd
```

Automation should template the token from project state, not from error
output. See [ADR 0002 — Invariant 4](https://github.com/gastownhall/beads/blob/main/engdocs/adr/0002-init-safety-invariants.md)
for why the token is never echoed in `bd`'s error messages.

---

## init-local-exists

**Exit code:** `11` (`ExitLocalExistsRefused`)

**Symptom**

```
Refusing to destroy N issues in non-interactive mode.
  See 'bd help init-safety' for the required --destroy-token format.
```

Or, in interactive mode, you declined the typed `destroy N issues`
confirmation.

**Why this happens**

Local `.beads/` has existing issues. `bd init --reinit-local` would
permanently destroy them.

**Recovery paths**

### 1. Export first, then proceed

```
bd export > issue-export.jsonl
bd init --reinit-local
```

`issue-export.jsonl` lets you re-import individual issues if needed. It is not
a full database backup; use `bd backup` when the Dolt database is healthy
enough to create a restorable backup before reinitializing.

### 2. Investigate why you hit this

If you did NOT expect `bd init` to be the right command here, run
`bd doctor` first — you may be looking at a server config issue that a
re-init won't fix.

---

## legacy-empty-server-recovery

**Symptom**

```
legacy Dolt server workspace detected; explicit migration is required before this bd version can open or modify the workspace. Preserve .beads unchanged and follow docs/getting-started/upgrading.md#cross-era-upgrades
```

on a workspace whose `metadata.json` selects `dolt_mode: server`, whose
`.beads/dolt/` root exists, and which has no `.local_version` — typically a
bootstrap that created the server database but was interrupted before the
witness was written, so the database is still empty.

**Why this happens**

A missing witness is ambiguous: the guard cannot tell a never-populated
current-era workspace from pre-1.0 data, so every command refuses. Only one
narrow init shape may proceed without a witness, and only after proving there
is nothing to destroy.

**Recovery path**

```
bd init --force --server --server-host HOST --server-port PORT --database NAME
```

This is admitted only when all of the following hold; otherwise the refusal
above is preserved unchanged:

1. `NAME` is the database `metadata.json` (or `BEADS_DOLT_SERVER_DATABASE`)
   already selects, so the destroy-count gate is skipped only for the store it
   would have counted.
2. No live `dolt_server_socket` persisted in `metadata.json` would route the
   commands run after recovery away from `HOST:PORT` (the init's own count
   ignores that socket; every later command honors it). A dead socket path
   is tolerated and cleared from `metadata.json` on success.
3. Read-only SQL proves that every database the server lists, and every
   database materialized under `.beads/dolt/`, has zero tables. One Dolt root
   serves many databases and the witness disarms the guard for the whole
   workspace, so a non-empty sibling database refuses.
4. The init is a native server init: not `--proxied-server`, not shared-server
   mode, and no `dolt.credential-command` is configured.

The proof is repeated under `bd init`'s mutation gate, and the current-era
`.local_version` is created exclusively (`O_EXCL`) only after the store opens.
If a witness appears in between, or the store fails to open, no witness is
written and the original refusal wins. If the exclusive witness write itself
fails after the store opened, the store has already created its schema, so the
proved database is no longer empty: later runs refuse, and the workspace must
be recovered through the explicit export/import path below.

If any condition fails, or the database was ever populated, follow the
explicit legacy Dolt export/import path in
[Cross-era Upgrades](../getting-started/upgrading.md#cross-era-upgrades).

---

## pk-fork-refused

**Symptom**

```
$ bd dolt pull
Error: ... cannot merge because table dependencies has different primary keys in its common ancestor
```

(or the variant without `in its common ancestor`). `bd` follows the error
with a short version of the recovery recipe below.

**Why this happens**

The two histories being merged disagree about a table's *primary key set* —
not about row contents. Dolt can cell-merge rows, but it refuses outright to
merge a table whose primary key was reshaped differently on each side (or
whose common ancestor had a different primary key than both sides). The
refusal happens before any row conflicts materialize, so `bd dolt pull`'s
conflict auto-resolver never gets a chance to run. **Retrying never helps**:
the histories are permanently un-mergeable.

The usual cause is upgrading `bd` independently on two clones while un-synced
changes existed on both sides, across a release whose schema migrations
reshape a primary key. Concretely: the
[#4259](https://github.com/gastownhall/beads/issues/4259) incident — clones
straddling the `0041`/`0043`/`0050` reshapes of `dependencies` (v1.0.4 →
v1.0.6) hit exactly this on the first post-upgrade pull if both sides had
unpushed dependency edits.

The remote-migrate prevention gate (v1.0.6+) exists to stop this from being
created: it refuses to auto-migrate a remote-backed database and tells you to
designate a single migrator. This playbook is for when the fork already
exists.

**Recovery: bootstrap from one canonical clone**

The forked histories cannot be merged, so one side must be chosen as
canonical and every other clone re-cloned from it. Issue *data* survives via
JSONL export/import; only the un-mergeable Dolt *history* is discarded on the
non-canonical clones.

### 1. Pick the canonical clone

Usually the most complete / most recently active clone. To compare, run on
each clone (read-only):

```
bd stats
bd dolt status
```

### 2. On the canonical clone: upgrade, migrate, force-push

```
bd version                 # confirm the new bd binary
bd doctor                  # sanity-check before publishing
bd dolt push --force       # make the remote authoritative
```

(`bd`'s migration gate may block here; that is exactly the designated-migrator
case the gate is asking about — follow the guidance it prints on the canonical
clone.)

### 3. On EVERY other clone: save local-only work, re-clone, re-apply

```
bd export --all -o /tmp/beads-local.jsonl    # safety net for un-synced work
rm -rf .beads/dolt                           # discard the un-mergeable history
bd bootstrap                                 # re-clone from the remote
bd import /tmp/beads-local.jsonl             # re-apply local-only work
```

`bd import` has upsert semantics: issues that only existed on this clone are
re-created, newer local edits are applied, and rows older than what the
remote already has are skipped. Spot-check with `bd stats` afterwards.

### Prevention (upgrades across PK-reshaping migrations)

- **Sync before upgrading**: `bd dolt push` + `bd dolt pull` on every clone
  while all clones still run the *old* version, then stop editing. Once the new
  binary is installed, `bd dolt push`/`bd dolt pull` are gated too, so this must
  happen first.
- **One designated migrator**: upgrade one machine, let it migrate, then
  `bd dolt push`.
- **Every other clone adopts, does not pull**: after the migrator pushes, each
  other clone upgrades the binary and runs `bd bootstrap` to adopt the migrated
  database. `bd dolt pull` is *refused* while the clone still has pending
  migrations, so do not rely on it; the "sync before" step above is what
  preserves these clones' work, because `bd bootstrap` replaces the local
  database.
