---
id: molecules
title: Molecules
sidebar_position: 2
---

# Molecules

Molecules are work graphs created from formulas.

## What is a Molecule?

A molecule is a persistent instance of a formula:
- Contains steps with dependencies
- Tracked in `.beads/` (syncs with git)
- Steps map to issues with parent-child relationships

## Creating Molecules

### From Formula

```bash
# Pour a formula into a molecule
bd pour <formula-name> [--var key=value]
```

**Example:**
```bash
bd pour release --var version=1.0.0
```

This creates:
- Parent issue: `bd-xyz` (the molecule root)
- Child issues: `bd-xyz.1`, `bd-xyz.2`, etc. (the steps)

### Listing Molecules

```bash
bd mol list
bd mol list --json
```

### Viewing a Molecule

```bash
bd mol show <molecule-id>
bd dep tree <molecule-id>  # Shows full hierarchy
```

## Working with Molecules

### Step Dependencies

Steps have `needs` dependencies:

```toml
[[steps]]
id = "implement"
title = "Implement feature"
needs = ["design"]  # Must complete design first
```

The `bd ready` command respects these:

```bash
bd ready  # Only shows steps with completed dependencies
```

### Progressing Through Steps

```bash
# Start a step
bd update bd-xyz.1 --claim

# Complete a step
bd close bd-xyz.1 --reason "Done"

# Check what's ready next
bd ready
```

### Viewing Progress

```bash
# See blocked steps
bd blocked

# See molecule stats
bd stats
```

## Molecule Lifecycle

```
Formula (template)
    ↓ bd pour
Molecule (instance)
    ↓ work steps
Completed Molecule
    ↓ optional cleanup
Archived
```

## Advanced Features

### Bond Points

Formulas can define bond points for composition:

```toml
[compose]
[[compose.bond_points]]
id = "entry"
step = "design"
position = "before"
```

### Hooks

Execute actions on step completion:

```toml
[[steps]]
id = "build"
title = "Build project"

[steps.on_complete]
run = "make build"
```

### Pinning Work

Assign molecules to agents:

```bash
# Pin to current agent
bd pin bd-xyz --start

# Check what's pinned
bd hook
```

## Example Workflow

```bash
# 1. Create molecule from formula
bd pour feature-workflow --var name="dark-mode"

# 2. View structure
bd dep tree bd-xyz

# 3. Start first step
bd update bd-xyz.1 --claim

# 4. Complete and progress
bd close bd-xyz.1
bd ready  # Shows next steps

# 5. Continue until complete
```

## See Also

- [Formulas](/workflows/formulas) - Creating templates
- [Gates](/workflows/gates) - Async coordination
- [Wisps](/workflows/wisps) - Ephemeral workflows
