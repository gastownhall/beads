---
id: worktree
title: bd worktree
slug: /cli-reference/worktree
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc worktree`

## bd worktree

Manage git worktrees with proper beads configuration.

Worktrees allow multiple working directories sharing the same git repository,
enabling parallel development (e.g., multiple agents or features).

Worktrees automatically share the same beads database as the main repository
via git common directory discovery — no manual redirect configuration needed.

Examples:
  bd worktree create feature-auth           # Create worktree
  bd worktree create bugfix --branch fix-1  # Create with specific branch name
  bd worktree list                          # List all worktrees
  bd worktree remove feature-auth           # Remove worktree (with safety checks)
  bd worktree info                          # Show info about current worktree

```
bd worktree
```

### bd worktree create

Create a git worktree for parallel development.

This command:
1. Creates a git worktree at ./&lt;name&gt; (or specified path)
2. Adds the worktree path to .gitignore (if inside repo root)

The worktree automatically shares the same beads database as the main
repository via git common directory discovery — no redirect file needed.

Examples:
  bd worktree create feature-auth           # Create at ./feature-auth
  bd worktree create bugfix --branch fix-1  # Create with branch name
  bd worktree create ../agents/worker-1     # Create at relative path

```
bd worktree create <name> [--branch=<branch>] [flags]
```

**Flags:**

```
      --branch string   Branch name for the worktree (default: same as name)
```

### bd worktree info

Show information about the current worktree.

If the current directory is in a git worktree, shows:
- Worktree path and name
- Branch
- Beads configuration (redirect or main)
- Main repository location

Examples:
  bd worktree info          # Show current worktree info
  bd worktree info --json   # JSON output

```
bd worktree info
```

### bd worktree list

List all git worktrees and their beads configuration state.

Shows each worktree with:
- Name (directory name)
- Path (full path)
- Branch
- Beads state: "redirect" (uses shared db), "shared" (is main), "none" (no beads)

Examples:
  bd worktree list          # List all worktrees
  bd worktree list --json   # JSON output

```
bd worktree list
```

### bd worktree remove

Remove a git worktree with safety checks.

Before removing, this command checks for:
- Uncommitted changes
- Unpushed commits
- Stashes

Use --force to skip safety checks (not recommended).

Examples:
  bd worktree remove feature-auth         # Remove with safety checks
  bd worktree remove feature-auth --force # Skip safety checks

```
bd worktree remove <name> [flags]
```

**Flags:**

```
      --force   Skip safety checks
```
