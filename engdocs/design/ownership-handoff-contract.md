# Direct-local ownership handoff contract

`internal/ownershiphandoff` is the explicit Beads-side boundary for handing a
GC-managed direct-local Dolt server to `bd`. Normal startup never invokes it.

Requests identify a canonical real-path Dolt root, database, workspace, local
endpoint, and current owner (`legacy-gc`). Symlinked, missing, non-canonical,
remote, or conflicting identities are rejected before any provider hook runs.
The operation records `prepared`, `target_configured`, `old_owner_stopped`,
`verified`, and `committed` in an atomic journal. Retries resume from the last
phase and committed replays are no-ops. Hooks own provider-specific snapshots,
validation, lifecycle, and artifact retirement; the package never guesses at
or kills an external process. Failures remain owned by `legacy-gc` and are
journaled with a stable error code. The commit hook has a durable in-progress
checkpoint and a post-hook completion checkpoint; an ambiguous commit refuses
to advance unless the provider supplies an explicitly idempotent replay hook.
The per-journal advisory lock is persistent and kernel-released rather than an
`O_EXCL` marker, so a crashed process cannot leave a stale file that wedges
future retries.
