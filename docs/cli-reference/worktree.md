---
title: "bd worktree"
description: "Manage git worktrees for parallel development"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc worktree`.

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
bd worktree [command]
```

## bd worktree create

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

## bd worktree info

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
bd worktree info [flags]
```

## bd worktree list

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
bd worktree list [flags]
```

## bd worktree remove

Remove a registered git worktree with fail-closed safety checks.

Without --force, the target must be clean and its pinned HEAD must be contained
in either the configured upstream or the single comparator selected by
--merged-into. Comparators may be full refs, unambiguous short ref names, or
full commit object IDs. Revision expressions and worktree-local pseudorefs such
as HEAD and ORIG_HEAD are rejected.

--force skips cleanliness and containment requirements, but it does not skip
registered-identity and concurrent-change checks. --force and --merged-into
are mutually exclusive, and each flag may be specified at most once.

Worktree removal and .gitignore cleanup are not atomic. If removal succeeds but
cleanup fails, this command returns an error that explicitly reports the
worktree as removed; it does not claim or attempt a rollback.

Examples:
  bd worktree remove feature-auth                    # Check the configured upstream
  bd worktree remove feature-auth --merged-into main # Check containment in main
  bd worktree remove feature-auth --force            # Skip clean/containment checks

```
bd worktree remove <name> [flags]
```

**Flags:**

```
      --force                Skip cleanliness and containment checks
      --merged-into string   Require worktree HEAD to be contained in this ref
```
