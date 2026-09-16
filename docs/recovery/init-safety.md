---
title: Recovery Playbooks
description: Step-by-step recovery for bd init and bd dolt push/pull refusals, including the primary-key fork playbook
---

Last reviewed: 2026-09-08

Freshness source: `cmd/bd/init.go`, `cmd/bd/init_safety.go`,
`cmd/bd/init_safety_test.go`, `cmd/bd/init_safety_help.go`, and
`cmd/bd/dolt.go`.

This document lives next to the ADRs and matches the structure of `bd`'s
error messages: each named refusal in `bd init` and `bd dolt push`/`pull`
has a labeled anchor here with step-by-step recovery instructions. The
`bd dolt push`/`pull` fork refusal deep-links its anchor directly; since
[#5310](https://github.com/gastownhall/beads/pull/5310) the `bd init`
refusals point at `bd help init-safety`, which links this document as a
whole — match those by exit code and symptom text.

See also: `bd help init-safety`, and
[ADR 0002 — `bd init` safety invariants](https://github.com/gastownhall/beads/blob/main/engdocs/adr/0002-init-safety-invariants.md).

## Table of contents

- [init-force-refused — `bd init --force`/`--reinit-local` refused because origin has Dolt history](#init-force-refused)
- [init-token-missing — a destructive re-init refused because `--destroy-token` is missing or wrong](#init-token-missing)
- [init-local-exists — `bd init --reinit-local` refused because local data already exists](#init-local-exists)
- [pk-fork-refused — `bd dolt pull`/`push` refused because a table has different primary keys in its common ancestor](#pk-fork-refused)
- [re-clone-gotchas — two gotchas hit during manual re-clone recovery: damaged stores left inside `data_dir`, and a fresh clone missing clone-local tables](#re-clone-gotchas)

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

If you set aside the old `.beads/dolt` instead of deleting it, or `bd list`
fails right after this with `table not found: leases`, see
[re-clone-gotchas](#re-clone-gotchas) below before you do anything else.

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

Or, re-initializing over existing local issues with no TTY:

```
Refusing to destroy N issues in non-interactive mode.
  See 'bd help init-safety' for the required --destroy-token format.
```

**Why this happens**

You're running non-interactively (CI, agent, piped input) and asked for a
destructive re-init. Destructive operations cannot be authorized silently,
so `bd` requires `--destroy-token` in place of the interactive confirmation
it cannot prompt for.

Both destructive paths need the token, not just the cross-boundary one:

- `--discard-remote`, which would discard the remote's Dolt history.
- plain `--reinit-local` over existing local issues, which would destroy
  them (see [init-local-exists](#init-local-exists)).

**Recovery paths**

### 1. Run interactively

Re-run in a TTY. `bd init --reinit-local --discard-remote` will prompt
you to type the destroy-token at confirmation time.

### 2. Supply the token explicitly (CI/automation)

The token format is `DESTROY-<issue-prefix>`. For a project whose issue
prefix is `bd`:

```
# Destroys local issues only:
bd init --reinit-local --destroy-token=DESTROY-bd

# Also discards the remote's Dolt history:
bd init --reinit-local --discard-remote --destroy-token=DESTROY-bd
```

Automation should template the token from project state, not from error
output. See [ADR 0002 — Invariant 4](https://github.com/gastownhall/beads/blob/main/engdocs/adr/0002-init-safety-invariants.md)
for why the token is never echoed in `bd`'s error messages.

---

## init-local-exists

**Exit code:** `11` (`ExitLocalExistsRefused`) interactively;
`12` (`ExitDestroyTokenMissing`) non-interactively

**Symptom**

Interactive (TTY): you declined the typed `destroy N issues` confirmation.
This is the only path that exits `11`.

```
Type 'destroy N issues' to confirm:
Aborted. Database was NOT modified.
```

Non-interactive (CI, agent, piped input) — note this exits `12`, not `11`:

```
Refusing to destroy N issues in non-interactive mode.
  See 'bd help init-safety' for the required --destroy-token format.
```

**Why this happens**

Local `.beads/` has existing issues. `bd init --reinit-local` (or its
deprecated alias `--force`) would permanently destroy them, so `bd` demands
an explicit confirmation first: the typed prompt in a TTY, and a
`--destroy-token` when there is no TTY. This applies to `--reinit-local` on
its own — `--discard-remote` is not required to trigger it.

A plain `bd init` over an initialized workspace does not reach either code:
the local-safety guard refuses it with an ordinary error before this point.

**Recovery paths**

### 1. Export first, then proceed

```
bd export > issue-export.jsonl

# Interactive: confirm at the typed prompt.
bd init --reinit-local

# Non-interactive: the token stands in for the prompt. Without it this
# re-runs straight back into the exit-12 refusal above.
bd init --reinit-local --destroy-token=DESTROY-<issue-prefix>
```

`issue-export.jsonl` lets you re-import individual issues if needed. It is not
a full database backup; use `bd backup` when the Dolt database is healthy
enough to create a restorable backup before reinitializing.

### 2. Investigate why you hit this

If you did NOT expect `bd init` to be the right command here, run
`bd doctor` first — you may be looking at a server config issue that a
re-init won't fix.

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

Doing this by hand (moving `.beads/dolt` aside instead of `rm -rf`, or
skipping straight to `bd list`) can hit either of two live gotchas — see
[re-clone-gotchas](#re-clone-gotchas) below.

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

---

## re-clone-gotchas

Two gotchas hit during a live manual re-clone recovery (issue ga-vrq5pu),
each of which cost real time because the symptom looks nothing like the
cause. Both
apply any time you set aside or replace a Dolt database directory by hand —
during the [pk-fork-refused](#pk-fork-refused) playbook above, the
[init-force-refused](#init-force-refused) `bd bootstrap` path, or any other
manual re-clone.

### Gotcha 1 — a damaged/set-aside store must go OUTSIDE data_dir

**Symptom**

```
root hash doesn't exist: <hash>
```

...printed repeatedly as the Dolt sql-server crash-loops under its
supervisor/watchdog. Nothing in that message mentions a stray directory, so
it does not look like "you left a directory lying around."

**Why this happens**

The sql-server treats *every* subdirectory of its `data_dir` (default
`.beads/dolt/`, overridable via `BEADS_DOLT_DATA_DIR` or the `dolt_data_dir`
field in `metadata.json`) as its own database and tries to load it. If you
move a damaged or superseded database directory aside but leave it *inside*
`data_dir` (for example, renaming `.beads/dolt/mydb` to
`.beads/dolt/mydb.bak` instead of moving it out of `.beads/dolt/`
entirely), the server tries to load the damaged copy too and dies on it —
even though the healthy database sitting right next to it is fine.

**The fix**

When you set a database directory aside by hand, move it *outside*
`data_dir` — e.g. up to `/tmp/` or a sibling of `.beads/`, never to a
sibling path still under `.beads/dolt/`. This is exactly what the automated
recovery path already does: `bd doctor --fix`'s corrupt-manifest repair
renames the *whole* `data_dir` directory itself to a timestamped
`.<ts>.corrupt.backup` sibling (`data_dir` → `data_dir.<ts>.corrupt.backup`),
landing next to `data_dir`, not inside it. Doing the equivalent by hand but
leaving the renamed copy nested under the original `data_dir` is what
triggers this gotcha; see `bd doctor --fix` for the automated, gotcha-free
version of this move.

### Gotcha 2 — a fresh clone needs `bd migrate schema`

**Symptom**

```
table not found: leases
```

(or a similar "table not found" error for `wisps`, `events`,
`local_metadata`, or another clone-local table). A supervisor or agent
harness that expects to load session beads right after a fresh clone fails
here.

**Why this happens**

A handful of tables — `leases`, `wisps`, `wisp_*`, `events`, `bd_events_*`,
`local_metadata`, `ignored_schema_migrations`, `repo_mtimes` — are
dolt-ignored, clone-local tables: they exist on a running database but are
deliberately excluded from what `bd dolt push`/`pull`/clone transfers, so a
fresh clone starts without them. `bd`'s ordinary open does not recreate
them.

**The fix**

```
bd migrate schema
```

No `--force` needed. This replays the clone-local tables and prints:

```
✓ Schema already at v<N>
```

**That output is expected and reassuring, not an error** — it means the
*versioned* schema was already current; the clone-local tables have now
been (re)created regardless. Run this once after any fresh clone or
`bd bootstrap`, before relying on `bd list` or any other command that reads
session state.
