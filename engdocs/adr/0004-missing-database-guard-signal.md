# ADR 0004 — missing-database init guard: proof-of-prior-init signal

## Status

Proposed — 2026-09-16.

Extends [ADR 0002](0002-init-safety-invariants.md), which established the
`bd init` safety invariants and the scope-bound `--force` / `--reinit-local`
rule. This ADR settles one question ADR 0002 left open: *what evidence proves
a workspace was initialized here before?*

## Context

Two places in `cmd/bd/init.go` must decide, with no opportunity to ask the
user, whether an apparently-missing server-side Dolt database means:

- **(a)** a genuine fresh clone, never initialized on this machine — safe to
  create; or
- **(b)** a workspace that *was* initialized here and whose database is now
  merely unreachable or lost — which must never be silently recreated.

The two sites are `checkExistingBeadsDataAt` (the default `bd init` path,
refuse-by-default) and `guardMissingServerDatabaseAt` (the safety net for
`--reinit-local` / `--force`, which bypass the first guard entirely).

Both used the same signal for the ambiguous case: `cfg.ProjectID != ""`, from
`.beads/metadata.json`, minted by `bd init` and originally added for
cross-project leak detection (GH#2372). That file is **git-tracked by
default** — `defaultGitignoreContent` in `cmd/bd/doctor/gitignore.go` says so
explicitly — so a fresh clone inherits a `project_id` it never earned locally
(GH#2433). Worse, whether it is tracked depends on the consuming repo's own
`.gitignore`, so the signal's meaning varies per repository.

A prior fix in the same neighbourhood corrected a separate bug: both guards
were statting the Dolt **data directory** (`doltserver.ResolveDoltDir`) rather
than this project's own database inside it. That directory always exists once
anything has used the store — per-project mode `MkdirAll`s it in
`ensureDoltInit`, shared-server mode `MkdirAll`s the machine-global
`~/.beads/shared-server/dolt` in `SharedDoltDir()` — so the stat always
succeeded, the missing-vs-present branch was unreachable, and
`--recreate-missing`'s opt-in inside it was dead code. That fix introduced a
fine-grained probe of `<dolt-data-dir>/<database>/.dolt`.

### Why absence can never be promoted to proof

It is tempting to conclude that the fine-grained probe is the sound
replacement and `project_id` is legacy. That does not hold.

*Presence* of `<dolt-data-dir>/<database>/.dolt` is unambiguous proof: the
path is gitignored, so it can only exist because a real local process created
it. Its *absence* is consistent with two histories that must be treated
oppositely:

1. Genuine fresh clone — never initialized here. Safe to create.
2. Initialized here, and the local Dolt storage was subsequently lost — disk
   wipe, volume reset, accidental `rm`, machine reimage. **This is the shape of
   the 2026-08-11 fleet-wide data-loss incident** that motivated the guard.

Local filesystem state cannot distinguish these: it is the same "nothing here"
observation either way. There is no purely-local, low-friction fix.

### Local proof is structurally unavailable for remote hosts

The only code that creates a local `<dolt-data-dir>/<database>/.dolt` is
`doltserver.Start()` (via `ensureDoltInit`), called exclusively from
local-server-management sites. None reaches a remote host. So where
`isRemoteServerHost(host)` is true (`internal/doltserver/physical_root.go`),
the fine-grained probe is **permanently** silent — not only at fresh-clone
time but for the life of that workspace. The probe is a sound partial answer,
not a complete replacement.

## Decision

Formalize what the code already does for local hosts, keep `project_id` plus
`--recreate-missing` as the permanent answer where no local signal can exist,
and close one real gap between the two guards. No new mechanism, no new
persisted signal, no migration.

### Tier 1 — local per-database directory presence

`<dolt-data-dir>/<database>/.dolt`. Applicable whenever the configured host is
local, covering `ServerModeOwned` (local by construction) and `ServerModeExternal`
configurations resolving to loopback, including same-machine shared-server
mode. **Presence is unconditional proof, independent of `project_id`.**

### Tier 2 — `project_id`, gated behind Tier 1's absence

Retained unchanged in mechanism, as the sole signal once Tier 1 is silent or
inapplicable. Its weakness — inherited by clone, absent for pre-GH#2372
workspaces — is **accepted, not fixed**, because there is nothing purely local
to fix it with. The failure direction is deliberate: a false positive (fresh
clone refused) costs a documented flag; a false negative (real data silently
recreated) costs unrecoverable data.

### Resolution path — `--recreate-missing` is the design, not a stopgap

With its opt-in now reachable, reproducible and documented, handing the
operator an explicit per-invocation flag *is* the answer for every case where
no proof is available.

### Per-mode behaviour

| Configured host | Tier 1 applicable | Tier 1 present | Tier 1 absent, `project_id` present | Tier 1 absent, `project_id` absent |
|---|---|---|---|---|
| Local (Owned, or External resolving to loopback) | Yes | Proven | Refuse; `--recreate-missing` required | Coarse-directory permit condition (below) |
| Remote (External, non-loopback) | No — never populated | N/A | Refuse; `--recreate-missing` required | Allow (pre-GH#2372 carve-out) |

Embedded and proxied-server modes use their own existence checks and are out
of scope.

### The coarse-directory permit condition, and its accepted asymmetry

For the last cell, both guards apply a second, coarser local check: allow only
when the Dolt data directory itself is also absent. In `ServerModeOwned` that
directory is per-project, so it is a meaningful signal — "this project has
touched local Dolt storage before". In same-machine shared-server mode it is
the machine-global `~/.beads/shared-server/dolt`, created the moment *any*
project on the machine uses the shared server, so the cell is effectively
always conservative there.

This asymmetry is **accepted**. It fails toward refuse, it is resolved by the
same documented flag, and the affected population only shrinks — every
successful init or `--recreate-missing` run mints a `project_id`. Equalizing it
would need new mechanism for no safety gain.

## Consequences

- No new persisted signal, no schema change, no migration.
- `guardMissingServerDatabaseAt` gains one behavioural fix: it must run the
  Tier 1 probe **unconditionally, before** consulting `project_id`, and apply
  the same coarse-directory permit condition as `checkExistingBeadsDataAt`.
  Previously it returned `nil` immediately on an empty `project_id`, so a
  pre-GH#2372 workspace whose local database was lost was protected under
  plain `bd init` but waved through under `--reinit-local` / `--force` — the
  flags an operator reaches for in a panic were the weaker path.
- `checkExistingBeadsDataAt` does not change.
- The fresh-clone false positive (GH#2433) remains by design for the cells
  where it is unavoidable.
- Both guards cite this ADR, so the question stops being re-litigated each
  review round.

## Alternatives considered

- **A new local-only gitignored init marker.** Sound in principle, but fails
  open across the entire pre-existing installed base until each workspace
  re-inits under it — a real cost during a migration that has no defined end.
  Rejected.
- **Repurposing `.local_version`.** Rejected outright: it is written by bd's
  own `PersistentPreRunE` on any command that resolves a beads dir, including
  the `bd init` invocation under test, before its own guard runs. It proves
  "some bd command ran here", not "init succeeded here".
- **Dropping `project_id` in favour of the data-directory probe.** Rejected:
  see "Why absence can never be promoted to proof".
- **Asking the server to distinguish an empty database from a populated one.**
  Potentially a materially better signal for the fallback population, since it
  asks something authoritative rather than something locally inherited. Out of
  scope here: it needs a definition of "empty" that does not race with
  `--from-jsonl` bootstrap import, and a decision on whether
  `checkDatabaseOnServer`'s `Exists` becoming tri-state affects other callers.
  Worth its own bead if pursued.

## References

- Decision bead: `be-5pjhd`, escalated from round-4 finding 2 on
  [gastownhall/beads#5791](https://github.com/gastownhall/beads/pull/5791).
- Guard sites: `cmd/bd/init.go`, `checkExistingBeadsDataAt` and
  `guardMissingServerDatabaseAt`.
- Guard matrix: `cmd/bd/init_guard_test.go`.
- Operator-facing limits: `docs/recovery/init-safety.md`.
- Host locality predicate: `isRemoteServerHost`,
  `internal/doltserver/physical_root.go`.
- Prior init-safety decisions: [ADR 0002](0002-init-safety-invariants.md).
