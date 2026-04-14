# Cross-Project Inbox

The **inbox** feature enables cross-project issue delivery between beads projects that share a Dolt server. One project can send an issue to another project's inbox, and the receiving project decides when to import it.

## Overview

```
Project A                  Shared Dolt Server              Project B
┌──────────┐              ┌──────────────────┐            ┌──────────┐
│ bd handoff send   │ ──────────→ │  beads_inbox     │ ←───────── │ bd handoff inbox  │
│ bd-42 →   │   INSERT    │  (in Project B)  │   SELECT   │ import    │
│ project-b │              └──────────────────┘            └──────────┘
└──────────┘                                               bd ready
                                                           shows 📬
```

## Commands

### `bd handoff send` — Send an issue to another project

```bash
# Send issue bd-42 to project "api-service"
bd handoff send bd-42 --to api-service

# Send with an expiry (auto-removed if not imported)
bd handoff send bd-42 --to api-service --expires 7d

# JSON output for automation
bd handoff send bd-42 --to api-service --json
```

**Requirements:**
- Both projects must be on the same shared Dolt server
- The sender must know the target project's database name

**What gets sent:**
- Title, description, priority, type, status
- Labels and metadata (as JSON)
- Blocking dependency context (issue IDs the sent issue blocks)
- A sender reference link back to the original issue

### `bd handoff inbox` — Manage received issues

```bash
# List pending inbox items
bd handoff inbox
bd handoff inbox list

# Import all pending items as real issues
bd handoff inbox import

# Import a specific item
bd handoff inbox import <inbox-id>

# Reject an item
bd handoff inbox reject <inbox-id> "not relevant to this project"

# Clean up imported/rejected/expired items
bd handoff inbox clean
bd handoff inbox clean --dry-run
```

### `bd ready` — Automatic inbox notifications

When `bd ready` runs, it automatically checks for pending inbox items:

```
📬 3 issue(s) pending in inbox (use bd handoff inbox list to review, bd handoff inbox import to accept)
```

In `--json` mode, the response includes an `inbox_pending` count:

```json
{
  "ready": [...],
  "inbox_pending": 3
}
```

## How It Works

### Schema

The `beads_inbox` table stores incoming items:

| Column | Description |
|--------|-------------|
| `inbox_id` | UUID primary key |
| `sender_project_id` | Which project sent the issue |
| `sender_issue_id` | Original issue ID in the sender's project |
| `title`, `description` | Issue content |
| `priority`, `issue_type` | Issue metadata |
| `labels`, `metadata` | JSON columns for structured data |
| `sender_ref` | Link back to the original issue |
| `imported_issue_id` | Set when the item is imported locally |
| `rejection_reason` | Set when the item is rejected |
| `expires_at` | Optional TTL for auto-cleanup |

### Idempotent Resends

If the sender resends an issue (same `sender_project_id` + `sender_issue_id`), the existing inbox item is **updated** rather than duplicated. This allows senders to push updated descriptions or priorities without creating duplicates.

### Import Process

When you run `bd handoff inbox import`:

1. Each pending inbox item becomes a new local issue
2. The issue gets a new local ID (receiver-owned)
3. `imported_issue_id` is set on the inbox item for audit
4. `external_ref` on the new issue points back to the sender

### Cleanup

`bd handoff inbox clean` removes:
- Items that have been imported
- Items that have been rejected
- Items past their `expires_at` timestamp

## Configuration

The inbox feature currently requires **shared server mode** where multiple beads projects connect to the same Dolt SQL server. See [SYNC_SETUP.md](SYNC_SETUP.md) for server configuration.

> **Note:** Federation/remote topology support (sending across separate Dolt servers) is planned but not yet implemented. The current implementation uses cross-database SQL on a shared server.

## Example Workflow

```bash
# Project A: Found a bug that affects the API service
bd create "Auth token expiry not handled" -t bug -p 1 --json
# → created bd-abc

# Project A: Send it to the API service project
bd handoff send bd-abc --to api-service

# Project B: Check for work (sees inbox notification)
bd ready
# 📬 1 issue(s) pending in inbox (use bd handoff inbox list to review, bd handoff inbox import to accept)

# Project B: Review what was sent
bd handoff inbox list
#   abc12345  Auth token expiry not handled
#        From: project-a/bd-abc  P1 bug  2m ago

# Project B: Import it
bd handoff inbox import
# ✓ bd-abc → bd-xyz (Auth token expiry not handled)

# Project B: Clean up
bd handoff inbox clean
# ✓ Cleaned 1 inbox item(s)
```
