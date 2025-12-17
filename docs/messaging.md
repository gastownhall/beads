# Beads Messaging System

Beads provides a built-in messaging system for inter-agent communication. Messages are stored as beads issues with type `message`, enabling git-native communication without external services.

## Overview

The messaging system enables:
- **Agent-to-agent communication** - Send messages between workers
- **Thread tracking** - Replies link back to original messages
- **Priority signaling** - Mark messages as urgent (P0) or routine
- **Ephemeral cleanup** - Messages can be bulk-deleted after completion

## Identity Configuration

Before using mail commands, configure your identity:

### Environment Variable

```bash
export BEADS_IDENTITY="worker-1"
```

### Config File

Add to `.beads/config.json`:

```json
{
  "identity": "worker-1"
}
```

### Priority

1. `--identity` flag (if provided)
2. `BEADS_IDENTITY` environment variable
3. `.beads/config.json` identity field
4. System username (fallback)

## Commands

### Send a Message

```bash
bd mail send <recipient> -s <subject> -m <body>
```

**Options:**
- `-s, --subject` - Message subject (required)
- `-m, --body` - Message body (required)
- `--urgent` - Set priority=0 (urgent)
- `--identity` - Override sender identity

**Examples:**

```bash
# Basic message
bd mail send worker-1 -s "Task complete" -m "Finished bd-xyz"

# Urgent message
bd mail send manager -s "Blocked!" -m "Need credentials for deploy" --urgent

# With custom identity
bd mail send worker-2 -s "Handoff" -m "Your turn on bd-abc" --identity refinery
```

### Check Your Inbox

```bash
bd mail inbox
```

Lists all open messages addressed to your identity.

**Options:**
- `--from <sender>` - Filter by sender
- `--priority <n>` - Filter by priority (0-4)

**Output:**

```
Inbox for worker-1 (2 messages):

  bd-a1b2: Task assigned [URGENT]
      From: manager (5m ago)

  bd-c3d4: FYI: Design doc updated
      From: worker-2 (1h ago)
      Re: bd-x7y8
```

### Read a Message

```bash
bd mail read <id>
```

Displays full message content. Does NOT mark as read.

**Example output:**

```
──────────────────────────────────────────────────────────────────
ID:      bd-a1b2
From:    manager
To:      worker-1
Subject: Task assigned
Time:    2025-12-16 10:30:45
Priority: P0
Status:  open
──────────────────────────────────────────────────────────────────

Please prioritize bd-xyz. It's blocking the release.

Let me know if you need anything.
```

### Acknowledge (Mark as Read)

```bash
bd mail ack <id> [id2...]
```

Closes messages to mark them as acknowledged.

**Examples:**

```bash
# Single message
bd mail ack bd-a1b2

# Multiple messages
bd mail ack bd-a1b2 bd-c3d4 bd-e5f6
```

### Reply to a Message

```bash
bd mail reply <id> -m <body>
```

Creates a threaded reply to an existing message.

**Options:**
- `-m, --body` - Reply body (required)
- `--urgent` - Set priority=0
- `--identity` - Override sender identity

**Behavior:**
- Sets `replies_to` to original message ID
- Sends to original message's sender
- Prefixes subject with "Re:" if not already present

**Example:**

```bash
bd mail reply bd-a1b2 -m "On it! Should be done by EOD."
```

## Message Storage

Messages are stored as issues with these fields:

| Field | Description |
|-------|-------------|
| `type` | `message` |
| `title` | Subject line |
| `description` | Message body |
| `assignee` | Recipient identity |
| `sender` | Sender identity |
| `priority` | 0 (urgent) to 4 (routine), default 2 |
| `ephemeral` | `true` - can be bulk-deleted |
| `replies_to` | ID of parent message (for threads) |
| `status` | `open` (unread) / `closed` (read) |

## Cleanup

Messages are ephemeral by default and can be cleaned up:

```bash
# Preview ephemeral message cleanup
bd cleanup --ephemeral --dry-run

# Delete all closed ephemeral messages
bd cleanup --ephemeral --force
```

## Hooks

The messaging system fires hooks when messages are sent:

**Hook file:** `.beads/hooks/on_message`

The hook receives:
- **Arg 1:** Issue ID
- **Arg 2:** Event type (`message`)
- **Stdin:** Full issue JSON

**Example hook:**

```bash
#!/bin/sh
# .beads/hooks/on_message

ISSUE_ID="$1"
EVENT="$2"

# Parse assignee from JSON stdin
ASSIGNEE=$(cat | jq -r '.assignee')

# Notify recipient (example: send to external system)
curl -X POST "https://example.com/notify" \
  -d "to=$ASSIGNEE&message=$ISSUE_ID"
```

Make the hook executable:

```bash
chmod +x .beads/hooks/on_message
```

## JSON Output

All commands support `--json` for programmatic use:

```bash
bd mail inbox --json
bd mail read bd-a1b2 --json
bd mail send worker-1 -s "Hi" -m "Test" --json
```

## Thread Visualization

Use `bd show --thread` to view message threads:

```bash
bd show bd-c3d4 --thread
```

This shows the full conversation chain via `replies_to` links.

## Best Practices

1. **Use descriptive subjects** - Recipients scan subjects first
2. **Mark urgent sparingly** - P0 should be reserved for blockers
3. **Acknowledge promptly** - Keep inbox clean
4. **Clean up after sprints** - Run `bd cleanup --ephemeral` periodically
5. **Configure identity** - Use `BEADS_IDENTITY` for consistent sender names

## See Also

- [Graph Links](graph-links.md) - Other link types (relates_to, duplicates, supersedes)
- [Hooks](EXTENDING.md) - Custom hook scripts
- [Config](CONFIG.md) - Configuration options
