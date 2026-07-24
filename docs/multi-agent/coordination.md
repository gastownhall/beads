---
title: Agent Coordination
description: Assign and claim beads, hand off work, and serialize conflict-prone work with merge slots across multiple agents
---

Patterns for coordinating work between multiple AI agents.

## Work Assignment

### Assigning and Claiming Work

Assign work to a specific agent, or claim it atomically for yourself:

```bash
# Assign issue to an agent
bd assign bd-42 agent-1

# Atomically claim an issue (sets assignee to you, status to in_progress)
bd update bd-42 --claim

# Claim the first ready issue matching your filters
bd ready --claim --json

# Release a claimed issue
bd assign bd-42 ""              # clear the assignee
bd update bd-42 --status open   # make it claimable again
```

### Checking Assigned Work

```bash
# What is agent-1 working on?
bd list --assignee agent-1 --status in_progress

# What is ready for agent-1?
bd ready --assignee agent-1

# JSON output
bd list --assignee agent-1 --json
```

## Handoff Patterns

### Sequential Handoff

Agent A completes work, hands off to Agent B:

```bash
# Agent A
bd comment bd-42 "API complete, ready for review"
bd assign bd-42 agent-b

# Agent B picks up
bd list --assignee agent-b  # Sees bd-42
bd update bd-42 --claim
```

### Parallel Work

Multiple agents work on different issues:

```bash
# Coordinator
bd assign bd-42 agent-a
bd assign bd-43 agent-b
bd assign bd-44 agent-c

# Each agent claims its issue and works independently
bd update bd-42 --claim

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

bd assign bd-epic.1 agent-a
bd assign bd-epic.2 agent-b
bd assign bd-epic.3 agent-c

# Fan-in: wait for all parts (one dependency per call)
bd dep add bd-merge bd-epic.1
bd dep add bd-merge bd-epic.2
bd dep add bd-merge bd-epic.3
```

<Tip>
For structured epic fan-out, `bd swarm` creates and tracks a swarm molecule
from an epic (`bd swarm create`, `bd swarm status`).
</Tip>

## Agent Discovery

Beads has no agent registry — assignees are plain strings. To see which
agents are active, group in-progress work by assignee:

```bash
bd list --status in_progress --json
```

## Conflict Prevention

### Atomic Claims

`--claim` is atomic: when multiple agents pull from the same ready queue,
the first claim wins, and repeating a claim you already hold is idempotent.
Prefer claiming over assigning when agents self-select work:

```bash
bd ready --claim --json
```

### Merge Slots

Serialize conflict-prone work (such as merge-queue conflict resolution) with
a merge slot — an exclusive-access primitive only one agent can hold at a
time. Each project has one merge slot bead, named from the issue prefix
(e.g. `bd-merge-slot`):

```bash
# Create the merge slot for this project
bd merge-slot create

# Check availability
bd merge-slot check

# Acquire before starting; release when done
bd merge-slot acquire
bd merge-slot release
```

## Communication Patterns

### Via Comments

```bash
# Agent A leaves note
bd comment bd-42 "Completed API, needs frontend integration"

# Agent B reads
bd comments bd-42
```

### Via Labels

```bash
# Mark for review
bd update bd-42 --add-label "needs-review"

# Agent B filters
bd list --label-any needs-review
```

## Coordinating Across Repositories

Agents can coordinate work that spans repositories:

```bash
# Depend on a capability delivered by another project
bd dep add bd-42 external:backend:api-ready
```

Multi-repo routing, aggregated views, and contributor/team workflows are
covered in [Routing](/multi-agent/routing) and
[Multi-Repo Migration](/multi-agent/multi-repo-migration).

## Best Practices

1. **Clear ownership** - Assign or claim work so every issue has one owner
2. **Document handoffs** - Use comments to explain context
3. **Use labels for status** - `needs-review`, `blocked`, `ready`
4. **Avoid conflicts** - Claim atomically; use merge slots to serialize
   conflict-prone work
5. **Monitor progress** - Regular status checks
6. **Sync at session end** - Run `bd dolt push` so other agents see your
   updates
