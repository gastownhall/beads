---
id: coordination
title: Agent Coordination
sidebar_position: 3
---

# Agent Coordination

Patterns for coordinating work between multiple AI agents.

## Work Assignment

### Pinning Work

Assign work to a specific agent:

```bash
# Pin issue to agent
bd pin bd-42 --for agent-1

# Pin and start work
bd pin bd-42 --for agent-1 --start

# Unpin work
bd unpin bd-42
```

### Checking Pinned Work

```bash
# What's on my hook?
bd hook

# What's on agent-1's hook?
bd hook --agent agent-1

# JSON output
bd hook --json
```

## Handoff Patterns

### Sequential Handoff

Agent A completes work, hands off to Agent B:

```bash
# Agent A
bd close bd-42 --reason "Ready for review"
bd pin bd-42 --for agent-b

# Agent B picks up
bd hook  # Sees bd-42
bd update bd-42 --claim
```

### Parallel Work

Multiple agents work on different issues:

```bash
# Coordinator
bd pin bd-42 --for agent-a --start
bd pin bd-43 --for agent-b --start
bd pin bd-44 --for agent-c --start

# Each agent works independently
# Coordinator monitors progress
bd list --status in_progress --json
```

### Fan-Out / Fan-In

Split work, then merge:

```bash
# Fan-out
bd create "Part A" --parent bd-epic
bd create "Part B" --parent bd-epic
bd create "Part C" --parent bd-epic

bd pin bd-epic.1 --for agent-a
bd pin bd-epic.2 --for agent-b
bd pin bd-epic.3 --for agent-c

# Fan-in: wait for all parts
bd dep add bd-merge bd-epic.1 bd-epic.2 bd-epic.3
```

## Agent Discovery

Find available agents:

```bash
# List known agents (if using agent registry)
bd agents list

# Check agent status
bd agents status agent-1
```

## Conflict Prevention

### File Reservations

Prevent concurrent edits:

```bash
# Reserve files before editing
bd reserve auth.go --for agent-1

# Check reservations
bd reservations list

# Release when done
bd reserve --release auth.go
```

### Issue Locking

```bash
# Lock issue for exclusive work
bd lock bd-42 --for agent-1

# Unlock when done
bd unlock bd-42
```

## Communication Patterns

### Via Comments

```bash
# Agent A leaves note
bd comment add bd-42 "Completed API, needs frontend integration"

# Agent B reads
bd show bd-42 --full
```

### Via Labels

```bash
# Mark for review
bd update bd-42 --add-label "needs-review"

# Agent B filters
bd list --label-any needs-review
```

## Best Practices

1. **Clear ownership** - Always pin work to specific agent
2. **Document handoffs** - Use comments to explain context
3. **Use labels for status** - `needs-review`, `blocked`, `ready`
4. **Avoid conflicts** - Use reservations for shared files
5. **Monitor progress** - Regular status checks
