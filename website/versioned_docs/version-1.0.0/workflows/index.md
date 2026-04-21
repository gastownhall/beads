---
id: index
title: Workflows
sidebar_position: 1
---

# Workflows

Beads provides powerful workflow primitives for complex, multi-step processes.

## Chemistry Metaphor

Beads uses a molecular chemistry metaphor for workflow phases:

| Phase | Command | Synced | Use Case |
|-------|---------|--------|----------|
| **Proto** (solid) | `bd cook` | N/A | Compiled template, reusable |
| **Mol** (liquid) | `bd mol pour` | Yes | Persistent work with audit trail |
| **Wisp** (vapor) | `bd mol wisp` | No | Ephemeral operations |

## Core Concepts

### Formulas

Declarative workflow templates in TOML or JSON. Support variables, conditional steps, loops, inheritance, and composition.

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

### Cooking

`bd cook` compiles a formula into a resolved proto. Two modes: compile-time (keeps `{{var}}` placeholders for planning) and runtime (substitutes all variables for final output).

### Molecules

Work graphs with parent-child relationships:
- Created by instantiating formulas with `bd mol pour`
- Steps have dependencies (`needs`)
- Progress tracked via issue status

### Gates

Async coordination primitives:
- **Human gates** - Wait for human approval
- **Timer gates** - Wait for duration
- **GitHub gates** - Wait for PR merge, CI, etc.

### Wisps

Ephemeral operations that don't sync:
- Created with `bd mol wisp`
- Stored locally with `Ephemeral=true`
- Lifecycle: squash (promote), burn (discard), or GC (auto-clean)

### Swarms

Parallel execution across an epic's dependency graph:
- Analyze epic structure for parallelism: `bd swarm validate`
- Create coordinated swarm from epic: `bd swarm create`
- Monitor progress across waves: `bd swarm status`

## Workflow Commands

| Command | Description |
|---------|-------------|
| `bd cook` | Compile formula into proto |
| `bd mol pour` | Instantiate formula as persistent molecule |
| `bd mol wisp` | Create ephemeral wisp from formula |
| `bd mol list` | List molecules |
| `bd mol squash` | Promote wisp to persistent molecule |
| `bd mol burn` | Delete wisp without trace |
| `bd mol wisp gc` | Garbage collect old wisps |
| `bd mol bond` | Bond formulas together with phase control |
| `bd swarm validate` | Analyze epic for parallel execution |
| `bd swarm create` | Create swarm from epic |
| `bd swarm status` | Show swarm progress |
| `bd swarm list` | List all swarm molecules |
| `bd formula list` | List available formulas |

## Simple Example

```bash
# Create a release workflow
bd mol pour release --var version=1.0.0

# View the molecule
bd dep tree bd-xyz

# Work through steps
bd update bd-xyz.1 --claim
bd close bd-xyz.1
# Next step becomes ready...
```

## Navigation

- [Molecules](/workflows/molecules) - Work graphs and execution
- [Formulas](/workflows/formulas) - Declarative templates (types, variables, inheritance, loops, conditions)
- [Gates](/workflows/gates) - Async coordination
- [Wisps](/workflows/wisps) - Ephemeral operations (squash, burn, GC lifecycle)
