---
title: "bd graph"
description: "Display issue dependency graph"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc graph`.

Display a visualization of an issue's dependency graph.

For epics, shows all children and their dependencies.
For regular issues, shows the issue and its direct dependencies.

With --all, shows all open issues grouped by connected component.
With --open, filters to only open/actionable issues (compact layer format).

Display formats:
  (default)        DAG with columns and box-drawing edges (terminal-native)
  --box            ASCII boxes showing layers, more detailed
  --compact        Tree format, one line per issue, more scannable
  --dot            Graphviz DOT format (pipe to dot -Tsvg &gt; graph.svg)
  --html           Self-contained interactive HTML with D3.js visualization
  --open           Open issues only, compact layers (LLM-friendly)

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
  bd graph --open issue-id       # Open issues only, layered by blocking order
  bd graph --all --open          # All open issues, compact layers

--max-rows / BEADS_MAX_ROWS caveat: the cap is checked differently per mode.
Single-issue graphs (no --all) check the connected-component node count
after the BFS traversal completes — the whole subgraph is always walked
first, then rejected if it's over cap. --all checks each status
(open/in_progress/blocked) independently, so up to 3x the cap can be loaded
in total before any individual status trips it.

```
bd graph [issue-id] [flags]
bd graph [command]
```

**Flags:**

```
      --all            Show graph for all open issues
      --box            ASCII boxes showing layers
      --compact        Tree format, one line per issue, more scannable
      --dot            Output Graphviz DOT format (pipe to: dot -Tsvg > graph.svg)
      --html           Output self-contained interactive HTML (redirect to file)
      --max-rows int   Hard upper bound on rows fetched from storage. Returns a non-zero exit (code 2) and an error to stderr if exceeded. 0 disables (the default). Overrides BEADS_MAX_ROWS for this invocation. Useful in CI/agent rigs that want a circuit breaker against pathological queries. Not supported under --proxied-server: an explicit --max-rows or BEADS_MAX_ROWS cap errors out rather than silently going unenforced.
      --open           Show only open issues (filters out closed/deferred), forces compact layer format
```

## bd graph check

Check the dependency graph for cycles, orphans, and other integrity issues.

Returns exit code 0 if the graph is clean, 1 if issues are found.

```
bd graph check [flags]
```
