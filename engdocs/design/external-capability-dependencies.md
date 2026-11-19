# External capability dependencies

## Problem

An explicit dependency target of the form
`external:<project>:<capability>` is not a local issue. The split dependency
schema stores it in `depends_on_external`, so joins that hydrate local issues
correctly omit it. The same omission currently makes an unsatisfied external
blocker invisible to ready-work queries and dependency trees.

External capability state cannot be materialized safely in `is_blocked`:
shipping happens in another project and does not create a local write that
could refresh the derived column.

## Decision

Resolve explicit external references at both storage and server-UOW policy
seams. This keeps direct, routed, and proxied-server commands on one policy.

The decorator:

- parses `external:<project>:<capability>` references and, when it was built
  with an issue router (see "Cross-prefix issue targets" below), bare
  `blocks` and `conditional-blocks` edges to an issue ID whose prefix differs
  from the source's;
- groups references by project and opens each configured foreign store once,
  read-only, per operation;
- considers a capability satisfied only when the foreign store contains a
  closed issue labeled `provides:<capability>`;
- treats malformed references, missing configuration, unavailable projects,
  and failed foreign reads as unsatisfied;
- emits one stderr warning per unavailable foreign project per command. Foreign
  projects in proxied-server mode are currently unsupported by the read-only
  opener and therefore remain blocking;
- filters ready and claim candidates, augments blocked output, and appends
  synthetic external leaves to dependency trees;
- rejects `parent-child` external edges: hierarchy has no foreign lifecycle
  semantics, while scheduling edges have an explicit capability predicate;
- applies the same guard to checked close and batch blocked-state reads.
- guards explicit lifecycle claims (including `bd update --claim`) before any
  accompanying edits, and carries external blockers into `bd close` batches.
  Eligible items still close together in one local transaction; refused items
  retain their positions in the results. `--force` waives close policy, but
  never makes blocked work eligible for `--claim-next`.
- leaves non-blocking external relationships visible without allowing them to
  gate readiness.

This is query-time state. There is deliberately no schema migration and no
attempt to persist foreign status in the local Dolt database.

First-party stores expose a narrow indexed query for explicit external
blocking rows. The decorator resolves those rows and adds unsatisfied source
IDs to `WorkFilter.ExcludeIDs`; the shared ready SQL applies those exclusions
before ordering, pagination, and claim selection.

## Consistency boundary

Resolution cannot be atomic across two independent project databases. The
decorator resolves a foreign snapshot, then passes the exclusions into the
existing atomic local `ClaimReadyIssue` operation. A concurrent foreign change
may therefore take effect on the next query, matching the historical SQLite
behavior without weakening local ready-selection or claim safety.

## Cross-prefix issue targets (GH#4647)

A bare cross-prefix target (`bd dep add gt-1 rig-2`) is stored in
`depends_on_external` like an explicit reference, so the local `is_blocked`
recompute never sees it. No `external_projects` entry names it: it is routed,
not configured. `GetExternalBlockingDependencyRecords` therefore returns those
edges alongside the explicit ones, and `externaldeps.New` takes an optional
`externaldeps.WithIssueRouter(IssueRouter)`. An `IssueRouter` maps an issue ID
to an `IssueRoute{Key, Open}`: `Key` names the destination store, so IDs routed
to one store are read through one open of it, and `Open` opens that store
read-only. The router, or the route's `Open`, returns an error wrapping
`ErrNoIssueRoute` when no route is configured or the route leads to a store the
process cannot read issues from; any other error is a configured route that
failed.

`cmd/bd` supplies `crossPrefixBlockerRouting` to the direct CLI chain and to
routed policy stores. It follows the read-only sequence a routed read uses
(`lookupPrefixRoute`, `admitForeignWorkspace`, `loadDoltDatabase`,
`openRoutedTargetStore(..., false)`), and reports a missing route and a
proxied-server destination as `ErrNoIssueRoute`.

Only `blocks` and `conditional-blocks` edges are classified; a bare
cross-prefix `waits-for`, `relates-to` or other edge never gates its source.
Resolution reads the inner store first, by exact ID in one batch, because one
database can hold several prefixes; a failed local read is an error. Each ID it
lacks is routed, grouped by `IssueRoute.Key`, and read there in one batch:

- a target found closed or pinned is satisfied; any other status blocks;
- a target the owning store does not hold does not block, as the local
  missing-target join treats a dangling row;
- an ID with no usable route (`ErrNoIssueRoute`) does not block, and prints no
  warning, for compatibility with the behavior before GH#4647;
- a configured route that fails (lookup, open or read) blocks every source that
  depends on it, with one stderr warning per prefix for the life of the policy
  instance, separate from the capability warnings:
  `Warning: issues with prefix "gt-" are unavailable through their route;
  dependencies blocked on them remain blocking`.

A policy built without a router, and one whose roles are served remotely, does
not classify bare cross-prefix edges at all: `bd serve`, the proxied-server
chain (`WrapUOWProvider` takes no router) and remote-role clients leave them to
the local store, as before. `bd serve` gets no router because
`openRoutedTargetStore` swaps the process-wide `BEADS_DOLT_SERVER_DATABASE`
while it opens, which a server answering concurrent requests must not do.

Every collector (the workspace-wide blocking state behind ready list, count
and claim, `IsBlocked` and its batch form, the blocked listing, the
checked-close guard and the claim guard) classifies edges through the one
`policyReference` function, so they cannot disagree about which edges are
cross-prefix blockers. `is_blocked` stays local: a foreign close admits the
source on the next query, with no write to the source row and no descendant
propagation.

Direct batch closes pass an immutable external-blocker snapshot through an
internal policy-aware batch accessor. Hooks and telemetry forward that policy
to the existing batch executor, which applies both per-item refusals and claim
exclusions inside its one transaction. An unsupported decorator fails closed
instead of silently discarding the policy. The proxied UOW applies the same
guards through its decorated issue use case. Neither path adds a public request
field or persists foreign status locally.

Every non-forced batch close, and every batch close with a next claim, scans all
external blocking edges and resolves their foreign capabilities before the
local transaction. This shares the lookup-cost concern tracked in GH#6156.

## Verification

Regression tests cover unsatisfied and shipped capabilities, fail-closed
resolution (including foreign read failure), non-blocking edges, paginated
ready work, claim selection, blocked output, dependency-tree synthesis,
checked-close and batch guards, and direct/proxied decorator wiring. Existing
storage and CLI suites remain the compatibility gate.
