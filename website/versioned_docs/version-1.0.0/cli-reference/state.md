---
id: state
title: bd state
slug: /cli-reference/state
sidebar_position: 120
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc state` (bd version 1.0.3)

## bd state

Query the current value of a state dimension from an issue's labels.

State labels follow the convention &lt;dimension&gt;:&lt;value&gt;, for example:
  patrol:active
  mode:degraded
  health:healthy

This command extracts the value for a given dimension.

Examples:
  bd state witness-abc patrol     # Output: active
  bd state witness-abc mode       # Output: normal
  bd state witness-abc health     # Output: healthy

```
bd state <issue-id> <dimension>
```

### bd state list

List all state labels (dimension:value format) on an issue.

This filters labels to only show those following the state convention.

Example:
  bd state list witness-abc
  # Output:
  #   patrol: active
  #   mode: normal
  #   health: healthy

```
bd state list <issue-id>
```
