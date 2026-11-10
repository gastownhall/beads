# Direct-local ownership handoff contract

This contract defines the Beads-side boundary required before Gas City can
retire a legacy GC-managed direct-local Dolt server. It is intentionally an
explicit operation; normal `bd` or `gc` startup never invokes it.

## Request identity

The handoff request must name the canonical, real-path Dolt data root, Dolt
database, Beads project/workspace identity, endpoint (host/port or socket),
and current owner (`legacy-gc`). The provider rejects missing, symlinked,
non-canonical, or conflicting identity. It must never create a replacement
root or database when identity cannot be proved.

## Journaled phases

The operation persists an atomic journal beside the Beads metadata. Every
retry resumes from the last durable phase and a committed retry is a no-op.

1. `prepared`: snapshot metadata, config, endpoint, ownership, and sentinel.
2. `target_configured`: validate that bd can open the exact root/database.
3. `old_owner_stopped`: stop only the positively identified GC owner through
   its existing lifecycle interface; a live or unknown process fails closed.
4. `verified`: start through bd and verify endpoint, database identity, and
   sentinel rows.
5. `committed`: atomically record owner `bd` and retire only GC artifacts whose
   ownership matches the snapshot.

Any failure before `committed` leaves `legacy-gc` authoritative. Rollback
restores the byte/row/config snapshot and never kills an unverified process.
The journal records the error and phase so operators can retry or inspect it.

## Front-door shape

The eventual CLI/API must expose dry-run, execute, and resume behavior with a
typed result containing `phase`, `owner`, `root`, `database`, `endpoint`,
`mutates`, and `error_code`. Text and JSON responses share the same values;
non-zero exits are required for every refusal. Dry-run and all refusals have
`mutates=false` and must not open a provider.

External TCP/Unix endpoints, embedded stores, wrong-root/database requests,
missing or stale runtime state, concurrent starts, and duplicate-owner
conditions are refusal boundaries. Existing rows, event history, metadata,
locks, PID records, and unrelated files are no-mutation artifacts in those
cases.
