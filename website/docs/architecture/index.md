---
sidebar_position: 1
title: Architecture Overview
description: Understanding Beads' three-layer data model
---

# Architecture Overview

This document explains how Beads' three-layer architecture works: Git, JSONL, and SQLite.

## The Three Layers

Beads uses a layered architecture where each layer serves a specific purpose:

```mermaid
flowchart TD
    subgraph GIT["🗂️ Layer 1: Git Repository"]
        G[(".beads/*.jsonl<br/><i>Historical Source of Truth</i>")]
    end

    subgraph JSONL["📄 Layer 2: JSONL Files"]
        J[("issues.jsonl<br/><i>Operational Source of Truth</i>")]
    end

    subgraph SQL["⚡ Layer 3: SQLite"]
        D[("beads.db<br/><i>Fast Queries / Derived State</i>")]
    end

    G <-->|"bd sync"| J
    J -->|"rebuild"| D
    D -->|"append"| J

    U((👤 User)) -->|"bd create<br/>bd update"| D
    D -->|"bd list<br/>bd show"| U

    style GIT fill:#2d5a27,stroke:#4a9c3e,color:#fff
    style JSONL fill:#1a4a6e,stroke:#3a8ac4,color:#fff
    style SQL fill:#6b3a6b,stroke:#a45ea4,color:#fff
```

:::info Historical vs Operational Truth
**Git** is the *historical* source of truth—commits preserve the full history of your issues and can be recovered from any point in time.

**JSONL** is the *operational* source of truth—when recovering from database corruption, Beads rebuilds SQLite from JSONL files, not directly from Git commits.

This layered model enables recovery: if SQLite is corrupted but JSONL is intact, run `bd sync --import-only` to rebuild. If JSONL is corrupted, recover it from Git history first.
:::

### Layer 1: Git Repository

Git is the *historical* source of truth. All issue data lives in the repository alongside your code, with full history preserved in commits.

**Why Git?**
- Issues travel with the code
- No external service dependency
- Full history via Git log (recover any point in time)
- Works offline
- Enables multi-machine and multi-agent workflows

### Layer 2: JSONL Files

JSONL (JSON Lines) files store issue data in an append-only format. This is the *operational* source of truth—SQLite databases are rebuilt from JSONL.

**Location:** `.beads/*.jsonl`

**Why JSONL?**
- Human-readable and inspectable
- Git-mergeable (append-only reduces conflicts)
- Portable across systems
- Can be recovered from Git history
- **Recovery source**: `bd sync --import-only` rebuilds SQLite from JSONL

### Layer 3: SQLite Database

SQLite provides fast local queries without network latency. This is *derived state*—it can always be rebuilt from JSONL.

**Location:** `.beads/beads.db`

**Why SQLite?**
- Instant queries (no network)
- Complex filtering and sorting
- Derived from JSONL (always rebuildable)
- Safe to delete and rebuild: `rm .beads/beads.db* && bd sync --import-only`

## Data Flow

### Write Path
```text
User runs bd create
    → SQLite updated
    → JSONL appended
    → Git commit (on sync)
```

### Read Path
```text
User runs bd list
    → SQLite queried
    → Results returned immediately
```

### Sync Path
```text
User runs bd sync
    → Git pull
    → JSONL merged
    → SQLite rebuilt if needed
    → Git push
```

### Sync Modes

Beads provides specialized sync modes for different recovery scenarios:

#### Standard Sync
```bash
bd sync
```
Normal bidirectional sync: pulls remote changes, merges JSONL, rebuilds SQLite if needed, pushes local changes.

#### Import-Only Mode
```bash
bd sync --import-only
```
Rebuilds the SQLite database from JSONL without pushing changes. Use this when:
- SQLite is corrupted or missing
- Recovering from a fresh clone
- Rebuilding after database migration issues

This is the safest recovery option when JSONL is intact.

#### Force Rebuild Mode
```bash
bd sync --force-rebuild
```
Forces complete SQLite rebuild from JSONL, discarding any SQLite-only state. Use with caution:
- More aggressive than `--import-only`
- May lose any uncommitted database state
- Recommended when standard sync fails repeatedly

### Multi-Machine Sync Considerations

When working across multiple machines or clones:

1. **Always sync before switching machines**
   ```bash
   bd sync  # Push changes before leaving
   ```

2. **Pull before creating new issues**
   ```bash
   bd sync  # Pull changes first on new machine
   bd create "New issue"
   ```

3. **Avoid parallel edits** - If two machines create issues simultaneously without syncing, conflicts may occur

See [Sync Failures Recovery](/recovery/sync-failures) for data loss prevention in multi-machine workflows (Pattern A5/C3).

## Dolt Server Mode

The Dolt server handles background synchronization and database operations:

- Manages the Dolt database backend
- Handles auto-commit for change tracking
- Keeps SQLite in sync with JSONL
- Logs available at `.beads/dolt/sql-server.log`

:::tip
Start the Dolt server with `bd dolt start`. Check health with `bd doctor`.
:::

### Embedded Mode (No Server)

For CI/CD pipelines, containers, and single-use scenarios, no server is needed. Beads operates in embedded mode automatically when no Dolt server is running:

```bash
bd create "CI-generated issue"
bd sync
```

**When embedded mode is appropriate:**
- CI/CD pipelines (Jenkins, GitHub Actions)
- Docker containers
- Ephemeral environments
- Scripts that should not leave background processes

### Multi-Clone Scenarios

:::warning Race Conditions in Multi-Clone Workflows
When multiple git clones of the same repository run sync operations simultaneously, race conditions can occur during push/pull operations. This is particularly common in:
- Multi-agent AI workflows (multiple Claude/GPT instances)
- Developer workstations with multiple checkouts
- Worktree-based development workflows

**Prevention:**
1. Stop the Dolt server (`bd dolt stop`) before switching between clones
2. Dolt handles worktrees natively in server mode
3. Use embedded mode for automated workflows
:::

See [Sync Failures Recovery](/recovery/sync-failures) for sync race condition troubleshooting (Pattern B2).

## Recovery Model

The three-layer architecture makes recovery straightforward because each layer can rebuild from the one above it:

1. **Lost SQLite?** → Rebuild from JSONL: `bd sync --import-only`
2. **Lost JSONL?** → Recover from Git history: `git checkout HEAD~1 -- .beads/issues.jsonl`
3. **Conflicts?** → Git merge, then rebuild

### Universal Recovery Sequence

The following sequence demonstrates how the architecture enables quick recovery. For detailed procedures, see [Recovery Runbooks](/recovery).

This sequence resolves the majority of reported issues:

```bash
bd dolt stop                 # Stop Dolt server (prevents race conditions)
git worktree prune           # Clean orphaned worktrees
rm .beads/beads.db*          # Remove potentially corrupted database
bd sync --import-only        # Rebuild from JSONL source of truth
```

:::danger Never Use `bd doctor --fix`
Analysis of 54 GitHub issues revealed that `bd doctor --fix` frequently causes **more damage** than the original problem:

- Deletes "circular" dependencies that are actually valid parent-child relationships
- False positive detection removes legitimate issue links
- Recovery after `--fix` is harder than recovery from the original issue

**Safe alternatives:**
- `bd doctor` — Diagnostic only, no changes made
- `bd blocked` — Check which issues are blocked and why
- `bd show <issue-id>` — Inspect a specific issue's state

If `bd doctor` reports problems, investigate each one manually before taking any action.
:::

See [Recovery](/recovery) for specific procedures and [Database Corruption Recovery](/recovery/database-corruption) for `bd doctor --fix` recovery (Pattern D4).

## Design Decisions

### Why not just SQLite?

SQLite alone doesn't travel with Git or merge well across branches. Binary database files create merge conflicts that are nearly impossible to resolve.

### Why not just JSONL?

JSONL is slow for complex queries. Scanning thousands of lines for filtering and sorting is inefficient. SQLite provides indexed lookups in milliseconds.

### Why append-only JSONL?

Append-only format minimizes Git merge conflicts. When two branches add issues, Git can cleanly merge by concatenating the additions. Edit operations append new records rather than modifying existing lines.

### Why not a server?

Beads is designed for offline-first, local-first development. No server means no downtime, no latency, no vendor lock-in, and full functionality on airplanes or in restricted networks.

### Trade-offs

| Benefit | Trade-off |
|---------|-----------|
| Works offline | No real-time collaboration |
| Git-native history | Requires Git knowledge |
| No server dependency | No web UI or mobile app |
| Local-first speed | Manual sync required |
| Append-only merging | JSONL files grow over time |

### When NOT to use Beads

Beads is not suitable for:

- **Large teams (10+)** — Git-based sync doesn't scale well for high-frequency concurrent edits
- **Non-developers** — Requires Git and command-line familiarity
- **Real-time collaboration** — No live updates; requires explicit sync
- **Cross-repository tracking** — Issues are scoped to a single repository
- **Rich media attachments** — Designed for text-based issue tracking

For these use cases, consider GitHub Issues, Linear, or Jira.

## Related Documentation

- [Recovery Runbooks](/recovery) — Step-by-step procedures for common issues
- [CLI Reference](/cli-reference) — Complete command documentation
- [Getting Started](/) — Installation and first steps
