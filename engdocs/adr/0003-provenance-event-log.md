# ADR-0003: Provenance Event Log

## Status

Proposed

## Date

2026-06-19

## Decision Drivers

- **Outcome linkage**: a runtime (orchestrator, git hook, CI) needs a durable
  place to record that work on an issue was *claimed*, *handed off*, *committed*,
  or *landed*, and to bind that fact to a structured external artifact (a git
  SHA, a PR, a work-id, a transcript, a branch).
- **Primitive, not policy**: the recording surface must be usable by *any*
  runtime without bd knowing anything about sessions, agents, or a specific
  orchestrator's vocabulary.
- **Honesty under reconstruction**: a consumer that later backfills derived
  events must be able to distinguish them from first-party recordings, so a
  "read-first" honesty filter can exclude reconstructed rows.
- **Append-only auditability**: provenance is a log of things that happened.
  Individual events are immutable — there is no UPDATE or DELETE operation on a
  single event after it is recorded.

## Context

bd already has an `events` table (migration 0005): a two-value field-mutation
audit trail (`old_value` / `new_value`, plus an `event_type` like
`status_changed`). It answers "what field changed on this issue, when, by whom."

It does *not* answer "what external artifact is this issue bound to" — there is
no typed place to record that commit `abc…` landed work for `bd-123`, or that a
PR was opened, or that a transcript captured the working session. Producers today
have to overload labels, comments, or `external_ref`, none of which carry a
typed `kind` / `ref_kind` pair or an event-time distinct from ingest-time.

This primitive was first proven in a TypeScript prototype; this ADR records the
durable upstream version.

## Decision

Add a dedicated, append-only `provenance_events` table (migration 0063) and a
`bd provenance` command group with exactly three verbs: `record`, `log`,
`by-ref`. There is deliberately **no** update or delete verb.

**Append-only is at the event level.** There is no UPDATE or DELETE operation on
an individual event. An event's lifecycle is bound to its issue: it is removed
only if the issue itself is deleted (`ON DELETE CASCADE`), identical to the
`events` audit table (migration 0005). In bd, `issues` is the source of truth, so
a provenance event for a deleted issue is meaningless; cascading on issue delete
matches the existing audit-log precedent rather than leaving orphaned rows.

**Wisp demotion is a delete for this purpose.** Demoting a permanent issue to a
wisp (`bd update <id> --ephemeral`) deletes the row from `issues` — a wisp is a
separate table, not a status on the same row — so it drops that issue's
provenance events for the same `ON DELETE CASCADE` reason as any other issue
deletion. This is an accepted consequence, not a gap: wisps are cheap,
short-lived, ephemeral-by-design records, and there is deliberately no wisp
counterpart to `provenance_events` to preserve history across the demotion.

Key design points:

- **Opaque identifiers.** `actor` and `ref` are opaque strings that bd never
  interprets. Only `kind` and `ref_kind` are structurally validated against
  closed sets (`kind` ∈ {cut, claim, suspend, resume, handoff, commit, land,
  used}; `ref_kind` ∈ {git-sha, pr, work-id, transcript, branch}). When
  `ref_kind = git-sha`, the `ref` must match `^[0-9a-f]{40}$`. This is the only
  shape bd asserts — consistent with ZFC: bd validates structure, never meaning.

- **occurred_at vs created_at.** `occurred_at` (event-time) is separate from
  `created_at` (ingest-time, `DEFAULT CURRENT_TIMESTAMP`), because a producer
  such as a git hook may record a fact *after* it happened. Reads order by
  `occurred_at` (nulls last) then `id`.

- **Idempotent recording.** `bd provenance record` computes a deterministic id
  from `source:issue:kind:(ref or occurred_at)` and inserts with `INSERT
  IGNORE`, so a producer firing twice is a harmless no-op (`inserted=false` on
  the second call). The id is always content-addressed — a caller-supplied `id`
  is never honored, so idempotency cannot be bypassed. Any event recorded
  *without* a `ref` requires `--at` so the id is caller-owned rather than minted
  from the wall clock — otherwise two distinct ref-less events would collapse to
  one id. This is enforced at the store boundary (in `ValidateProvenanceEvent`),
  not as a CLI kind-list, so every caller is covered.

- **Reserved source for backfill.** The source value `ingest-backfill` is
  rejected by the record path (case-insensitively). It is reserved for
  derived/reconstructed events written by consumers, so a read-first honesty
  filter can exclude them. Real producers must name their own source.

- **`ref` is `VARCHAR(255)`, indexed.** A SHA or a PR URL fits comfortably, and
  the column is directly indexable in dolt/MySQL (unlike `TEXT`). Refs longer
  than 255 chars are out of scope for this column.

## Considered Alternatives

### Extend the existing `events` table

Rejected. `events` is the hot field-mutation audit path: a `(old_value,
new_value, event_type)` shape answering "what changed." Provenance is a different
concern — a typed binding from an issue to a *structured external artifact*, with
its own closed `kind`/`ref_kind` vocabulary, an event-time/ingest-time split, and
idempotent deterministic ids. Bolting these columns onto `events` would (a)
widen and complicate the most frequently written table, (b) mix two unrelated
reasons-to-change in one schema (SRP violation), and (c) force every `events`
reader to reason about NULL provenance columns.

bd already has a precedent for purpose-specific event tables: `wisp_events`
(migration 0021) is a parallel event log for ephemeral wisps rather than a column
set grafted onto `events`. A separate `provenance_events` table follows the same
established pattern and keeps the hot audit path untouched.

### Make `actor` / `ref` typed and interpreted by bd

Rejected. If bd parsed `actor` into a session/agent identity or `ref` into a
known orchestrator's work-id format, the table would only be usable by that one
runtime. Keeping them opaque makes provenance a *primitive*: a git hook, a CI
job, and an orchestrator can all record into the same log, and consumers attach
whatever semantics they need at read time. bd's only job is to store the binding
and validate its structural shape.

### Allow update/delete

Rejected. Provenance is a log of facts that occurred; mutating it would destroy
auditability and the idempotency guarantee. The append-only constraint is
enforced at the event level by simply not providing UPDATE or DELETE operations
on individual events. (An event is still removed when its owning issue is deleted
via `ON DELETE CASCADE` — see the Decision section. This is not an event-level
delete path; it is the same issue-bound lifecycle the `events` audit table has.)

## Distributed merge

`provenance_events` is **not** in the merge auto-resolve allowlist today. A
`bd pull` that brings in concurrent inserts from two clones will leave the
conflict for the operator to resolve manually, like any other non-allowlisted
table.

This is safe to auto-resolve in a future change: the table is append-only, its
ids are deterministic and content-addressed, and recording uses `INSERT IGNORE`,
so concurrent inserts from independent clones are commutative (the union of rows
is the correct merge regardless of order, and duplicate ids are idempotent
no-ops). Adding `provenance_events` to the auto-resolve allowlist is deferred as
a separate change so the merge-policy surface is reviewed on its own.
