---
id: show
title: bd show
slug: /cli-reference/show
sidebar_position: 40
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc show`

## bd show

Show issue details

```
bd show [id...] [--id=<id>...] [--current] [flags]
```

**Aliases:** view

**Flags:**

```
      --as-of string     Show issue as it existed at a specific commit hash or branch (requires Dolt)
      --children         Show only the children of this issue
      --current          Show the currently active issue (in-progress, hooked, or last touched)
      --id stringArray   Issue ID (use for IDs that look like flags, e.g., --id=gt--xyz)
      --local-time       Show timestamps in local time instead of UTC
      --long             Show all available fields (extended metadata, agent identity, gate fields, etc.)
      --refs             Show issues that reference this issue (reverse lookup)
      --short            Show compact one-line output per issue
      --thread           Show full conversation thread (for messages)
  -w, --watch            Watch for changes and auto-refresh display
```
