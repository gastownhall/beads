---
title: "bd human"
description: "Show essential commands for human users"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc human`.

Display a focused help menu showing only the most common commands.

bd has 70+ commands - many for AI agents, integrations, and advanced workflows.
This command shows the ~15 essential commands that human users need most often.

For the full command list, run: bd --help

SUBCOMMANDS:
  human list              List human-needed beads (issues with 'human' label; hides closed by default)
  human respond &lt;id&gt;      Respond to a human-needed bead (adds comment and closes)
  human dismiss &lt;id&gt;      Dismiss a human-needed bead permanently
  human stats             Show summary statistics for human-needed beads

```
bd human [flags]
bd human [command]
```

## bd human dismiss

Dismiss a human-needed bead permanently without responding.

The issue is closed with a "Dismissed" reason and optional note.
The reason can be given as positional arguments or --reason.

Examples:
  bd human dismiss bd-123
  bd human dismiss bd-123 "No longer applicable"
  bd human dismiss bd-123 --reason "No longer applicable"

```
bd human dismiss <issue-id> [reason...] [flags]
```

**Flags:**

```
      --reason string   Reason for dismissal (optional)
```

## bd human list

List issues labeled with 'human' tag.

These are issues that require human intervention or input. Every
human-labeled bead shows regardless of type (including gates and wisps).
By default closed, pinned, and other done/frozen beads are hidden; use
--status to select specific statuses, or --status=all to include every
status.

Examples:
  bd human list
  bd human list --status=closed
  bd human list --status=all
  bd human list --json

```
bd human list [flags]
```

**Flags:**

```
  -s, --status string   Filter by status (open, closed, etc.; comma-separated for multiple, 'all' for every status)
```

## bd human respond

Respond to a human-needed bead by adding a comment and closing it.

The response is added as a comment and the issue is closed with reason "Responded".
The response text can be given as positional arguments, --response, --file, or --stdin.

Examples:
  bd human respond bd-123 "Use OAuth2 for authentication"
  bd human respond bd-123 -r "Approved, proceed with implementation"
  bd human respond bd-123 --file response.md
  echo "Approved" | bd human respond bd-123 --stdin

```
bd human respond <issue-id> [response...] [flags]
```

**Flags:**

```
      --file string       Read response text from file
  -r, --response string   Response text
      --stdin             Read response text from stdin
```

## bd human stats

Display summary statistics for human-needed beads.

Shows counts for total, pending (open), responded (closed without dismiss),
and dismissed beads.

Example:
  bd human stats

```
bd human stats [flags]
```
