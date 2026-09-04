# Vision

`bd` exists so that an AI coding agent's work survives its context window.
It is a distributed, dependency-aware issue tracker powered by Dolt, living inside the repository whose work it tracks.
The pain it removes is concrete: agents running long-horizon tasks lose context between sessions, relearn project state from stale markdown plans, duplicate work nobody recorded, and block each other silently because nothing knows what is ready, claimed, or done.
None of this is hypothetical: an operating agent fleet moved dozens of homes off private markdown backlogs onto one shared graph, because work invisible to the graph is work that gets duplicated, lost, or quietly forgotten.
Beads turns that rot into a graph: issues with dependencies, priorities, and status, where `bd ready` always answers what can be worked on now and every claim is atomic.
It serves AI coding agents first, and the humans supervising them second - operators running one agent or a fleet, on one machine or many.
The fleet case is the demanding one: many concurrent agents on one shared graph, where a per-home side tracker means live work the rest of the fleet cannot see.
It deliberately does not serve human teams that want web dashboards, cross-team portfolio views, and notification workflows; that is the job of the external trackers beads can sync with.
It owns exactly one thing: issue tracking primitives - issues and their lifecycle, dependencies and readiness, labels, comments, priority, assignment, metadata, and the CLI workflows, sync, backup, and recovery that keep that data trustworthy.

## An agent's memory outlives its context

Without beads, task state lives in markdown TODO lists and chat logs that no query can trust and no merge can reconcile.
Beads gives every agent the same lifecycle: create a bead, see unblocked work with `bd ready`, claim it atomically with `bd update --claim`, close it, and let the graph release whatever it blocked.
Hash-based IDs like `bd-a1b2` exist so two agents on two branches can create work in parallel and merge without collision.
Hierarchical epics, graph links such as `relates-to`, `supersedes`, and `discovered-from`, and cross-repo dependencies let a fleet decompose work without losing the map.
The lifecycle must be drivable end to end by programs - ready, blocked, claim, dependencies, close - not just list and close, because an orchestrator that cannot record a block cannot trust the ready answer.
Dependency edges are written at the moment a tool learns one, so readiness reflects real causality instead of a snapshot someone remembered to edit.
Every command speaks JSON, because the primary reader is a program, not a person.

## The database is the source of truth

Dolt is the foundation: a version-controlled SQL database providing cell-level merge, native branching, and sync through the project's own git remote under `refs/dolt/data`.
Beads is offline-first and works without git at all; the Dolt database is the store, and git integration is optional.
Embedded mode runs Dolt in-process for the single-writer majority; server mode serves multiple concurrent writers; parity between the two is tested, not assumed.
`.beads/issues.jsonl` is an export for viewers and interchange, never the sync protocol, and the docs say so explicitly so that nobody builds on the wrong channel.
A schema version guard refuses to open a database migrated by a newer binary, trading a hard stop with named remedies for cryptic SQL failures downstream.
Determinism holds under concurrent writers, not just sequential ones: list and JSON output must be stable while other agents mutate the graph.
Speed is part of correctness at fleet scale: a single-record lookup must stay fast as the graph grows, because a slow answer stalls real agent startups, and a lookup that fans out over the whole backlog is a defect no matter how correct its result.
Every mutation is attributable: claims and closes record which actor made them, so concurrent homes and tasks stay distinguishable in the audit trail.
The issue graph is sensitive operational data: state directories carry restrictive permissions, sync targets stay private, and backups are actually restorable.
Backup, `bd bootstrap` for fresh clones, and migration discipline exist because agent memory that loses history is not memory.

## Primitives, not policy

Beads owns issue tracking primitives and refuses to own policy built on top of them.
Schedulers, swarms, and workflow engines may consume beads; beads encodes none of their concepts, because agent routing, task assignment strategy, model choice, retries, and scheduling belong to the orchestration layer.
Beads is not a storage engine: storage, versioning, merge behavior, concurrency, and crash safety belong to Dolt, reached through the driver interface, with a linter rule mechanically keeping storage-engine imports inside the storage layer.
The schema is stable by policy; extension requests reach for issue metadata first and promote to schema only when a field has broad, durable meaning and the migration cost is justified.
Tracker integrations are adoption bridges that map external tracker data into beads concepts, not a second product surface replicating other systems' UIs, notifications, or credential vaults.
What beads owes the layer above is a complete, mechanical lifecycle - ready, blocked, claim, close with recorded evidence - so orchestration tools can bind their own spawn, teardown, and landing steps to graph state instead of memory.
The same discipline applies to rules: few rules, consistently applied, with a recycling path so constraints do not accumulate until nothing is allowed.
Nothing is built to expire with the current generation of models; scaffolding tuned to today's limitations is scaffolding that will need tearing out.

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
The board must not lie: claiming belongs to starting work and closing belongs to confirmed landing with the evidence recorded, so a record that contradicts live reality is a defect even when every individual command returned success.
Claims of done require live proof on a real machine against a real graph; unit tests alone never verify a workflow.
Search must find what descriptions contain, counts must match the graph, and a report assembled from a stale snapshot is not evidence about the present.
When embedded and server behavior drift, the drift is a defect to close, and the parity tests exist to keep both modes honest.

## Contributors are throughput

The maintainer philosophy is to help contributors reach the finish line, not to gatekeep them.
External contributor PRs have priority; maintainers review, build on contributor branches, preserve tests and attribution, and absorb value locally rather than superseding silently.
Contributor and maintainer routing keeps fork-side planning out of upstream PRs, so an agent can adopt beads on a project it contributes to without polluting that project.
Prior-art preflight checks older open PRs and issues, so community work is never silently duplicated by competing maintainer changes.
An increasing share of defect reports comes from live fleet usage - search semantics, determinism under concurrent writes, field visibility - and every genuine fix travels upstream, keeping downstream installs pinned and converged on upstream rather than diverging into private variants.

## Scope

Non-goals:
- Orchestration policy: agent routing, scheduling, model choice, retry plans, workflow semantics.
- Storage-engine behavior: beads-side flocks, engine introspection, crash-recovery logic around Dolt.
- Human-team surfaces: web UIs, dashboards, notification systems, credential vaults, webhook gateways.
- A hosted service: beads is local-first, installed once, used everywhere.

A change aligns when it makes agent memory more trustworthy across sessions, machines, and merges; when it keeps queries deterministic and output legible to programs; when it preserves parity between storage modes; when failure gets more honest; when it keeps the graph fast, truthful, and attributable under real fleet load; or when it keeps the project small enough to remain reliable, understandable, and composable.
A change should be resisted when it encodes orchestration policy, leaks storage-engine detail into core, reaches for schema when metadata would do, grows a second product surface, serves human dashboards over agent workflows, spends context where a query would do, adds guardrails faster than they can be pruned, or lets a silent success survive because no test was watching for it.
