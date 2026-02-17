# Architecture

This document describes bd's overall architecture - the data model, sync mechanism, and how components fit together. For internal implementation details (FlushManager, Blocked Cache), see [INTERNALS.md](INTERNALS.md).

## The Two-Layer Data Model

bd's core design enables a distributed, git-backed issue tracker that feels like a centralized database. The architecture has two synchronized layers:

```
┌─────────────────────────────────────────────────────────────────┐
│                        CLI Layer                                 │
│                                                                  │
│  bd create, list, update, close, ready, show, dep, sync, ...    │
│  - Cobra commands in cmd/bd/                                     │
│  - All commands support --json for programmatic use              │
│  - Direct DB access (embedded or server mode)                    │
└──────────────────────────────┬──────────────────────────────────┘
                               │
                               v
┌─────────────────────────────────────────────────────────────────┐
│                      Dolt Database                               │
│                      (.beads/dolt/)                               │
│                                                                  │
│  - Version-controlled SQL database with cell-level merge         │
│  - Two modes: embedded (single-process) or server (multi-writer) │
│  - Fast queries, indexes, foreign keys                           │
│  - Issues, dependencies, labels, comments, events                │
│  - Automatic Dolt commits on every write                         │
└──────────────────────────────┬──────────────────────────────────┘
                               │
                          JSONL export
                        (git hooks, portability)
                               │
                               v
┌─────────────────────────────────────────────────────────────────┐
│                       JSONL File                                 │
│                   (.beads/issues.jsonl)                          │
│                                                                  │
│  - Git-tracked for portability and distribution                  │
│  - One JSON line per entity (issue, dep, label, comment)         │
│  - Merge-friendly: additions rarely conflict                     │
│  - Shared across machines via git push/pull                      │
└──────────────────────────────┬──────────────────────────────────┘
                               │
                          git push/pull
                               │
                               v
┌─────────────────────────────────────────────────────────────────┐
│                     Remote Repository                            │
│                    (GitHub, GitLab, etc.)                        │
│                                                                  │
│  - Stores JSONL as part of normal repo history                   │
│  - All collaborators share the same issue database               │
│  - Protected branch support via separate sync branch             │
└─────────────────────────────────────────────────────────────────┘
```

### Why This Design?

**Dolt for versioned SQL:** Queries complete in milliseconds with full SQL support. Dolt adds native version control — every write is automatically committed to Dolt history, providing a complete audit trail.

**JSONL for git:** One entity per line means git diffs are readable and merges usually succeed automatically. JSONL is maintained for portability across machines via git.

**Git for distribution:** No special sync server needed. Issues travel with your code. Offline work just works.

## Write Path

When you create or modify an issue:

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   CLI Command   │───▶│   Dolt Write    │───▶│  Dolt Commit    │
│   (bd create)   │    │  (immediate)    │    │  (automatic)    │
└─────────────────┘    └─────────────────┘    └────────┬────────┘
                                                       │
                                                  git hooks
                                                       │
                                                       v
                       ┌─────────────────┐    ┌─────────────────┐
                       │   Git Commit    │◀───│  JSONL Export   │
                       │   (git hooks)   │    │  (incremental)  │
                       └─────────────────┘    └─────────────────┘
```

1. **Command executes:** `bd create "New feature"` writes to Dolt immediately
2. **Dolt commit:** Every write is automatically committed to Dolt history
3. **JSONL export:** Git hooks export changes to JSONL for portability
4. **Git commit:** If git hooks are installed, JSONL changes auto-commit

Key implementation:
- Export: `cmd/bd/export.go`
- Dolt storage: `internal/storage/dolt/`

## Read Path

When you query issues after a `git pull`:

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   git pull      │───▶│  Auto-Import    │───▶│  Dolt Update    │
│   (new JSONL)   │    │  (on next cmd)  │    │  (merge logic)  │
└─────────────────┘    └─────────────────┘    └────────┬────────┘
                                                       │
                                                       v
                                               ┌─────────────────┐
                                               │  CLI Query      │
                                               │  (bd ready)     │
                                               └─────────────────┘
```

1. **Git pull:** Fetches updated JSONL from remote
2. **Auto-import detection:** First bd command checks if JSONL is newer than DB
3. **Import to Dolt:** Parse JSONL, merge with local state using content hashes
4. **Query:** Commands read from fast local Dolt database

Key implementation:
- Import: `cmd/bd/import.go`
- Auto-import logic: `internal/autoimport/autoimport.go`

## Hash-Based Collision Prevention

The key insight that enables distributed operation: **content-based hashing for deduplication**.

### The Problem

Sequential IDs (bd-1, bd-2, bd-3) cause collisions when multiple agents create issues concurrently:

```
Branch A: bd create "Add OAuth"   → bd-10
Branch B: bd create "Add Stripe"  → bd-10 (collision!)
```

### The Solution

Hash-based IDs derived from random UUIDs ensure uniqueness:

```
Branch A: bd create "Add OAuth"   → bd-a1b2
Branch B: bd create "Add Stripe"  → bd-f14c (no collision)
```

### How It Works

1. **Issue creation:** Generate random UUID, derive short hash as ID
2. **Progressive scaling:** IDs start at 4 chars, grow to 5-6 chars as database grows
3. **Content hashing:** Each issue has a content hash for change detection
4. **Import merge:** Same ID + different content = update, same ID + same content = skip

```
┌─────────────────────────────────────────────────────────────────┐
│                        Import Logic                              │
│                                                                  │
│  For each issue in JSONL:                                       │
│    1. Compute content hash                                       │
│    2. Look up existing issue by ID                               │
│    3. Compare hashes:                                            │
│       - Same hash → skip (already imported)                      │
│       - Different hash → update (newer version)                  │
│       - No match → create (new issue)                            │
└─────────────────────────────────────────────────────────────────┘
```

This eliminates the need for central coordination while ensuring all machines converge to the same state.

See [COLLISION_MATH.md](COLLISION_MATH.md) for birthday paradox calculations on hash length vs collision probability.

## Daemon Architecture

Each workspace runs its own background daemon for auto-sync:

```
┌─────────────────────────────────────────────────────────────────┐
│                       RPC Server                                 │
│                                                                  │
│  ┌─────────────┐    ┌─────────────┐                             │
│  │ RPC Server  │    │  Background │                             │
│  │ (bd.sock)   │    │  Tasks      │                             │
│  └─────────────┘    └─────────────┘                             │
│         │                  │                                     │
│         └──────────────────┘                                     │
│                            │                                     │
│                            v                                     │
│                   ┌─────────────┐                                │
│                   │    Dolt     │                                │
│                   │   Database  │                                │
│                   └─────────────┘                                │
└─────────────────────────────────────────────────────────────────┘

     CLI commands ───RPC───▶ Server ───SQL───▶ Database
                              or
     CLI commands ───SQL───▶ Database (embedded mode)
```

**Two modes:**
- **Embedded:** In-process Dolt database (single-process, no server needed)
- **Server:** Connect to external `dolt sql-server` (multi-writer, high-concurrency)

**Communication (server mode):**
- Unix domain socket at `.beads/bd.sock` (Windows: named pipes)
- Protocol defined in `internal/rpc/protocol.go`
- Used by Dolt server mode for multi-writer access

## Data Types

Core types in `internal/types/types.go`:

| Type | Description | Key Fields |
|------|-------------|------------|
| **Issue** | Work item | ID, Title, Description, Status, Priority, Type |
| **Dependency** | Relationship | FromID, ToID, Type (blocks/related/parent-child/discovered-from) |
| **Label** | Tag | Name, Color, Description |
| **Comment** | Discussion | IssueID, Author, Content, Timestamp |
| **Event** | Audit trail | IssueID, Type, Data, Timestamp |

### Dependency Types

| Type | Semantic | Affects `bd ready`? |
|------|----------|---------------------|
| `blocks` | Issue X must close before Y starts | Yes |
| `parent-child` | Hierarchical (epic/subtask) | Yes (children blocked if parent blocked) |
| `related` | Soft link for reference | No |
| `discovered-from` | Found during work on parent | No |

### Status Flow

```
open ──▶ in_progress ──▶ closed
  │                        │
  └────────────────────────┘
         (reopen)
```

### JSONL Issue Schema

Each issue in `.beads/issues.jsonl` is a JSON object with the following fields. Fields marked with `(optional)` use `omitempty` and are excluded when empty/zero.

**Core Identification:**

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Unique identifier (e.g., `bd-a1b2`) |

**Issue Content:**

| Field | Type | Description |
|-------|------|-------------|
| `title` | string | Issue title (required) |
| `description` | string | Detailed description (optional) |
| `design` | string | Design notes (optional) |
| `acceptance_criteria` | string | Acceptance criteria (optional) |
| `notes` | string | Additional notes (optional) |

**Status & Workflow:**

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | Current status: `open`, `in_progress`, `blocked`, `deferred`, `closed`, `tombstone`, `pinned`, `hooked` (optional, defaults to `open`) |
| `priority` | int | Priority 0-4 where 0=critical, 4=backlog |
| `issue_type` | string | Type: `bug`, `feature`, `task`, `epic`, `chore`, `message`, `merge-request`, `molecule`, `gate`, `agent`, `role`, `convoy` (optional, defaults to `task`) |

**Assignment:**

| Field | Type | Description |
|-------|------|-------------|
| `assignee` | string | Assigned user/agent (optional) |
| `estimated_minutes` | int | Time estimate in minutes (optional) |

**Timestamps:**

| Field | Type | Description |
|-------|------|-------------|
| `created_at` | RFC3339 | When issue was created |
| `created_by` | string | Who created the issue (optional) |
| `updated_at` | RFC3339 | Last modification time |
| `closed_at` | RFC3339 | When issue was closed (optional, set when status=closed) |
| `close_reason` | string | Reason provided when closing (optional) |

**External Integration:**

| Field | Type | Description |
|-------|------|-------------|
| `external_ref` | string | External reference (e.g., `gh-9`, `jira-ABC`) (optional) |

**Relational Data:**

| Field | Type | Description |
|-------|------|-------------|
| `labels` | []string | Tags attached to the issue (optional) |
| `dependencies` | []Dependency | Relationships to other issues (optional) |
| `comments` | []Comment | Discussion comments (optional) |

**Tombstone Fields (soft-delete):**

| Field | Type | Description |
|-------|------|-------------|
| `deleted_at` | RFC3339 | When deleted (optional, set when status=tombstone) |
| `deleted_by` | string | Who deleted (optional) |
| `delete_reason` | string | Why deleted (optional) |
| `original_type` | string | Issue type before deletion (optional) |

**Note:** Fields with `json:"-"` tags (like `content_hash`, `source_repo`, `id_prefix`) are internal and never exported to JSONL.

## Directory Structure

```
.beads/
├── dolt/             # Dolt database directory (gitignored)
├── issues.jsonl      # JSONL export (git-tracked, for portability)
├── metadata.json     # Backend config (local, gitignored)
├── config.yaml       # Project config (optional)
└── bd.sock           # RPC socket (gitignored, server mode only)
```

## Key Code Paths

| Area | Files |
|------|-------|
| CLI entry | `cmd/bd/main.go` |
| Storage interface | `internal/storage/storage.go` |
| Dolt implementation | `internal/storage/dolt/` |
| RPC protocol | `internal/rpc/protocol.go`, `server_*.go` |
| Export logic | `cmd/bd/export.go` |
| Import logic | `cmd/bd/import.go` |

## Wisps and Molecules

**Molecules** are template work items that define structured workflows. When spawned, they create **wisps** - ephemeral child issues that track execution steps.

> **For full documentation** on the molecular chemistry metaphor (protos, pour, bond, squash, burn), see [MOLECULES.md](MOLECULES.md).

### Wisp Lifecycle

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   bd mol wisp       │───▶│  Wisp Issues    │───▶│  bd mol squash  │
│ (from template) │    │  (local-only)   │    │  (→ digest)     │
└─────────────────┘    └─────────────────┘    └─────────────────┘
```

1. **Create:** Create wisps from a molecule template
2. **Execute:** Agent works through wisp steps (local database only)
3. **Squash:** Compress wisps into a permanent digest issue

### Why Wisps Don't Sync

Wisps are intentionally **local-only**:

- They exist only in the spawning agent's local database
- They are **never exported to JSONL**
- They cannot resurrect from other clones (they were never there)
- They are **hard-deleted** when squashed (no tombstones needed)

This design enables:

- **Fast local iteration:** No sync overhead during execution
- **Clean history:** Only the digest (outcome) enters git
- **Agent isolation:** Each agent's execution trace is private
- **Bounded storage:** Wisps don't accumulate across clones

### Wisp vs Regular Issue Deletion

| Aspect | Regular Issues | Wisps |
|--------|---------------|-------|
| Exported to JSONL | Yes | No |
| Tombstone on delete | Yes | No |
| Can resurrect | Yes (without tombstone) | No (never synced) |
| Deletion method | `CreateTombstone()` | `DeleteIssue()` (hard delete) |

The `bd mol squash` command uses hard delete intentionally - tombstones would be wasted overhead for data that never leaves the local database.

### Future Directions

- **Separate wisp repo:** Keep wisps in a dedicated ephemeral git repo
- **Digest migration:** Explicit step to promote digests to main repo
- **Wisp retention:** Option to preserve wisps in local git history

## Related Documentation

- [MOLECULES.md](MOLECULES.md) - Molecular chemistry metaphor (protos, pour, bond, squash, burn)
- [INTERNALS.md](INTERNALS.md) - FlushManager, Blocked Cache implementation details
- [ADVANCED.md](ADVANCED.md) - Advanced features and configuration
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) - Recovery procedures and common issues
- [FAQ.md](FAQ.md) - Common questions about the architecture
- [COLLISION_MATH.md](COLLISION_MATH.md) - Hash collision probability analysis
