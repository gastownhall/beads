---
id: graph
title: bd graph
slug: /cli-reference/graph
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc graph` (bd version 1.0.3)

## bd graph

Display a visualization of an issue's dependency graph.

For epics, shows all children and their dependencies.
For regular issues, shows the issue and its direct dependencies.

With --all, shows all open issues grouped by connected component.

Display formats:
  (default)        DAG with columns and box-drawing edges (terminal-native)
  --box            ASCII boxes showing layers, more detailed
  --compact        Tree format, one line per issue, more scannable
  --dot            Graphviz DOT format (pipe to dot -Tsvg &gt; graph.svg)
  --html           Self-contained interactive HTML with D3.js visualization

The graph shows execution order:
- Layer 0 / leftmost = no dependencies (can start immediately)
- Higher layers depend on lower layers
- Nodes in the same layer can run in parallel

Status icons: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred

Examples:
  bd graph issue-id              # Terminal DAG visualization (default)
  bd graph --box issue-id        # ASCII boxes with layer grouping
  bd graph --dot issue-id | dot -Tsvg &gt; graph.svg  # SVG via Graphviz
  bd graph --dot issue-id | dot -Tpng &gt; graph.png  # PNG via Graphviz
  bd graph --html issue-id &gt; graph.html  # Interactive browser view
  bd graph --all --html &gt; all.html       # All issues, interactive

```
bd graph [issue-id] [flags]
```

**Flags:**

```
      --all       Show graph for all open issues
      --box       ASCII boxes showing layers
      --compact   Tree format, one line per issue, more scannable
      --dot       Output Graphviz DOT format (pipe to: dot -Tsvg > graph.svg)
      --html      Output self-contained interactive HTML (redirect to file)
```

### bd graph check

Check the dependency graph for cycles, orphans, and other integrity issues.

Returns exit code 0 if the graph is clean, 1 if issues are found.

```
bd graph check
```
