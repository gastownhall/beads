# Cross-Project Handoff

The **handoff** feature enables selective issue transfer between beads projects on a shared Dolt server. One project sends an issue to another project's inbox; the receiver decides whether to import or reject it.

## Quick Start

```bash
# Configure a named target (one-time setup)
bd config set handoff.target.api-service api_service_beads

# Send an issue to another project
bd handoff send bd-42 --to api-service

# On the receiving side: review and import
bd handoff inbox list
bd handoff inbox import
```

## Architecture

```
Project A                  Shared Dolt Server              Project B
┌──────────┐              ┌──────────────────┐            ┌──────────┐
│ bd handoff│ ──────────→ │  beads_inbox     │ ←───────── │ bd handoff│
│   send    │   INSERT    │  (in Project B)  │   SELECT   │   inbox   │
└──────────┘              └──────────────────┘            └──────────┘
```

The handoff system has three layers:

1. **InboxStore** — local CRUD for the `beads_inbox` table (add, get, list, mark, clean)
2. **InboxTransport** — delivery mechanism (currently: shared-server cross-DB SQL)
3. **Authorization** — config-based send/accept policies

This separation means future transport backends (federation, remote push) can be added without changing the inbox storage or CLI.

## Commands

### `bd handoff send` — Send an issue

```bash
bd handoff send <issue-id> --to <project-name> [--expires <duration>] [--json]
```

Sends a snapshot of the issue (title, description, priority, type, labels, dependency context) to the target project's inbox. The target is resolved via config:

```bash
bd config set handoff.target.frontend frontend_beads
bd handoff send bd-42 --to frontend
```

### `bd handoff inbox` — Manage received issues

```bash
bd handoff inbox [list]              # List pending inbox items
bd handoff inbox import [inbox-id]   # Import pending items as local issues
bd handoff inbox reject <id> "reason" # Reject an item
bd handoff inbox clean [--dry-run]   # Remove imported/rejected/expired items
```

### `bd ready` — Inbox notifications

`bd ready` automatically shows pending handoff items:

```
📬 3 handoff(s) pending (bd handoff inbox list to review)
```

JSON output includes `handoff_pending` count.

## Authorization

Handoff uses config-based allow-lists stored in the beads database (not local files).

### Outbound policy (sender side)

Controls which projects you can send to:

```bash
# Allow sending to specific projects
bd config set handoff.allow_send_to "api-service,frontend"

# Allow sending to any project
bd config set handoff.allow_send_to "*"
```

If `handoff.allow_send_to` is not configured, **all sends are denied** (secure by default).

### Inbound policy (receiver side)

Controls which projects can send to your inbox:

```bash
# Accept from specific senders
bd config set handoff.accept_from "mobile-app,backend"

# Accept from anyone (default behavior for backward compatibility)
bd config set handoff.accept_from "*"
```

If `handoff.accept_from` is not configured, **all senders are accepted** (backward compatible).

### Sender identity

On a shared Dolt server, sender identity is derived from the source database name — it is transport-authenticated, not self-reported. This means a project cannot spoof its identity when sending via shared-server transport.

> **Note:** This trust model is scoped to shared-server deployments where database access implies identity. Future federation transports will need cryptographic identity (e.g., Ed25519 signing).

### Error handling

Authorization errors intentionally return generic messages ("send failed") rather than distinguishing "project not found" from "not authorized". This prevents enumeration of project names.

## Idempotent Resends

If the sender resends an issue (same `sender_project_id` + `sender_issue_id`):

- **Pending item**: Content is updated in-place (ON DUPLICATE KEY UPDATE)
- **Rejected item**: Rejection is cleared, item returns to pending
- **Imported item**: No change (already accepted)

This allows senders to push updated descriptions or priorities without creating duplicates, and to retry after a rejection with updated content.

## Import Safety

The import process (`bd handoff inbox import`) uses optimistic locking to prevent race conditions:

1. Each inbox item is checked for prior import (dedup via `external_ref`)
2. `MarkInboxItemImported` only succeeds if `imported_at IS NULL`
3. If a concurrent import claims the item first, the second import skips gracefully

This ensures that even if two users run `bd handoff inbox import` simultaneously, each inbox item produces exactly one local issue.

## Cross-Project Model Taxonomy

Beads supports multiple patterns for cross-project coordination. Choose the right one:

| Pattern | Use Case | Coupling | Scope |
|---------|----------|----------|-------|
| **Parent repo** | Tightly coupled projects, unified dependency graph | High | One Dolt DB |
| **`bd ship`** | Abstract cross-project blocking (external deps) | Low | Any topology |
| **`bd handoff`** | Selective issue transfer between isolated projects | Medium | Shared server |
| **Federation** | Full bidirectional sync between autonomous projects | Low | Distributed |

### When to use handoff

- You have separate beads projects on the same Dolt server
- One team discovers work that belongs to another team
- You want the receiver to decide whether to accept the work
- You need audit trail of what was sent and when

### When NOT to use handoff

- Projects share a single beads database → just create issues directly
- You only need to track "we're blocked on team X" → use `bd ship`
- You need real-time bidirectional sync → wait for federation

## Configuration Reference

| Config Key | Description | Default |
|-----------|-------------|---------|
| `handoff.target.<name>` | Maps project name to database name | (none) |
| `handoff.allow_send_to` | Comma-separated project names (or `*`) | (none — denies all) |
| `handoff.accept_from` | Comma-separated project names (or `*`) | (none — accepts all) |

## Schema

The `beads_inbox` table:

| Column | Description |
|--------|-------------|
| `inbox_id` | UUID primary key |
| `sender_project_id` | Source project identifier |
| `sender_issue_id` | Original issue ID in sender's project |
| `title`, `description` | Issue content snapshot |
| `priority`, `issue_type` | Issue metadata |
| `labels`, `metadata` | JSON columns |
| `sender_ref` | Link back to original issue |
| `imported_at`, `imported_issue_id` | Set on import |
| `rejected_at`, `rejection_reason` | Set on rejection |
| `expires_at` | Optional TTL |

## Requirements

- Both projects must be on the same shared Dolt server
- Sender must have a configured target mapping (`handoff.target.<name>`)
- Sender must be allowed by outbound policy (`handoff.allow_send_to`)
