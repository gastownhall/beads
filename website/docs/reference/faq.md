---
id: faq
title: FAQ
sidebar_position: 5
---

# Frequently Asked Questions

## General

### Why beads instead of GitHub Issues or Jira?

Beads was designed specifically for AI-supervised coding workflows:
- **Hash-based IDs** prevent collisions with concurrent agents
- **Git-backed storage** enables branch-based workflows
- **Dependency-aware** ready queue for automated work selection
- **Formula system** for declarative workflow templates

### What does "beads" stand for?

Nothing specific - it's a metaphor for linked work items (like beads on a string).

### Is beads production-ready?

Yes, beads is used in production for AI-assisted development. The API is stable with semantic versioning.

## Architecture

### Why SQLite + JSONL instead of just one?

- **SQLite** for fast local queries and complex filtering
- **JSONL** for git-friendly versioning and sync
- Auto-sync keeps them aligned

### Why hash-based IDs instead of sequential?

Sequential IDs (`#1`, `#2`) break when:
- Multiple agents create issues simultaneously
- Different branches have independent numbering
- Forks diverge and merge

Hash-based IDs are globally unique without coordination.

### How does the Dolt server work?

Beads uses Dolt server mode for concurrent access:
- Transaction isolation for multiple agents
- SQL-based queries for performance
- Automatic retry on conflicts

In CI/CD or single-agent environments, beads uses embedded mode automatically (no server required).

## Usage

### How do I sync issues to git?

```bash
# Manual sync:
bd sync
```

### How do I handle merge conflicts?

Install the beads merge driver:
```bash
bd init  # Prompts for merge driver
```

Or manually resolve and reimport.

### Can multiple agents work on the same repo?

Yes! That's what beads was designed for:
- Hash IDs prevent collisions
- Pin work to specific agents
- Track who's working on what

### How do I use beads in CI/CD?

```bash
# Just run commands directly — beads uses embedded mode in CI
bd list
```

## Workflows

### What are formulas?

Declarative workflow templates in TOML or JSON. Pour them to create molecules (instances).

### What are gates?

Async coordination primitives:
- Human gates wait for approval
- Timer gates wait for duration
- GitHub gates wait for CI/PR events

### What's the difference between molecules and wisps?

- **Molecules** persist in `.beads/` and sync with git
- **Wisps** are ephemeral in `.beads-wisp/` and don't sync

## Integration

### Should I use CLI or MCP?

**Use CLI + hooks** when shell is available (Claude Code, Cursor, etc.):
- Lower context overhead (~1-2k vs 10-50k tokens)
- Faster execution
- Universal across editors

**Use MCP** when CLI unavailable (Claude Desktop).

### How do I integrate with my editor?

```bash
bd setup claude   # Claude Code
bd setup cursor   # Cursor
bd setup aider    # Aider
```

### Can beads import from GitHub Issues?

Yes:
```bash
bd import --from github --repo owner/repo
```

## Troubleshooting

### Why is the Dolt server not starting?

```bash
# Check server status
bd doctor

# Check server logs
cat .beads/dolt/sql-server.log

# Restart the server
bd dolt stop
bd dolt start
```

### Why aren't my changes syncing?

```bash
# Force sync
bd sync

# Check hooks
bd hooks status
```

### How do I report a bug?

1. Check existing issues: https://github.com/steveyegge/beads/issues
2. Include: `bd version`, `bd info --json`, reproduction steps
3. File at: https://github.com/steveyegge/beads/issues/new
