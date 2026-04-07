---
id: wisps
title: Wisps
sidebar_position: 5
---

# Wisps

Wisps are ephemeral workflows that don't sync to git.

## What are Wisps?

Wisps are "vapor phase" molecules:
- Stored in `.beads-wisp/` (gitignored)
- Don't sync with git
- Auto-expire after completion
- Perfect for temporary operations

## Use Cases

| Scenario | Why Wisp? |
|----------|-----------|
| Local experiments | No need to pollute git history |
| CI/CD pipelines | Ephemeral by nature |
| Scratch workflows | Quick throwaway work |
| Agent coordination | Local-only coordination |

## Creating Wisps

```bash
# Create wisp from formula
bd wisp create <formula> [--var key=value]

# Example
bd wisp create quick-check --var target=auth-module
```

## Wisp Commands

```bash
# List wisps
bd wisp list
bd wisp list --json

# Show wisp details
bd wisp show <wisp-id>

# Delete wisp
bd wisp delete <wisp-id>

# Delete all completed wisps
bd wisp cleanup
```

## Wisp vs Molecule

| Aspect | Molecule | Wisp |
|--------|----------|------|
| Storage | `.beads/` | `.beads-wisp/` |
| Git sync | Yes | No |
| Persistence | Permanent | Ephemeral |
| Use case | Tracked work | Temporary ops |

## Phase Control

Use `bd mol bond` to control phase:

```bash
# Force liquid (persistent molecule)
bd mol bond <formula> <target> --pour

# Force vapor (ephemeral wisp)
bd mol bond <formula> <target> --wisp
```

## Example: Quick Check Workflow

Create a wisp for running checks:

```toml
# .beads/formulas/quick-check.formula.toml
formula = "quick-check"
description = "Quick local checks"

[[steps]]
id = "lint"
title = "Run linter"

[[steps]]
id = "test"
title = "Run tests"
needs = ["lint"]

[[steps]]
id = "build"
title = "Build project"
needs = ["test"]
```

Use as wisp:

```bash
bd wisp create quick-check
# Work through steps...
bd wisp cleanup  # Remove when done
```

## Auto-Expiration

Wisps can auto-expire:

```toml
[wisp]
expires_after = "24h"  # Auto-delete after 24 hours
```

Or cleanup manually:

```bash
bd wisp cleanup --all  # Remove all wisps
bd wisp cleanup --completed  # Remove only completed
```

## Best Practices

1. **Use wisps for local-only work** - Don't sync to git
2. **Clean up regularly** - `bd wisp cleanup`
3. **Use molecules for tracked work** - Wisps are ephemeral
4. **Consider CI/CD wisps** - Perfect for pipeline steps
