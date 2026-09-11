# Direct-local ownership handoff contract

`internal/ownershiphandoff` is the explicit Beads-side boundary for handing a
GC-managed direct-local Dolt server to `bd`. Normal startup never invokes it.

Requests identify a canonical real-path Dolt root, database, workspace, local
endpoint, and current owner (`legacy-gc`). Symlinked, missing, non-canonical,
remote, or conflicting identities are rejected before any provider hook runs.
The operation records `prepared`, `target_configured`, `old_owner_stopped`,
`verified`, and `committed` in an atomic journal; each write is fsynced and its
containing directory is fsynced after the rename, so a checkpoint cannot be
lost while the effect it guards survives. Retries resume from the last phase
and committed replays are no-ops. Hooks own provider-specific snapshots,
validation, lifecycle, and artifact retirement; the package never guesses at
or kills an external process. Because a retry resumes from the last durable
phase, every hook except `Commit` must be idempotent under re-invocation, and
the legacy-owner stop is reserved in the journal before it is attempted so a
partial stop is reported as a mutation. Failures remain owned by `legacy-gc`
and are journaled with a stable error code. The commit hook has a durable
in-progress checkpoint and a post-hook completion checkpoint; a commit is
ambiguous whenever it was reserved but not recorded as complete — including
when the hook itself returned an error, which may have applied part of its
effect — and an ambiguous commit refuses to advance unless the provider
supplies an explicitly idempotent replay hook.
The per-journal advisory lock is persistent and kernel-released rather than an
`O_EXCL` marker, so a crashed process cannot leave a stale file that wedges
future retries.

## Identity and invocation

`bd migrate ownership-handoff` is the only invocation path; nothing else in
`bd` constructs a handoff request. Request identity includes the canonical Gas
City root that owns the legacy server, so the same Dolt scope under a different
city is a different handoff: it is refused as `identity_conflict` rather than
resumed. A journal written before `city_root` joined the request — pre-release
builds of this series only — decodes with an empty city root and therefore
conflicts with every request a current binary makes; discarding such a journal
is safe when it records no mutation, and the refusal says so. Every
`identity_conflict` refusal reports the journaled mutation state in `mutates`,
whichever entry point produced it, so a `--json` caller that never sees the
refusal text reads the same answer from the field.

Provider hooks are resolved while the journal lock is held, after the request
and any existing journal have been validated, so a conflicting journal writer
cannot win the race between preflight and execution. `mutation_occurred` is
persistent operator state rather than an inference from the phase: once a hook
reports that it mutated the scope, the journal carries that fact across
retries, so a later non-mutating refusal cannot report the handoff as clean.
Commit is not trusted to the phase machine alone — the commit hook must
re-prove that the legacy owner is gone (a `process_missing` refusal on a fresh
inspect) before ownership moves to `bd`.

## Provider obligations

This package enforces only the Beads side of the protocol. The peer lives in
another codebase, so the following obligations are stated here and pinned by
in-tree fakes rather than by the real provider:

- `StopLegacy` must treat an identity-matching process that is already gone as
  success — `result=stopped` with `mutates=true` — not as a refusal. A crash
  between a successful stop and its `old_owner_stopped` checkpoint leaves the
  journal at `target_configured` while the owner is in fact stopped; the retry
  re-invokes the stop hook. A provider that refuses that retry (for example
  with `process_missing`) wedges the handoff mid-migration with the server
  down and no mutation recorded — the untruthful `mutates=false` the journaled
  mutation flag exists to prevent. Pinned from both directions by
  `TestGCProviderResumeCompletesWhenStopTreatsMissingOwnerAsStopped` and
  `TestGCProviderResumeWedgesWhenStopRefusesMissingOwner`.
- `Configure` must be non-mutating. Mutation reporting is honored on the
  `StopLegacy` failure path only, so a `Configure` that mutates the target and
  then fails is reported as `mutates=false`.
- A post-stop inspect must keep reporting `owner=legacy-gc`, and must keep
  answering `process_missing` for the stopped scope until commit completes. A
  response naming another owner fails the protocol decode, and an eager
  provider-side cleanup that answers `state_missing` instead wedges a resumed
  handoff at `old_owner_stopped`.
