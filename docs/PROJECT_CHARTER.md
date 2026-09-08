# Project Charter

This document defines the product boundary for beads. It is the source of
truth for deciding whether proposed work belongs in core, belongs in an
integration or plugin, belongs in an orchestration layer, or should stay
outside the project.

Beads is a focused issue tracker for AI-supervised development. It should stay
small enough to remain reliable, understandable, and composable.

## Core Scope

Beads owns issue tracking primitives:

- issues and issue lifecycle
- dependency relationships and readiness
- labels, comments, status, priority, and assignment
- metadata attached to issues
- local CLI workflows around those concepts
- import, export, sync, backup, and recovery for beads data
- integrations that translate external tracker data into beads concepts

Within those boundaries, the project should absorb useful contributor work
when practical. If a contribution has value but does not fit as submitted,
prefer preserving the value by simplifying it, moving it to metadata, routing
it to an integration or plugin, cherry-picking the reusable part, or
reimplementing the use case in a smaller design.

## Orchestration Boundary

Beads should not know about orchestration layers built on top of it. Systems
such as Gastown, Gas City, schedulers, swarms, release coordinators, and future
workflow engines may use beads, but beads should not encode their concepts in
core.

Core beads can expose stable issue data, metadata, CLI output, and documented
extension points. The orchestration layer owns orchestration policy: agent
routing, task assignment strategy, model choice, retry plans, scheduling,
workflow semantics, and cross-system coordination.

When orchestration needs extra per-issue data, prefer issue metadata before
adding first-class fields or commands.

## Storage Boundary

Beads should not become a storage engine. Dolt provides storage, versioning,
sync, merge behavior, concurrency, and crash safety. Beads should put data in
and pull data out through the storage boundary.

Storage-engine details should not leak into beads packages unless they are part
of a deliberate storage interface. Avoid beads-side flocks, engine
introspection, storage-specific retry loops, crash-recovery workarounds, or
schema poking that belongs in Dolt or the Dolt driver.

If the current storage interface cannot express a needed operation, widen the
interface or route the issue to the driver instead of embedding storage-engine
logic in core.

## Schema Boundary

The database schema is considered stable. Schema changes are allowed when there
is a pressing product or correctness need, but they should not be the first
answer to extension requests.

Use issue metadata first when:

- the data is specific to one integration, orchestrator, or team workflow
- the data is advisory rather than part of beads' core issue model
- the data can be represented as JSON without harming queryability
- the shape may evolve before it deserves a stable CLI or schema contract

Promote metadata to first-class schema only when the field has broad, durable
meaning for beads itself and the migration cost is justified.

## Integration Boundary

Tracker integrations are adoption bridges, not a second product surface. They
should map external tracker data into beads concepts and keep the dependency
graph useful. They should not replicate tracker UIs, notification systems,
credential vaults, webhook gateways, or cross-tracker automation.

See [Integration Charter](INTEGRATION_CHARTER.md) for the detailed policy for
GitHub, GitLab, Jira, Linear, Azure DevOps, and similar tracker integrations.

## Review Posture

These boundaries are fences, not bounce messages. Maintainers should not reject
useful work reflexively just because the first version crosses a boundary.

For pull requests and proposals:

- identify the contributor value first
- keep the part that belongs in core when possible
- move boundary-crossing behavior to metadata, integrations, plugins, or
  external tools when that preserves the use case
- preserve attribution when transforming, cherry-picking, or reimplementing
  contributor work
- explain clearly when a feature belongs outside beads

Use request-changes or rejection only after considering whether the project can
absorb, transform, or reroute the useful part.

## Related Documents

- [Integration Charter](INTEGRATION_CHARTER.md) - tracker integration scope
- [Issue Metadata](METADATA.md) - metadata extension point
- [Architecture](ARCHITECTURE.md) - data model and storage architecture
- [Maintainer PR Guidelines](../PR_MAINTAINER_GUIDELINES.md) - PR triage and
  contributor handling
