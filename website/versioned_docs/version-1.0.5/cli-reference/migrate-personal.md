---
id: migrate-personal
title: bd migrate-personal
slug: /cli-reference/migrate-personal
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc migrate-personal`

## bd migrate-personal

Identify issues you created in the project database and move them to your
personal planning repository (~/.beads-planning by default).

This is a one-time migration for contributors who created personal planning
issues before contributor routing was configured.

The command:
  1. Finds all issues in the project database created by your git identity
  2. Shows you the list and asks for confirmation
  3. Moves them to the planning repo configured in routing.contributor

EXAMPLES:
  bd migrate-personal        # Interactive: show list and prompt
  bd migrate-personal -y     # Non-interactive: skip confirmation

```
bd migrate-personal [flags]
```

**Flags:**

```
  -y, --yes   Skip confirmation prompt
```
