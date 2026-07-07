---
id: heartbeat
title: bd heartbeat
slug: /cli-reference/heartbeat
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc heartbeat`

## bd heartbeat

Refresh the lease on an issue you currently hold in_progress.

A claim carries a lease that expires after a TTL. A worker keeps its claim alive
by heartbeating faster than the TTL; once it stops (because it died), the lease
goes stale and 'bd reclaim' reverts the issue to ready so another worker can pick
it up. Heartbeat pushes lease_expires_at forward and stamps heartbeat_at = now.

Only the current owner may heartbeat. If the lease has already been reclaimed or
the issue closed, heartbeat fails so the worker learns to stop.

Heartbeat writes a Dolt commit, so heartbeat well below the TTL but not so fast
it bloats history — cadence should be a small fraction of the TTL, not per-op.

Examples:
  bd heartbeat bd-123
  bd hb bd-123

```
bd heartbeat <id> [flags]
```

**Aliases:** hb
