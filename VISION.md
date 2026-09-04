# Vision

`bd` exists so that an AI coding agent's work survives its context window.
It is a distributed, dependency-aware issue tracker powered by Dolt, living inside the repository whose work it tracks.
The pain it removes is concrete: agents running long-horizon tasks lose context between sessions, relearn project state from stale markdown plans, duplicate work nobody recorded, and block each other silently because nothing knows what is ready, claimed, or done.
Beads turns that rot into a graph: issues with dependencies, priorities, and status, where `bd ready` always answers what can be worked on now and every claim is atomic.
It serves AI coding agents first, and the humans supervising them second - operators running one agent or a fleet, on one machine or many.
It deliberately does not serve human teams that want web dashboards, cross-team portfolio views, and notification workflows; that is the job of the external trackers beads can sync with.
It owns exactly one thing: issue tracking primitives - issues and their lifecycle, dependencies and readiness, labels, comments, priority, assignment, metadata, and the CLI workflows, sync, backup, and recovery that keep that data trustworthy.

## An agent's memory outlives its context

Without beads, task state lives in markdown TODO lists and chat logs that no query can trust and no merge can reconcile.
Beads gives every agent the same lifecycle: create a bead, see unblocked work with `bd ready`, claim it atomically with `bd update --claim`, close it, and let the graph release whatever it blocked.
Hash-based IDs like `bd-a1b2` exist so two agents on two branches can create work in parallel and merge without collision.
Hierarchical epics, graph links such as `relates-to`, `supersedes`, and `discovered-from`, and cross-repo dependencies let a fleet decompose work without losing the map.
Every command speaks JSON, because the primary reader is a program, not a person.

## The database is the source of truth

Dolt is the foundation: a version-controlled SQL database providing cell-level merge, native branching, and sync through the project's own git remote under `refs/dolt/data`.
Beads is offline-first and works without git at all; the Dolt database is the store, and git integration is optional.
Embedded mode runs Dolt in-process for the single-writer majority; server mode serves multiple concurrent writers; parity between the two is tested, not assumed.
`.beads/issues.jsonl` is an export for viewers and interchange, never the sync protocol, and the docs say so explicitly so that nobody builds on the wrong channel.
A schema version guard refuses to open a database migrated by a newer binary, trading a hard stop with named remedies for cryptic SQL failures downstream.
Backup, `bd bootstrap` for fresh clones, and migration discipline exist because agent memory that loses history is not memory.

## Primitives, not policy

Beads owns issue tracking primitives and refuses to own policy built on top of them.
Schedulers, swarms, and workflow engines may consume beads; beads encodes none of their concepts, because agent routing, task assignment strategy, model choice, retries, and scheduling belong to the orchestration layer.
Beads is not a storage engine: storage, versioning, merge behavior, concurrency, and crash safety belong to Dolt, reached through the driver interface, with a linter rule mechanically keeping storage-engine imports inside the storage layer.
The schema is stable by policy; extension requests reach for issue metadata first and promote to schema only when a field has broad, durable meaning and the migration cost is justified.
Tracker integrations are adoption bridges that map external tracker data into beads concepts, not a second product surface replicating other systems' UIs, notifications, or credential vaults.

## Context is the scarce resource

Agents pay for every token they read, so beads spends them carefully.
`bd prime` injects workflow context and persistent memories; `bd remember` stores insights so agents stop hand-maintaining MEMORY.md files.
Compaction summarizes old closed tasks so long-lived databases stop eating the context window.
Wisps make operational work - release checklists, health patrols - ephemeral by default, excluded from sync and purged once done, because work with no audit value should leave no storage burden.
Terminal output follows a data-ink discipline: semantic color, no decoration, and no emoji noise where a symbol and a label would do.

## Honest failure beats silent success

An agent that trusts a false answer is worse off than one that gets an error.
Beads treats silent wrongness as a cardinal bug class: a pull that never landed must not report success, a broken memory plane must not read as an empty one, a count that misses rows must not answer zero.
Errors are actionable: the schema guard names the stale binary, the fix, and the escape hatch, and `bd doctor` diagnoses with commands that actually exist.
When embedded and server behavior drift, the drift is a defect to close, and the parity tests exist to keep both modes honest.

## Contributors are throughput

The maintainer philosophy is to help contributors reach the finish line, not to gatekeep them.
External contributor PRs have priority; maintainers review, build on contributor branches, preserve tests and attribution, and absorb value locally rather than superseding silently.
Contributor and maintainer routing keeps fork-side planning out of upstream PRs, so an agent can adopt beads on a project it contributes to without polluting that project.
Prior-art preflight checks older open PRs and issues, so community work is never silently duplicated by competing maintainer changes.

## Scope

Non-goals:
- Orchestration policy: agent routing, scheduling, model choice, retry plans, workflow semantics.
- Storage-engine behavior: beads-side flocks, engine introspection, crash-recovery logic around Dolt.
- Human-team surfaces: web UIs, dashboards, notification systems, credential vaults, webhook gateways.
- A hosted service: beads is local-first, installed once, used everywhere.

A change aligns when it makes agent memory more trustworthy across sessions, machines, and merges; when it keeps queries deterministic and output legible to programs; when it preserves parity between storage modes; when failure gets more honest; or when it keeps the project small enough to remain reliable, understandable, and composable.
A change should be resisted when it encodes orchestration policy, leaks storage-engine detail into core, reaches for schema when metadata would do, grows a second product surface, serves human dashboards over agent workflows, spends context where a query would do, or lets a silent success survive because no test was watching for it.
