---
id: index
title: Workflows
sidebar_position: 1
---

# Workflows

Beads provides powerful workflow primitives for complex, multi-step processes.

## Chemistry Metaphor

Beads uses a molecular chemistry metaphor:

| Phase | Storage | Synced | Use Case |
|-------|---------|--------|----------|
| **Proto** (solid) | Built-in | N/A | Reusable templates |
| **Mol** (liquid) | `.beads/` | Yes | Persistent work |
| **Wisp** (vapor) | `.beads-wisp/` | No | Ephemeral operations |

## Core Concepts

### Formulas

Declarative workflow templates in TOML or JSON:

```toml
formula = "feature-workflow"
version = 1
type = "workflow"

[[steps]]
id = "design"
title = "Design the feature"
type = "human"

[[steps]]
id = "implement"
title = "Implement the feature"
needs = ["design"]
```

### Molecules

Work graphs with parent-child relationships:
- Created by instantiating formulas with `bd pour`
- Steps have dependencies (`needs`)
- Progress tracked via issue status

### Gates

Async coordination primitives:
- **Human gates** - Wait for human approval
- **Timer gates** - Wait for duration
- **GitHub gates** - Wait for PR merge, CI, etc.

### Wisps

Ephemeral operations that don't sync to git:
- Created with `bd wisp`
- Stored in `.beads-wisp/` (gitignored)
- Auto-expire after completion

## Workflow Commands

| Command | Description |
|---------|-------------|
| `bd pour` | Instantiate formula as molecule |
| `bd wisp` | Create ephemeral wisp |
| `bd mol list` | List molecules |
| `bd pin` | Pin work to agent |
| `bd hook` | Show pinned work |

## Simple Example

```bash
# Create a release workflow
bd pour release --var version=1.0.0

# View the molecule
bd mol show release-1.0.0

# Work through steps
bd update release-1.0.0.1 --claim
bd close release-1.0.0.1
# Next step becomes ready...
```

## Navigation

- [Molecules](/workflows/molecules) - Work graphs and execution
- [Formulas](/workflows/formulas) - Declarative templates
- [Gates](/workflows/gates) - Async coordination
- [Wisps](/workflows/wisps) - Ephemeral operations
