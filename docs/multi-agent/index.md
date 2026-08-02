---
title: Multi-Agent
description: Coordinate beads across multiple agents and repositories with routing, cross-repo dependencies, and work handoff
---

Beads supports coordination between multiple AI agents and repositories.

## Overview

Multi-agent features enable:
- **Routing** - Automatic issue routing to correct repositories
- **Cross-repo dependencies** - Dependencies across repository boundaries
- **Agent coordination** - Work assignment and handoff between agents

## Key Concepts

### Routing

Routing decides which repository a new bead lands in, based on your role
(contributor vs maintainer) and the `routing.*` config keys — an explicit
`--repo` flag always wins. See [Multi-Repo Routing](/multi-agent/routing)
for the decision flow and configuration reference.

### Work Assignment

Assign or atomically claim work:

```bash
bd assign bd-42 agent-1        # shorthand for bd update bd-42 --assignee agent-1
bd update bd-42 --claim        # atomically set assignee + in_progress
bd ready --claim --json        # claim the first ready match
```

### Cross-repo Dependencies

Track dependencies across repositories:

```bash
bd dep add bd-42 external:other-repo:api-ready
```

## Architecture

```
┌─────────────────┐
│   Main Repo     │
│   (coordinator) │
└────────┬────────┘
         │ routes
    ┌────┴────┐
    │         │
┌───▼───┐ ┌───▼───┐
│Frontend│ │Backend│
│ Repo   │ │ Repo  │
└────────┘ └────────┘
```

## Getting Started

1. **Single repo**: Standard beads workflow
2. **Multi-repo**: Configure routes and cross-repo deps
3. **Multi-agent**: Add work assignment and handoff

## Pages in this section

- [Routing](/multi-agent/routing) — automatic issue routing across
  repositories and `BEADS_DIR` resolution.
- [Coordination](/multi-agent/coordination) — work assignment and handoff
  patterns between agents.
- [Federation](/multi-agent/federation) — peer-to-peer sharing of beads
  across repos and organizations.
- [Multi-Repo Migration](/multi-agent/multi-repo-migration) — moving an
  existing single-repo setup to multi-repo routing.
