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

- parses only `external:<project>:<capability>` references; cross-prefix issue
  IDs remain a separate concern;
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

## Verification

Regression tests cover unsatisfied and shipped capabilities, fail-closed
resolution (including foreign read failure), non-blocking edges, paginated
ready work, claim selection, blocked output, dependency-tree synthesis,
checked-close and batch guards, and direct/proxied decorator wiring. Existing
storage and CLI suites remain the compatibility gate.
