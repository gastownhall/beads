---
id: formula
title: bd formula
slug: /cli-reference/formula
sidebar_position: 310
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc formula`

## bd formula

Manage workflow formulas - the source layer for molecule templates.

Formulas are TOML/JSON files that define workflows with composition rules.
Define formulas, cook them into protos, then pour or wisp them into work.

Search paths (in order):
  1. &lt;resolved-beads-dir&gt;/formulas/ (active project)
  2. &lt;checkout-root&gt;/.beads/formulas/ (repo-local formulas)
  3. ~/.beads/formulas/ (user)
  4. $GT_ROOT/.beads/formulas/ (shared workspace root, if GT_ROOT set)

Commands:
  list   List available formulas from all search paths
  show   Show formula details, steps, and composition rules

```
bd formula
```

### bd formula convert

Convert formula files from JSON to TOML format.

TOML format provides better ergonomics:
  - Multi-line strings without \n escaping
  - Human-readable diffs
  - Comments allowed

The convert command reads a .formula.json file and outputs .formula.toml.
The original JSON file is preserved (use --delete to remove it).

Examples:
  bd formula convert shiny              # Convert shiny.formula.json to .toml
  bd formula convert ./my.formula.json  # Convert specific file
  bd formula convert --all              # Convert all JSON formulas
  bd formula convert shiny --delete     # Convert and remove JSON file
  bd formula convert shiny --stdout     # Print TOML to stdout

```
bd formula convert <formula-name|path> [--all] [flags]
```

**Flags:**

```
      --all      Convert all JSON formulas
      --delete   Delete JSON file after conversion
      --stdout   Print TOML to stdout instead of file
```

### bd formula list

List all formulas from search paths.

Search paths (in order of priority):
  1. &lt;resolved-beads-dir&gt;/formulas/ (active project - highest priority)
  2. &lt;checkout-root&gt;/.beads/formulas/ (repo-local formulas)
  3. ~/.beads/formulas/ (user)
  4. $GT_ROOT/.beads/formulas/ (shared workspace root, if GT_ROOT set)

Formulas in earlier paths shadow those with the same name in later paths.

Examples:
  bd formula list
  bd formula list --json
  bd formula list --type workflow
  bd formula list --type convoy

```
bd formula list [flags]
```

**Flags:**

```
      --type string   Filter by type (workflow, expansion, aspect, convoy)
```

### bd formula show

Show detailed information about a formula.

Displays:
  - Formula metadata (name, type, description)
  - Variables with defaults and constraints
  - Steps with dependencies
  - Composition rules (extends, aspects, expansions)
  - Bond points for external composition

Examples:
  bd formula show shiny
  bd formula show rule-of-five
  bd formula show security-audit --json

```
bd formula show <formula-name>
```
