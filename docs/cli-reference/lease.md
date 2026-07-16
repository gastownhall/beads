---
title: "bd lease"
description: "Manage automatic claim leases"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc lease`.

Manage the store's automatic claim-lease behavior.

By default every claim stamps a lease (`lease.auto` on): a worker that
stops heartbeating loses its claim to 'bd reclaim'. Deployments whose
recovery authority lives elsewhere — an orchestrator with its own liveness
evidence — disarm automatic stamping so an un-renewed fleet is never one
stray reclaim away from mass-reverting live work. Explicit leases requested
AFTER disarming remain available and reclaimable; leases existing at disarm
time are cleared by the sweep regardless of how they were requested.

```
bd lease [command]
```

## bd lease disarm

Set lease.auto=off and NULL the lease columns on existing in_progress
rows — the flip and the first sweep share one transaction, and bounded
follow-up sweeps catch claims that were in flight during the flip. Nothing
is released:
status and assignee are untouched, and the ownership fence does not move.

After disarming, claims carry no lease unless one is explicitly requested,
heartbeats on unleased claims are rejected (exit non-zero, they never arm a
lease as a side effect), and 'bd reclaim' only ever touches explicitly
requested leases.

Re-arm with: bd config set lease.auto on
(existing claims stay unleased until re-claimed or explicitly leased).

```
bd lease disarm [flags]
```
