---
id: gate
title: bd gate
slug: /cli-reference/gate
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc gate`

## bd gate

Gates are async wait conditions that block workflow steps.

Gates are created automatically when a formula step has a gate field.
They must be closed (manually or via watchers) for the blocked step to proceed.

Gate types:
  human   - Requires manual bd close (Phase 1)
  timer   - Expires after timeout (Phase 2)
  gh:run  - Waits for GitHub workflow (Phase 3)
  gh:pr   - Waits for PR merge (Phase 3)
  bead    - Waits for cross-rig bead to close (Phase 4)

For bead gates, await_id format is &lt;rig&gt;:&lt;bead-id&gt; (e.g., "other-project:op-abc123").

Examples:
  bd gate list           # Show all open gates
  bd gate list --all     # Show all gates including closed
  bd gate check          # Evaluate all open gates
  bd gate check --type=bead  # Evaluate only bead gates
  bd gate resolve &lt;id&gt;   # Close a gate manually

```
bd gate
```

### bd gate add-waiter

Register an agent as a waiter on a gate bead.

When the gate closes, the waiter will receive a wake notification via 'bd gate wake'.
The waiter is typically the worker's address (e.g., "my-project/workers/agent-1").

This is used by 'bd done --phase-complete' to register for gate wake notifications.

```
bd gate add-waiter <gate-id> <waiter>
```

### bd gate check

Evaluate gate conditions and automatically close resolved gates.

By default, checks all open gates. Use --type to filter by gate type.

Gate types:
  gh       - Check all GitHub gates (gh:run and gh:pr)
  gh:run   - Check GitHub Actions workflow runs
  gh:pr    - Check pull request merge status
  timer    - Check timer gates (auto-expire based on timeout)
  bead     - Check cross-rig bead gates
  all      - Check all gate types

GitHub gates use the 'gh' CLI to query status:
  - gh:run checks 'gh run view &lt;id&gt; --json status,conclusion'
  - gh:pr checks 'gh pr view &lt;id&gt; --json state,title'

A gate is resolved when:
  - gh:run: status=completed AND conclusion=success
  - gh:pr: state=MERGED
  - timer: current time &gt; created_at + timeout
  - bead: target bead status=closed

A gate is escalated when:
  - gh:run: status=completed AND conclusion in (failure, canceled)
  - gh:pr: state=CLOSED

Examples:
  bd gate check              # Check all gates
  bd gate check --type=gh    # Check only GitHub gates
  bd gate check --type=gh:run # Check only workflow run gates
  bd gate check --type=timer # Check only timer gates
  bd gate check --type=bead  # Check only cross-rig bead gates
  bd gate check --dry-run    # Show what would happen without changes
  bd gate check --escalate   # Escalate expired/failed gates

```
bd gate check [flags]
```

**Flags:**

```
      --dry-run       Show what would happen without making changes
  -e, --escalate      Escalate failed/expired gates
  -l, --limit int     Limit results (default 100) (default 100)
  -t, --type string   Gate type to check (gh, gh:run, gh:pr, timer, bead, all)
```

### bd gate create

Create an ad-hoc gate issue that blocks another issue until resolved.

The blocked issue will not appear in 'bd ready' until the gate is resolved
via 'bd gate resolve'.

Gate types:
  human   - Requires manual 'bd gate resolve' (default)
  timer   - Auto-resolves after --timeout duration
  gh:run  - Waits for GitHub Actions workflow
  gh:pr   - Waits for PR merge

Examples:
  bd gate create --blocks bd-abc
  bd gate create --type=human --blocks bd-abc --reason="Need design review"
  bd gate create --type=timer --blocks bd-abc --timeout=2h
  bd gate create --type=gh:pr --blocks bd-abc --await-id=42

```
bd gate create [flags]
```

**Flags:**

```
      --await-id string   Condition identifier (run ID, PR number, etc.)
      --blocks string     Issue ID to block (required)
  -r, --reason string     Reason for the gate
      --timeout string    Timeout duration (e.g., 2h, 30m)
  -t, --type string       Gate type (human, timer, gh:run, gh:pr) (default "human")
```

### bd gate discover

Discovers GitHub workflow run IDs for gates awaiting CI/CD completion.

This command finds open gates with await_type="gh:run" that don't have an await_id,
queries recent GitHub workflow runs, and matches them using heuristics:
  - Branch name matching
  - Commit SHA matching
  - Time proximity (runs within 5 minutes of gate creation)

Once matched, the gate's await_id is updated with the GitHub run ID, enabling
subsequent polling to check the run's status.

Examples:
  bd gate discover           # Auto-discover run IDs for all matching gates
  bd gate discover --dry-run # Preview what would be matched (no updates)
  bd gate discover --branch main --limit 10  # Only match runs on 'main' branch

```
bd gate discover [flags]
```

**Flags:**

```
  -b, --branch string      Filter runs by branch (default: current branch)
  -n, --dry-run            Preview mode: show matches without updating
  -l, --limit int          Max runs to query from GitHub (default 10)
  -a, --max-age duration   Max age for gate/run matching (default 30m0s)
```

### bd gate list

List all gate issues in the current beads database.

By default, shows only open gates. Use --all to include closed gates.

```
bd gate list [flags]
```

**Flags:**

```
  -a, --all         Show all gates including closed
  -n, --limit int   Limit results (default 50) (default 50)
```

### bd gate resolve

Close a gate issue to unblock the step waiting on it.

This is equivalent to 'bd close &lt;gate-id&gt;' but with a more explicit name.
Use --reason to provide context for why the gate was resolved.

```
bd gate resolve <gate-id> [flags]
```

**Flags:**

```
  -r, --reason string   Reason for resolving the gate
```

### bd gate show

Display details of a gate issue including its waiters.

This is similar to 'bd show' but validates that the issue is a gate.

```
bd gate show <gate-id>
```
