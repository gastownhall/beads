---
id: migrate
title: bd migrate
slug: /cli-reference/migrate
sidebar_position: 620
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc migrate`

## bd migrate

Database migration and data transformation commands.

Without subcommand, checks and updates database metadata to current version.

Subcommands:
  hooks       Plan git hook migration to marker-managed format
  issues      Move issues between repositories
  sync        Set up sync.branch workflow for multi-clone setups


```
bd migrate [flags]
```

**Flags:**

```
      --dry-run          Show what would be done without making changes
      --inspect          Show migration plan and database state for AI agent analysis
      --json             Output migration statistics in JSON format
      --update-repo-id   Update repository ID (use after changing git remote)
      --yes              Auto-confirm prompts
```

### bd migrate hooks

Analyze git hook files and sidecar artifacts for migration to marker-managed format.

Modes:
  --dry-run  Preview migration operations without changing files
  --apply    Apply migration operations

Examples:
  bd migrate hooks --dry-run
  bd migrate hooks --apply
  bd migrate hooks --apply --yes
  bd migrate hooks --dry-run --json

```
bd migrate hooks [path] [flags]
```

**Flags:**

```
      --apply     Apply planned hook migration changes
      --dry-run   Show what would be done without making changes
      --json      Output in JSON format
      --yes       Skip confirmation prompt for --apply
```

### bd migrate issues

Move issues from one source repository to another with filtering and dependency preservation.

This command updates the source_repo field for selected issues, allowing you to:
- Move contributor planning issues to upstream repository
- Reorganize issues across multi-phase repositories
- Consolidate issues from multiple repos

Examples:
  # Preview migration from planning repo to current repo
  bd migrate-issues --from ~/.beads-planning --to . --dry-run

  # Move all open P1 bugs
  bd migrate-issues --from ~/repo1 --to ~/repo2 --priority 1 --type bug --status open

  # Move specific issues with their dependencies
  bd migrate-issues --from . --to ~/archive --id bd-abc --id bd-xyz --include closure

  # Move issues with label filter
  bd migrate-issues --from . --to ~/feature-work --label frontend --label urgent

```
bd migrate issues [flags]
```

**Flags:**

```
      --dry-run            Show plan without making changes
      --from string        Source repository (required)
      --id strings         Specific issue IDs to migrate (can specify multiple)
      --ids-file string    File containing issue IDs (one per line)
      --include string     Include dependencies: none/upstream/downstream/closure (default "none")
      --label strings      Filter by labels (can specify multiple)
      --priority int       Filter by priority (0-4) (default -1)
      --status string      Filter by status (open/closed/all)
      --strict             Fail on orphaned dependencies or missing repos
      --to string          Destination repository (required)
      --type string        Filter by issue type (bug/feature/task/epic/chore/decision)
      --within-from-only   Only include dependencies from source repo (default true)
      --yes                Skip confirmation prompt
```

### bd migrate sync

Configure separate branch workflow for multi-clone setups.

This sets the sync.branch config value so that issue data is committed
to a dedicated branch, keeping your main branch clean.

Example:
  bd migrate sync beads-sync

```
bd migrate sync <branch> [flags]
```

**Flags:**

```
      --dry-run   Show what would be done without making changes
      --json      Output in JSON format
```
