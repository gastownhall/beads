---
name: beads
description: >
  Git-backed issue tracker for multi-session work with dependencies and persistent
  memory across conversation compaction. Use when work spans sessions, has blockers,
  or needs context recovery after compaction.
allowed-tools: "Read,Bash(bd:*)"
version: "0.47.1"
author: "Steve Yegge <https://github.com/steveyegge>"
license: "MIT"
---

# Beads - Persistent Task Memory for AI Agents

Graph-based issue tracker that survives conversation compaction. Provides persistent memory for multi-session work with complex dependencies.

## bd vs TodoWrite

| bd (persistent) | TodoWrite (ephemeral) |
|-----------------|----------------------|
| Multi-session work | Single-session tasks |
| Complex dependencies | Linear execution |
| Survives compaction | Conversation-scoped |
| Git-backed, team sync | Local to session |

**Decision test**: "Will I need this context in 2 weeks?" → YES = bd

**When to use bd**:
- Work spans multiple sessions or days
- Tasks have dependencies or blockers
- Need to survive conversation compaction
- Exploratory/research work with fuzzy boundaries
- Collaboration with team (git sync)

**When to use TodoWrite**:
- Single-session linear tasks
- Simple checklist for immediate work
- All context is in current conversation
- Will complete within current session

## Prerequisites

```bash
bd --version  # Requires v0.47.0+
```

- **bd CLI** installed and in PATH
- **Git repository** (bd requires git for sync)
- **Initialization**: `bd init` run once (humans do this, not agents)

## CLI Reference

**Run `bd prime`** for AI-optimized workflow context (auto-loaded by hooks).
**Run `bd <command> --help`** for specific command usage.

Essential commands: `bd ready`, `bd create`, `bd show`, `bd update`, `bd close`, `bd sync`

## Session Protocol

1. `bd ready` — Find unblocked work
2. `bd show <id>` — Get full context
3. `bd update <id> --status in_progress` — Start work
4. Add notes as you work (critical for compaction survival)
5. `bd close <id> --reason "..."` — Complete task
6. `bd sync` — Persist to git (always run at session end)

## Advanced Features

| Feature | CLI | Resource |
|---------|-----|----------|
| Molecules (templates) | `bd mol --help` | [MOLECULES.md](resources/MOLECULES.md) |
| Chemistry (pour/wisp) | `bd pour`, `bd wisp` | [CHEMISTRY_PATTERNS.md](resources/CHEMISTRY_PATTERNS.md) |
| Agent beads | `bd agent --help` | [AGENTS.md](resources/AGENTS.md) |
| Async gates | `bd gate --help` | [ASYNC_GATES.md](resources/ASYNC_GATES.md) |
| Worktrees | `bd worktree --help` | [WORKTREES.md](resources/WORKTREES.md) |

## Resources

| Resource | Content |
|----------|---------|
| [BOUNDARIES.md](resources/BOUNDARIES.md) | bd vs TodoWrite detailed comparison |
| [CLI_REFERENCE.md](resources/CLI_REFERENCE.md) | Complete command syntax |
| [DEPENDENCIES.md](resources/DEPENDENCIES.md) | Dependency system deep dive |
| [INTEGRATION_PATTERNS.md](resources/INTEGRATION_PATTERNS.md) | TodoWrite and tool integration |
| [ISSUE_CREATION.md](resources/ISSUE_CREATION.md) | When and how to create issues |
| [MOLECULES.md](resources/MOLECULES.md) | Proto definitions, component labels |
| [PATTERNS.md](resources/PATTERNS.md) | Common usage patterns |
| [RESUMABILITY.md](resources/RESUMABILITY.md) | Compaction survival guide |
| [STATIC_DATA.md](resources/STATIC_DATA.md) | Database schema reference |
| [TROUBLESHOOTING.md](resources/TROUBLESHOOTING.md) | Error handling and fixes |
| [WORKFLOWS.md](resources/WORKFLOWS.md) | Step-by-step workflow patterns |
| [AGENTS.md](resources/AGENTS.md) | Agent bead tracking (v0.40+) |
| [ASYNC_GATES.md](resources/ASYNC_GATES.md) | Human-in-the-loop gates |
| [CHEMISTRY_PATTERNS.md](resources/CHEMISTRY_PATTERNS.md) | Mol vs Wisp decision tree |
| [WORKTREES.md](resources/WORKTREES.md) | Parallel development patterns |

## Full Documentation

- **bd prime**: AI-optimized workflow context
- **GitHub**: [github.com/steveyegge/beads](https://github.com/steveyegge/beads)
