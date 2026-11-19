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

- parses `external:<project>:<capability>` references, and — since GH#4647 —
  bare cross-prefix issue ids too: an id whose prefix names another repository
  shares `depends_on_external` storage with explicit refs, but is a real issue
  somewhere, so the same policy classifies it by edge type (`blocks` and
  `conditional-blocks` only, mirroring `types.IsGateEdge`; a bare
  cross-prefix `waits-for` or `relates-to` edge never gates) and resolves it
  through an injected `IssueRouter` (see below) rather than a project
  configuration;
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

A bare cross-prefix target is routed, not configured: no `external_projects`
entry names it, so the decorator takes an optional `IssueRouter`
(`externaldeps.WithIssueRouter`) that resolves ids to a verdict. `cmd/bd`
injects its direct-CLI router — the same read-only sequence a routed command's
read uses (`lookupPrefixRoute` → `admitForeignWorkspace` → `loadDoltDatabase`
→ `openRoutedTargetStore(false)`), grouping ids by matched destination and
opening each destination once per evaluation. `bd serve` and the proxied
unit-of-work provider deliberately get NO router: `openRoutedTargetStore`
swaps a process-global environment variable, unsafe under concurrent
requests.

Resolution order: the inner store answers by exact id first (a multi-prefix
database holds rows for prefixes that are not its own, no route needed); a
read failure there is an error. Whatever remains goes to the router once.
Verdicts:

- `Found`: satisfied iff the target is closed or pinned; any other status
  keeps the blocker standing.
- `Absent` (the owning store answered, holds no such row): satisfied, the
  same way the local missing-target join treats a dangling row.
- `Unrouted` (no route reaches the id, or a proxied-server workspace a direct
  store cannot open): satisfied, for compatibility with the pre-fix behavior.
- `Unavailable` (a CONFIGURED route could not answer): fail closed — every
  affected source stays excluded, with one stderr warning per prefix per
  policy instance, kept separate from the capability warnings.

A policy instance with no router treats every id it cannot resolve locally as
unrouted (satisfied): such an instance cannot observe a configured route's
failure, and compatibility demands it not strand work on an unverifiable edge.

The classification is one shared function
(`externaldeps.policyBlockersFor`), so the workspace-wide blocking state
(ready list/count/claim), `IsBlocked`, its batch twins, the blocked listing,
the checked-close guard and the claim guard cannot drift about what a bare
cross-prefix target is or when one is satisfied. `is_blocked` stays local: a
foreign close admits the source on the next query, with no write to the source
row and no descendant propagation.

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
