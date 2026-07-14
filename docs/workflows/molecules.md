---
title: Molecules
description: Molecules are epics whose children flow through bd ready as ordered steps; covers creating, executing, bonding, and the molecule lifecycle.
---

Molecules are work graphs: epics whose children flow through `bd ready` as
dependency-ordered steps. They are usually instantiated from formulas, but a
formula is optional — any epic with children is a molecule.

## What is a Molecule?

A molecule is a persistent instance of a proto (a cooked formula):
- Contains steps with dependencies
- Persistent beads in the issue database, synced like any other bead
- Steps map to issues with parent-child relationships

Under the hood, **a molecule is just an epic** — a parent bead with children —
plus workflow semantics:

| Term | Meaning | When to use |
|------|---------|-------------|
| **Epic** | Parent issue with children | General term for hierarchical work |
| **Molecule** | Epic with execution intent | When discussing workflow traversal |
| **Proto** | Epic with the `template` label | Reusable pattern (optional) |

Protos and formulas are optional layers for reusable patterns and complex
composition — most work needs only epics and dependencies.

## Creating Molecules

### From a Formula

```bash
# Cook the formula into a proto, then pour the proto into a molecule
bd cook release.formula.toml
bd mol pour release --var version=1.0.0
```

This creates:
- Parent issue: `bd-xyz` (the molecule root)
- Child issues: `bd-xyz.1`, `bd-xyz.2`, etc. (the steps)

### Without a Formula

Create the epic and wire the dependencies directly:

```bash
bd create "Feature X" -t epic
bd create "Design" -t task --parent <epic-id>
bd create "Implement" -t task --parent <epic-id>
bd create "Test" -t task --parent <epic-id>
bd dep add <implement-id> <design-id>   # implement needs design
bd dep add <test-id> <implement-id>     # test needs implement
```

If an ad-hoc epic turns out to be worth repeating, extract a reusable formula
from it with `bd mol distill <epic-id> <formula-name>`.

### Finding Molecules

```bash
bd mol current           # Where you are in the molecule you're working
bd mol stale             # Complete-but-still-open molecules
bd mol wisp list         # Ephemeral molecules (wisps)
```

### Viewing a Molecule

```bash
bd mol show <molecule-id>             # Structure and variables
bd mol show <molecule-id> --parallel  # Highlight steps that can run concurrently
bd dep tree <molecule-id>             # Shows full hierarchy
```

## Working with Molecules

### The Execution Model

An agent picks up a molecule and executes ready children in parallel until
everything closes:

```
epic-root (assigned to agent)
├── child.1 (no deps → ready)      ← execute in parallel
├── child.2 (no deps → ready)      ← execute in parallel
├── child.3 (needs child.1) → blocked until child.1 closes
└── child.4 (needs child.2, child.3) → blocked until both close
```

**Children are parallel by default.** Only explicit dependencies create
sequence. The multi-session loop:

1. Get ready work: `bd ready --mol <molecule-id>`
2. Claim it: `bd update <id> --claim`
3. Do the work
4. Close it: `bd close <id>`
5. Repeat until the molecule is done

### Dependency Types

Only some dependency types block execution:

| Type | Semantics | Use case |
|------|-----------|----------|
| `blocks` | B can't start until A closes | Sequencing work |
| `parent-child` | If the parent is blocked, children are blocked | Hierarchy (children parallel by default) |
| `conditional-blocks` | B runs only if A fails | Error-handling paths |
| `waits-for` | B waits for all of A's dynamic children | Fan-in gates — see [Gates](/workflows/gates) |

Non-blocking types (`related`, `discovered-from`, `replies-to`) link issues
without affecting execution.

### Step Dependencies

In a formula, steps declare `needs`:

```toml
[[steps]]
id = "implement"
title = "Implement feature"
needs = ["design"]  # Must complete design first
```

On live issues, add the edge directly — the dependent comes first:

```bash
bd dep add <B-id> <A-id>   # B depends on A (B needs A)
```

The `bd ready` command respects these:

```bash
bd ready --mol <molecule-id>  # Only shows steps with completed dependencies
```

### Progressing Through Steps

```bash
# Start a step
bd update bd-xyz.1 --claim

# Complete a step
bd close bd-xyz.1 --reason "Done"

# Check what's ready next
bd ready --mol bd-xyz
```

### Viewing Progress

```bash
# See blocked steps
bd blocked

# Step-by-step status: [done] / [current] / [ready] / [blocked] / [pending]
bd mol current <molecule-id>

# Progress summary: completed/total, rate, ETA
bd mol progress <molecule-id>
```

## Molecule Lifecycle

```
Formula (template source)
    ↓ bd cook
Proto (template epic)
    ↓ bd mol pour
Molecule (instance)
    ↓ work steps
Completed Molecule
    ↓ optional cleanup
Closed / Squashed / Burned
```

Closing the last child does not close the molecule root — epics stay open as
close-eligible work until closed explicitly (`bd epic close-eligible` sweeps
them). For cleanup of the beads themselves:

- `bd mol squash <id>` condenses a molecule's ephemeral children into a
  permanent digest issue.
- `bd mol burn <id>` deletes a molecule outright, no digest — for abandoned
  or test runs.

See [Wisps](/workflows/wisps) for the ephemeral lifecycle these commands
usually serve.

## Bonding: Connecting Work Graphs

**Bond** means creating a dependency between two work graphs. When molecule A
blocks molecule B, completing A unblocks B and an agent can continue from A
into B — one compound workflow that can span days.

```bash
bd mol bond A B                    # B depends on A (sequential by default)
bd mol bond A B --type parallel    # B runs alongside A
bd mol bond A B --type conditional # B runs only if A fails
```

The command is polymorphic over its operands:

| Operands | What happens |
|----------|--------------|
| proto + proto | Compound proto (reusable template) |
| proto + molecule | Spawns the proto as new issues, attached to the molecule |
| molecule + molecule | Joins them into a compound molecule |
| formula + anything | The formula is cooked inline first |

Spawned issues follow the target's phase (persistent or ephemeral) by
default. Override with `--pour` (force persistent) or `--ephemeral` (force
ephemeral) — see [Wisps](/workflows/wisps).

### Dynamic Bonding

When the number of children isn't known until runtime, bond in a loop with
`--ref` to get readable child IDs instead of random hashes:

```bash
# One arm per discovered worker
bd mol bond mol-worker-arm bd-patrol --ref arm-{{name}} --var name=ace
# Creates: bd-patrol.arm-ace (and children like bd-patrol.arm-ace.capture)
```

## Advanced Features

### Bond Points

Formulas can define bond points — named attachment sites for composition.
Each names a step to attach `before_step` or `after_step` (with optional
`parallel = true`):

```toml
[[compose.bond_points]]
id = "entry"
description = "Attach setup work here"
before_step = "design"
```

### Hooks

Step-completion hooks are not currently exposed as runnable formula
actions. The historical `on_complete.run` example was invalid: `run` is
not a formula field, and `on_complete` runtime expansion is tracked
separately until it is wired end to end.

### Assigning Molecules

Assign the molecule root to an agent at pour time, then track where each
agent is:

```bash
bd mol pour mol-feature --assignee <agent>   # Assign on creation
bd mol current --for <agent>                 # Where that agent is
```

## Agent Pitfalls

1. **Temporal language inverts dependencies.** "Phase 1 comes before Phase 2"
   tempts `bd dep add phase1 phase2` — backwards. Use requirement language:
   "Phase 2 needs Phase 1" is `bd dep add phase2 phase1`. Verify with
   `bd blocked`.
2. **Numbered steps don't create sequence.** Steps named "Step 1/2/3" still
   run in parallel until you add dependencies between them.
3. **Forgetting to close work.** Blocked issues stay blocked forever if their
   blockers aren't closed: `bd close <id> --reason "Done"`.

## Example Workflow

```bash
# 1. Create molecule from formula
bd cook feature-workflow.formula.toml
bd mol pour feature-workflow --var name="dark-mode"

# 2. View structure
bd dep tree bd-xyz

# 3. Start first step
bd update bd-xyz.1 --claim

# 4. Complete and progress
bd close bd-xyz.1
bd ready --mol bd-xyz  # Shows next steps

# 5. Continue until complete
```

## See Also

- [Formulas](/workflows/formulas) - Creating templates
- [Gates](/workflows/gates) - Async coordination
- [Wisps](/workflows/wisps) - Ephemeral workflows
