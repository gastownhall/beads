---
title: Workflows
description: Declare multi-step work once as a formula, then stamp it out as molecules of real, dependency-ordered beads.
---

Repeatable multi-step work — a release checklist, a feature pipeline, a
review process — shouldn't be re-planned by hand every time. Beads lets you
declare the shape once and instantiate it on demand: a **formula** (TOML
source) is cooked into a **proto** (template), and the proto is poured into a
**molecule** — real beads whose steps flow through `bd ready` like any other
work. The full pipeline is diagrammed in
[How Beads Works](/core-concepts/index).

```bash
bd formula list                    # formulas visible on the search paths
bd cook release.formula.toml       # compile the formula into a proto
bd mol pour release --var version=1.2.0   # instantiate real work
bd ready --mol <mol-id>            # which steps can run right now
```

The three phases, in the chemistry metaphor the CLI uses:

| Phase | What it is | Lifecycle |
|-------|------------|-----------|
| **Proto** (solid) | template epic with `{{variables}}`, carries the `template` label | reusable, not live work |
| **Molecule** (liquid) | persistent beads poured from a proto (`bd mol pour`) | synced like any bead |
| **Wisp** (vapor) | ephemeral instantiation (`bd mol wisp`) | excluded from federation push by default; deleted by `bd purge` |

## Pages in this section

- [Molecules](/workflows/molecules) — instantiated work graphs: pouring,
  inspecting, bonding, and squashing molecules.
- [Formulas](/workflows/formulas) — the TOML/JSON source format: steps,
  `needs` dependencies, variables, and composition rules.
- [Gates](/workflows/gates) — async wait conditions (human, timer, GitHub
  run/PR, cross-rig bead) that park a step until the world catches up.
- [Wisps](/workflows/wisps) — ephemeral molecules for transient operational
  work that shouldn't clutter history.
- [TODO Command](/workflows/todo) — `bd todo`, the lightweight interface for
  managing TODO items as task beads.
