# Multi-Phase Development Workflow Example

This example demonstrates how to use beads for large projects with multiple development phases (planning, MVP, iteration, polish).

## Problem

When building complex features, you want to:
- **Phase 1:** Research and planning
- **Phase 2:** Build MVP quickly
- **Phase 3:** Iterate based on feedback
- **Phase 4:** Polish and production-ready
- Track discovered work at each phase
- Keep priorities clear across phases

## Solution

Use beads epics and hierarchical issues to organize work by phase, with priority-based focus.

## Setup

```bash
# Initialize beads in your project
cd my-project
bd init

# Start daemon for auto-sync (optional)
bd daemon --start --auto-commit --auto-push
```

## Phase 1: Research & Planning

Create the epic and initial planning issues:

```bash
# Create the main epic
bd create "Build real-time collaboration system" -t epic -p 1
# Returns: bd-a1b2c3

# Plan the phases (hierarchical children)
bd create "Phase 1: Research WebSocket libraries" -p 1
# Auto-assigned: bd-a1b2c3.1

bd create "Phase 2: Build MVP (basic sync)" -p 1
# Auto-assigned: bd-a1b2c3.2

bd create "Phase 3: Add conflict resolution" -p 2
# Auto-assigned: bd-a1b2c3.3

bd create "Phase 4: Production hardening" -p 3
# Auto-assigned: bd-a1b2c3.4

# Add blocking dependencies (phases must happen in order)
bd dep add bd-a1b2c3.2 bd-a1b2c3.1 --type blocks
bd dep add bd-a1b2c3.3 bd-a1b2c3.2 --type blocks
bd dep add bd-a1b2c3.4 bd-a1b2c3.3 --type blocks
```

### Research Phase Tasks

```bash
# Add research tasks for Phase 1
bd create "Evaluate Socket.IO vs native WebSockets" -p 1 \
  --deps discovered-from:bd-a1b2c3.1

bd create "Research operational transform vs CRDT" -p 1 \
  --deps discovered-from:bd-a1b2c3.1

bd create "Document technical decisions" -p 2 \
  --deps discovered-from:bd-a1b2c3.1

# See what's ready to work on
bd ready
# Shows only Phase 1 tasks (nothing blocks them)
```

## Phase 2: Build MVP

After completing Phase 1 research:

```bash
# Close Phase 1
bd close bd-a1b2c3.1 --reason "Research complete, chose Socket.IO + CRDT"

# Phase 2 is now unblocked
bd ready
# Shows Phase 2 and its tasks

# Break down MVP work
bd create "Set up Socket.IO server" -p 1 \
  --deps discovered-from:bd-a1b2c3.2

bd create "Implement basic CRDT for text" -p 1 \
  --deps discovered-from:bd-a1b2c3.2

bd create "Build simple UI for testing" -p 2 \
  --deps discovered-from:bd-a1b2c3.2

# Start implementing
bd update bd-xyz --status in_progress
```

### Discovered Work During MVP

You'll discover issues during implementation:

```bash
# Found a bug while implementing
bd create "Socket.IO disconnects on network change" -t bug -p 1 \
  --deps discovered-from:bd-xyz

# Found missing feature
bd create "Need reconnection logic" -p 1 \
  --deps discovered-from:bd-xyz

# Technical debt to address later
bd create "Refactor CRDT code for performance" -p 3 \
  --deps discovered-from:bd-xyz
```

## Phase 3: Iteration

After MVP is working:

```bash
# Close Phase 2
bd close bd-a1b2c3.2 --reason "MVP working, tested with 2 users"

# Phase 3 is now unblocked
bd ready

# Add iteration tasks
bd create "Handle concurrent edits properly" -p 1 \
  --deps discovered-from:bd-a1b2c3.3

bd create "Add conflict indicators in UI" -p 2 \
  --deps discovered-from:bd-a1b2c3.3

bd create "Test with 10+ concurrent users" -p 1 \
  --deps discovered-from:bd-a1b2c3.3
```

### Feedback-Driven Discovery

```bash
# User testing reveals issues
bd create "Cursor positions get out of sync" -t bug -p 0 \
  --deps discovered-from:bd-a1b2c3.3

bd create "Large documents cause lag" -t bug -p 1 \
  --deps discovered-from:bd-a1b2c3.3

# Feature requests
bd create "Add presence awareness (who's online)" -p 2 \
  --deps discovered-from:bd-a1b2c3.3
```

## Phase 4: Production Hardening

Final polish before production:

```bash
# Close Phase 3
bd close bd-a1b2c3.3 --reason "Conflict resolution working well"

# Phase 4 is now unblocked
bd ready

# Add hardening tasks
bd create "Add error monitoring (Sentry)" -p 1 \
  --deps discovered-from:bd-a1b2c3.4

bd create "Load test with 100 users" -p 1 \
  --deps discovered-from:bd-a1b2c3.4

bd create "Security audit: XSS, injection" -p 0 \
  --deps discovered-from:bd-a1b2c3.4

bd create "Write deployment runbook" -p 2 \
  --deps discovered-from:bd-a1b2c3.4

bd create "Add metrics and dashboards" -p 2 \
  --deps discovered-from:bd-a1b2c3.4
```

## Viewing Progress

### See All Phases

```bash
# View the entire dependency tree
bd dep tree bd-a1b2c3

# Example output:
# bd-a1b2c3 (epic) - Build real-time collaboration system
# ├─ bd-a1b2c3.1 [CLOSED] - Phase 1: Research
# │  ├─ bd-abc [CLOSED] - Evaluate Socket.IO
# │  ├─ bd-def [CLOSED] - Research CRDT
# │  └─ bd-ghi [CLOSED] - Document decisions
# ├─ bd-a1b2c3.2 [CLOSED] - Phase 2: MVP
# │  ├─ bd-jkl [CLOSED] - Socket.IO server
# │  ├─ bd-mno [CLOSED] - Basic CRDT
# │  └─ bd-pqr [IN_PROGRESS] - Testing UI
# ├─ bd-a1b2c3.3 [OPEN] - Phase 3: Iteration
# │  └─ (blocked by bd-a1b2c3.2)
# └─ bd-a1b2c3.4 [OPEN] - Phase 4: Hardening
#    └─ (blocked by bd-a1b2c3.3)
```

### Current Phase Status

```bash
# See only open issues
bd list --status open

# See current phase's ready work
bd ready

# See high-priority issues across all phases
bd list --priority 0 --status open
bd list --priority 1 --status open
```

### Progress Metrics

```bash
# Overall stats
bd stats

# Issues by phase
bd list | grep "Phase 1"
bd list | grep "Phase 2"
```

## Priority Management Across Phases

### Dynamic Priority Adjustment

As you learn more, priorities change:

```bash
# Started as P2, but user feedback made it critical
bd update bd-xyz --priority 0

# Started as P1, but can wait until later phase
bd update bd-abc --priority 3
```

### Focus on Current Phase

```bash
# See only P0-P1 issues (urgent work)
bd ready | grep -E "P0|P1"

# See backlog for future phases (P3-P4)
bd list --priority 3 --status open
bd list --priority 4 --status open
```

## Example: Full Workflow

```bash
# Day 1: Planning
bd create "Build auth system" -t epic -p 1  # bd-a1b2
bd create "Phase 1: Research OAuth providers" -p 1  # bd-a1b2.1
bd create "Phase 2: Implement OAuth flow" -p 1  # bd-a1b2.2
bd create "Phase 3: Add session management" -p 2  # bd-a1b2.3
bd create "Phase 4: Security audit" -p 1  # bd-a1b2.4
bd dep add bd-a1b2.2 bd-a1b2.1 --type blocks
bd dep add bd-a1b2.3 bd-a1b2.2 --type blocks
bd dep add bd-a1b2.4 bd-a1b2.3 --type blocks

# Week 1: Phase 1 (Research)
bd ready  # Shows Phase 1 tasks
bd create "Compare Auth0 vs Firebase" -p 1 --deps discovered-from:bd-a1b2.1
bd update bd-xyz --status in_progress
# ... research complete ...
bd close bd-a1b2.1 --reason "Chose Auth0"

# Week 2-3: Phase 2 (Implementation)
bd ready  # Now shows Phase 2 tasks
bd create "Set up Auth0 tenant" -p 1 --deps discovered-from:bd-a1b2.2
bd create "Implement login callback" -p 1 --deps discovered-from:bd-a1b2.2
bd create "Handle token refresh" -p 1 --deps discovered-from:bd-a1b2.2
# ... discovered bugs ...
bd create "Callback fails on Safari" -t bug -p 0 --deps discovered-from:bd-abc
bd close bd-a1b2.2 --reason "OAuth flow working"

# Week 4: Phase 3 (Sessions)
bd ready  # Shows Phase 3 tasks
bd create "Implement Redis session store" -p 1 --deps discovered-from:bd-a1b2.3
bd create "Add session timeout handling" -p 2 --deps discovered-from:bd-a1b2.3
bd close bd-a1b2.3 --reason "Sessions working"

# Week 5: Phase 4 (Security)
bd ready  # Shows Phase 4 tasks
bd create "Review OWASP top 10" -p 1 --deps discovered-from:bd-a1b2.4
bd create "Add CSRF protection" -p 0 --deps discovered-from:bd-a1b2.4
bd create "Pen test with security team" -p 1 --deps discovered-from:bd-a1b2.4
bd close bd-a1b2.4 --reason "Security audit passed"

# Epic complete!
bd close bd-a1b2 --reason "Auth system in production"
```

## Best Practices

### 1. Keep Phases Focused

Each phase should have clear exit criteria:

```bash
# Good: Specific, measurable
bd create "Phase 1: Research (exit: chosen solution + ADR doc)" -p 1

# Bad: Vague
bd create "Phase 1: Look at stuff" -p 1
```

### 2. Use Priorities Within Phases

Not everything in a phase is equally urgent:

```bash
# Critical path
bd create "Implement core sync algorithm" -p 0 --deps discovered-from:bd-a1b2.2

# Nice to have, can wait
bd create "Add dark mode to test UI" -p 3 --deps discovered-from:bd-a1b2.2
```

### 3. Link Discovered Work

Always link to parent issue/phase:

```bash
# Maintains context
bd create "Bug found during testing" -t bug -p 1 \
  --deps discovered-from:bd-a1b2.3

# Can trace back to which phase/feature it came from
bd dep tree bd-a1b2
```

### 4. Don't Block on Low-Priority Work

If a phase has P3-P4 issues, don't let them block the next phase:

```bash
# Move nice-to-haves to backlog, unblock Phase 2
bd update bd-xyz --priority 4
bd close bd-a1b2.1 --reason "Core research done, polish can wait"
```

### 5. Regular Review

Check progress weekly:

```bash
# What's done?
bd list --status closed --limit 20

# What's stuck?
bd list --status blocked

# What's ready?
bd ready
```

## Common Patterns

### MVP → Iteration Loop

```bash
# MVP phase
bd create "Phase 2: MVP (basic features)" -p 1
bd create "Phase 3: Iteration (feedback loop)" -p 2
bd dep add bd-phase3 bd-phase2 --type blocks

# After MVP, discover improvements
bd create "Add feature X (user requested)" -p 1 \
  --deps discovered-from:bd-phase3
bd create "Fix UX issue Y" -p 2 \
  --deps discovered-from:bd-phase3
```

### Parallel Workstreams

Not all phases must be sequential:

```bash
# Frontend and backend can happen in parallel
bd create "Frontend: Build UI mockups" -p 1
bd create "Backend: API design" -p 1

# No blocking dependency between them
# Both show up in 'bd ready'
```

### Rollback Planning

Plan for failure:

```bash
# Phase 3: Launch
bd create "Phase 3: Deploy to production" -p 1

# Contingency plan (related, not blocking)
bd create "Rollback plan if deploy fails" -p 1
bd dep add bd-rollback bd-phase3 --type related
```

## See Also

- [Team Workflow](../team-workflow/) - Collaborate across phases
- [Contributor Workflow](../contributor-workflow/) - External contributions
- [Multiple Personas Example](../multiple-personas/) - Architect/implementer split
