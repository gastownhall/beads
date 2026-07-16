---
title: "bd reclaim"
description: "Revert stale-lease in_progress issues back to ready (dead-worker recovery)"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc reclaim`.

Revert in_progress issues whose lease has gone stale back to ready.

When a worker claims an issue it takes a lease that expires after a TTL, kept
alive by 'bd heartbeat'. A worker that dies stops heartbeating, so its lease
expires and its issue would otherwise stay in_progress forever. reclaim is the
reaper: it finds in_progress issues whose lease expired more than --older-than
ago, clears the assignee, and sets them back to open so another worker can
claim them. The previous owner's stale lease is recorded as a recovery event.

--older-than is a grace window past lease expiry: only leases that expired at
least this long ago are reclaimed, so a worker briefly paused (GC, clock skew)
is not robbed of live work. Run it from a supervisor on a timer with a window
of roughly 2× the claim TTL.

On stores with lease.auto=off (see 'bd lease disarm'), claims carry no
lease unless explicitly requested, so reclaim only ever touches explicitly
requested leases there.

UPGRADE NOTE: earlier binaries stamped a lease on every claim (both tiers)
but only ever reclaimed the issues table. Reclaim now also sweeps the wisps
table, so on an auto-on store upgrading across this change the first run can
reclaim in_progress wisp rows whose leases went stale under the old binary.
Run 'bd lease disarm' first (it clears armed leases on both tiers) if that
recovery is not wanted, or let live workers heartbeat within the grace
window to refresh their leases.

Examples:
  bd reclaim                       # default grace window (2× the lease TTL)
  bd reclaim --older-than 10m      # reclaim leases expired &gt;10m ago
  bd reclaim --older-than 0s       # reclaim every currently-expired lease

```
bd reclaim [flags]
```

**Flags:**

```
      --older-than duration   Only reclaim leases that expired at least this long ago (grace window) (default 10m0s)
```
