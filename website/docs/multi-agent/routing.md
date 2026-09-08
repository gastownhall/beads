---
id: routing
title: Routing
sidebar_position: 2
---

# Multi-Repo Routing

Automatic issue routing across repositories.

## Overview

Routing enables:
- Issues created in one repo routed to another
- Pattern-based routing rules
- Fallback to default repository

## Configuration

Create `.beads/routes.jsonl`:

```jsonl
{"pattern": "frontend/**", "target": "frontend-repo", "priority": 10}
{"pattern": "backend/**", "target": "backend-repo", "priority": 10}
{"pattern": "docs/**", "target": "docs-repo", "priority": 5}
{"pattern": "*", "target": "main-repo", "priority": 0}
```

## Route Fields

| Field | Description |
|-------|-------------|
| `pattern` | Glob pattern to match |
| `target` | Target repository |
| `priority` | Higher = checked first |

## Pattern Matching

Patterns match against:
- Issue title
- Labels
- Explicit path prefix

**Examples:**
```jsonl
{"pattern": "frontend/*", "target": "frontend"}
{"pattern": "*api*", "target": "backend"}
{"pattern": "label:docs", "target": "docs-repo"}
```

## Commands

```bash
# Show routing table
bd routes list
bd routes list --json

# Test routing
bd routes test "Fix frontend button"
bd routes test --label frontend

# Add route
bd routes add "frontend/**" --target frontend-repo --priority 10

# Remove route
bd routes remove "frontend/**"
```

## Auto-Routing

When creating issues, beads checks routes:

```bash
bd create "Fix frontend button alignment" -t bug
# Auto-routed to frontend-repo based on title match
```

Override with explicit target:

```bash
bd create "Fix button" --repo backend-repo
```

## Cross-Repo Dependencies

Track dependencies across repos:

```bash
# In frontend-repo
bd dep add bd-42 external:backend-repo/bd-100

# View cross-repo deps
bd dep tree bd-42 --cross-repo
```

## Hydration

Pull related issues from other repos:

```bash
# Hydrate issues from related repos
bd hydrate

# Preview hydration
bd hydrate --dry-run

# Hydrate specific repo
bd hydrate --from backend-repo
```

## Best Practices

1. **Use specific patterns** - Avoid overly broad matches
2. **Set priorities** - Ensure specific patterns match first
3. **Default fallback** - Always have a `*` pattern with lowest priority
4. **Test routes** - Use `bd routes test` before committing
