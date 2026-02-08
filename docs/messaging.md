# Messaging in Beads

Beads supports messaging as a first-class issue type, enabling inter-agent and human-agent communication within the same system used for issue tracking.

## Architecture

Mail commands (`bd mail`) delegate to an external mail provider (typically `gt mail` in Gas Town). Beads stores messages as issues with `type: message`, threading via `replies_to` dependencies, and ephemeral lifecycle via the `ephemeral` flag.

This design separates concerns:
- **Beads** = data plane (stores messages as issues)
- **Orchestrator** = control plane (routing, delivery, notifications)

## Setup

Configure the mail delegate (one-time):

```bash
# Environment variable (recommended for agents)
export BEADS_MAIL_DELEGATE="gt mail"

# Or per-project config
bd config set mail.delegate "gt mail"
```

## Sending and Receiving

```bash
# Send mail (delegates to gt mail)
bd mail send worker/ -s "Review needed" -m "Please review bd-abc"

# Check inbox
bd mail inbox

# Read a message
bd mail read msg-123

# Reply to a thread
bd mail reply msg-123 -m "Reviewed and approved"
```

## Message Issue Type

Messages are issues with `type: message`:

| Field | Purpose |
|-------|---------|
| `type` | `message` |
| `sender` | Who sent the message |
| `assignee` | Recipient |
| `title` | Subject line |
| `description` | Message body |
| `status` | `open` (unread) / `closed` (read) |
| `ephemeral` | If true, eligible for bulk cleanup |

## Threading

Messages form threads via `replies_to` dependencies. View a full thread:

```bash
bd show msg-123 --thread
```

This traces the `replies_to` chain to find the root message, then collects all replies via BFS, displaying the conversation with proper indentation.

Thread display shows:
- Sender and recipient
- Timestamp
- Subject and body
- Reply depth (indented)

## Ephemeral Messages

Messages marked `ephemeral: true` are transient - they can be bulk-deleted after a swarm completes:

```bash
# Clean up closed ephemeral messages
bd cleanup --ephemeral --force

# Preview what would be deleted
bd cleanup --ephemeral --dry-run

# Only delete ephemeral messages older than 7 days
bd cleanup --ephemeral --older-than 7 --force
```

Ephemeral messages are:
- Excluded from `bd ready` by default
- Not exported to JSONL (transient)
- Eligible for bulk deletion when closed

## Identity

The actor identity (used for `sender` on messages) is resolved in order:

1. `--actor` flag on the command
2. `BD_ACTOR` environment variable
3. `BEADS_ACTOR` environment variable
4. `git config user.name`
5. `$USER` environment variable
6. `"unknown"`

## Beads Event Hooks

Scripts in `.beads/hooks/` run after certain events:

| Hook | Trigger |
|------|---------|
| `on_create` | After `bd create` |
| `on_update` | After `bd update` |
| `on_close` | After `bd close` |

Hooks receive event data as JSON on stdin. This enables orchestrator integration (e.g., notifying daemons of new messages) without beads knowing about the orchestrator.

## See Also

- [Graph Links](graph-links.md) - relates_to, duplicates, supersedes, replies_to
- [CLI Reference](CLI_REFERENCE.md) - All commands
