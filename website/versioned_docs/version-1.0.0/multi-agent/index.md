---
id: index
title: Multi-Agent
sidebar_position: 1
---

# Multi-Agent Coordination

Beads supports coordination between multiple AI agents and repositories.

## Overview

Multi-agent features enable:
- **Routing** - Automatic issue routing to correct repositories
- **Cross-repo dependencies** - Dependencies across repository boundaries
- **Agent coordination** - Work assignment and handoff between agents

## Key Concepts

### Routes

Routes define which repository handles which issues:

```jsonl
{"pattern": "frontend/*", "target": "frontend-repo"}
{"pattern": "backend/*", "target": "backend-repo"}
{"pattern": "*", "target": "main-repo"}
```

### Work Assignment

Pin work to specific agents:

```bash
bd pin bd-42 --for agent-1 --start
bd hook --agent agent-1  # Show pinned work
```

### Cross-repo Dependencies

Track dependencies across repositories:

```bash
bd dep add bd-42 external:other-repo/bd-100
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

## Navigation

- [Routing](/multi-agent/routing) - Auto-routing configuration
- [Coordination](/multi-agent/coordination) - Agent coordination patterns
