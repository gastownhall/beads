---
title: "bd migrate"
description: "Database migration commands"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc migrate`.

Database migration and data transformation commands.

Without subcommand, checks and updates database metadata to current version.

Subcommands:
  hooks                            Plan git hook migration to marker-managed format
  issues                           Move issues between repositories
  schema                           Apply pending schema migrations (idempotent)
  sync                             Set up sync.branch workflow for multi-clone setups
  from-server-to-proxied-server           [EXPERIMENTAL] Switch server mode to proxied-server mode
  from-proxied-server-to-server           [EXPERIMENTAL] Switch proxied-server mode to server mode
  from-shared-server-to-proxied-server    [EXPERIMENTAL] Switch shared-server mode to proxied-server mode
  from-proxied-server-to-shared-server    [EXPERIMENTAL] Switch proxied-server mode to shared-server mode

On a remote-backed database with pending schema migrations bd refuses to
migrate in place (#4259): migrating two clones independently forks the schema
so bd dolt pull can no longer merge — the break is silent and unrecoverable.
Use --force to confirm you are the single designated migrator, after which you
should publish the migrated schema with 'bd dolt push'. The env-var equivalent
BD_ALLOW_REMOTE_MIGRATE=1 remains supported for scripted/CI use.


```
bd migrate [flags]
bd migrate [command]
```

**Flags:**

```
      --dry-run          Show what would be done without making changes
      --force            Bypass the remote-migrate gate as the single designated migrator (equivalent to BD_ALLOW_REMOTE_MIGRATE=1)
      --inspect          Show migration plan and database state for AI agent analysis
      --json             Output migration statistics in JSON format
      --update-repo-id   Update repository ID (use after changing git remote)
      --yes              Auto-confirm prompts
```

## bd migrate from-proxied-server-to-server

Switch a repo from proxied-server mode to server mode (bd init --server).

Both modes root their dolt sql-server at the same .beads/dolt directory, so this
only rewrites .beads/metadata.json (dolt_mode) and removes the proxied-server
sidecar — no Dolt data is copied or moved. Stop the running proxy first with
'bd dolt stop'.

Note: dolt_mode lives in the committed metadata.json, so this change propagates
to clones on the next push.

```
bd migrate from-proxied-server-to-server [flags]
```

**Flags:**

```
      --dry-run   Show what would be done without making changes
```

## bd migrate from-proxied-server-to-shared-server

Switch a repo from proxied-server mode back to shared-server mode.

Only applies to a proxied-server repo rooted at the shared dolt directory
(~/.beads/shared-server/dolt) — the reverse of from-shared-server-to-proxied-server.
This rewrites .beads/metadata.json (dolt_mode), re-enables dolt.shared-server, and
removes the proxied-server sidecar; no Dolt data is copied or moved. Stop the
running proxy first with 'bd dolt stop'.

```
bd migrate from-proxied-server-to-shared-server [flags]
```

**Flags:**

```
      --dry-run   Show what would be done without making changes
```

## bd migrate from-server-to-proxied-server

Switch a repo from server mode (bd init --server) to proxied-server mode.

Both modes root their dolt sql-server at the same .beads/dolt directory, so this
only rewrites .beads/metadata.json (dolt_mode) and writes the proxied-server
sidecar — no Dolt data is copied or moved. Stop the running server first with
'bd dolt stop'.

Note: dolt_mode lives in the committed metadata.json, so this change propagates
to clones on the next push.

```
bd migrate from-server-to-proxied-server [flags]
```

**Flags:**

```
      --dry-run                 Show what would be done without making changes
      --idle-timeout duration   Proxy idle timeout; omit for the 30s default, 0 for indefinite uptime
```

## bd migrate from-shared-server-to-proxied-server

Switch a repo from shared-server mode to proxied-server mode.

The proxied server is rooted at the shared dolt directory
(~/.beads/shared-server/dolt), so no Dolt data is copied or moved; this rewrites
.beads/metadata.json (dolt_mode), turns off dolt.shared-server for this repo, and
writes the proxied-server sidecar. Stop the running shared server first with
'bd dolt stop' — note that stops it for every project sharing it.

```
bd migrate from-shared-server-to-proxied-server [flags]
```

**Flags:**

```
      --dry-run                 Show what would be done without making changes
      --idle-timeout duration   Proxy idle timeout; omit for the 30s default, 0 for indefinite uptime
```

## bd migrate hooks

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

## bd migrate issues

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

## bd migrate schema

Apply pending schema migrations idempotently.

Schema migrations also run automatically on store open, so this subcommand
is typically a no-op. It exists to make migration explicit and observable
in CI, release gates, and recovery scenarios.

Example:
  bd migrate schema
  bd migrate schema --json

```
bd migrate schema [flags]
```

**Flags:**

```
      --force   Bypass the remote-migrate gate as the single designated migrator (equivalent to BD_ALLOW_REMOTE_MIGRATE=1)
      --json    Output in JSON format
```

## bd migrate sync

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
