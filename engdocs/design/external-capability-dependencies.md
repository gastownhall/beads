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

Resolve explicit external references in a storage decorator above the local
database transaction.

The decorator:

- parses only `external:<project>:<capability>` references; cross-prefix issue
  IDs remain a separate concern;
- groups references by project and opens each configured foreign store once,
  read-only, per operation;
- considers a capability satisfied only when the foreign store contains a
  closed issue labeled `provides:<capability>`;
- treats malformed references, missing configuration, unavailable projects,
  and failed foreign reads as unsatisfied;
- filters ready and claim candidates, augments blocked output, and appends
  synthetic external leaves to dependency trees;
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
resolution, non-blocking edges, paginated ready work, claim selection, blocked
output, dependency-tree synthesis, and decorator wiring. Existing storage and
CLI suites remain the compatibility gate.
