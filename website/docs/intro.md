---
id: intro
title: Introduction
sidebar_position: 1
slug: /
---

# Beads Documentation

**Beads** (`bd`) is a git-backed issue tracker designed for AI-supervised coding workflows.

## Why Beads?

Traditional issue trackers (Jira, GitHub Issues) weren't designed for AI agents. Beads was built from the ground up for:

- **AI-native workflows** - Hash-based IDs prevent collisions when multiple agents work concurrently
- **Git-backed storage** - Issues sync via JSONL files, enabling collaboration across branches
- **Dependency-aware execution** - `bd ready` shows only unblocked work
- **Formula system** - Declarative templates for repeatable workflows
- **Multi-agent coordination** - Routing, gates, and molecules for complex workflows

## Quick Start

```bash
# Install via Homebrew (macOS/Linux)
brew install beads

# Or quick install (macOS/Linux/FreeBSD)
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

# Initialize in your project
cd your-project
bd init --quiet

# Create your first issue
bd create "Set up database" -p 1 -t task

# See ready work
bd ready
```

## Core Concepts

| Concept | Description |
|---------|-------------|
| **Issues** | Work items with priorities, types, labels, and dependencies |
| **Dependencies** | `blocks`, `parent-child`, `discovered-from`, `related` |
| **Daemon** | Background process for auto-sync and performance |
| **Formulas** | Declarative workflow templates (TOML or JSON) |
| **Molecules** | Work graphs with parent-child relationships |
| **Gates** | Async coordination primitives (human, timer, GitHub) |

## For AI Agents

Beads is optimized for AI coding agents:

```bash
# Always use --json for programmatic access
bd list --json
bd show bd-42 --json

# Track discovered work during implementation
bd create "Found bug in auth" --description="Details..." \
  --deps discovered-from:bd-100 --json

# Sync at end of session
bd sync
```

See the [Claude Code integration](/integrations/claude-code) for detailed agent instructions.

## Architecture

```
SQLite DB (.beads/beads.db, gitignored)
    ↕ auto-sync (5s debounce)
JSONL (.beads/issues.jsonl, git-tracked)
    ↕ git push/pull
Remote JSONL (shared across machines)
```

The magic is automatic synchronization between a local SQLite database and git-tracked JSONL files.

## Next Steps

- [Installation](/getting-started/installation) - Get bd installed
- [Quick Start](/getting-started/quickstart) - Create your first issues
- [CLI Reference](/cli-reference) - All available commands
- [Workflows](/workflows) - Formulas, molecules, and gates
