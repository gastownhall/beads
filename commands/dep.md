---
description: Manage dependencies between issues
argument-hint: [command] [from-id] [to-id]
---

Manage dependencies between beads issues.

## Available Commands

- **add**: Add a dependency between issues
  - $1: "add"
  - $2: From issue ID
  - $3: To issue ID
  - $4: Dependency type (blocks, related, parent-child, discovered-from)

- **remove**: Remove a dependency
  - $1: "remove"
  - $2: From issue ID
  - $3: To issue ID

- **tree**: Show dependency tree for an issue
  - $1: "tree"
  - $2: Issue ID
  - Flags:
    - `--reverse`: Show dependent tree (what was discovered from this) instead of dependency tree (what blocks this)
    - `--json`: Output as JSON
    - `--max-depth N`: Limit tree depth (default: 50)
    - `--show-all-paths`: Show all paths (no deduplication for diamond dependencies)

- **cycles**: Detect dependency cycles

## Dependency Types

- **blocks**: Hard blocker (from blocks to) - affects ready queue
- **related**: Soft relationship - for context only
- **parent-child**: Epic/subtask relationship
- **discovered-from**: Track issues found during work

## Examples

- `bd dep add bd-10 bd-20 --type blocks`: bd-10 blocks bd-20
- `bd dep tree bd-20`: Show what blocks bd-20 (dependency tree going UP)
- `bd dep tree bd-1 --reverse`: Show what was discovered from bd-1 (dependent tree going DOWN)
- `bd dep tree bd-1 --reverse --max-depth 3`: Show discovery tree with depth limit
- `bd dep cycles`: Check for circular dependencies

## Reverse Mode: Discovery Trees

The `--reverse` flag inverts the tree direction to show **dependents** instead of **dependencies**:

**Normal mode** (`bd dep tree ISSUE`):
- Shows what blocks you (dependency tree)
- Answers: "What must I complete before I can work on this?"
- Tree flows **UP** toward prerequisites

**Reverse mode** (`bd dep tree ISSUE --reverse`):
- Shows what was discovered from you (dependent tree)
- Answers: "What work was discovered while working on this?"
- Tree flows **DOWN** from goal to discovered tasks
- Perfect for visualizing work breakdown and discovery chains

**Use Cases:**
- Document project evolution and how work expanded from initial goal
- Share "how we got here" context with stakeholders
- Visualize work breakdown structure from epics
- Track discovery chains (what led to what)
- Show yak shaving journeys in retrospectives
