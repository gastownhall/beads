# Linear Integration for bd

Bidirectional synchronization between Linear and bd (beads) using the built-in `bd linear` commands.

## Overview

The Linear integration provides:

- **Pull**: Import issues from Linear into bd
- **Push**: Export bd issues to Linear
- **Bidirectional Sync**: Two-way sync with conflict resolution
- **Incremental Sync**: Only sync issues changed since last sync
- **Configurable Mappings**: Customize priority, state, label, and relation mappings

## Quick Start

### 1. Get Linear Credentials

1. **API Key**: Go to Linear → Settings → API → Personal API keys → Create key
2. **Team ID**: Go to Linear → Settings → General → find the Team ID (UUID format)

### 2. Configure bd

```bash
# Set API key (or use LINEAR_API_KEY environment variable)
bd config set linear.api_key "lin_api_YOUR_API_KEY_HERE"

# Set team ID
bd config set linear.team_id "YOUR_TEAM_UUID"
```

### 3. Sync with Linear

```bash
# Check configuration status
bd linear status

# Pull issues from Linear
bd linear sync --pull

# Push local issues to Linear
bd linear sync --push

# Full bidirectional sync (pull, resolve conflicts, push)
bd linear sync
```

## Authentication

### API Key

Linear uses Personal API Keys for authentication. Create one at:
**Linear → Settings → API → Personal API keys**

Store securely:

```bash
# Option 1: bd config (stored in database)
bd config set linear.api_key "lin_api_..."

# Option 2: Environment variable
export LINEAR_API_KEY="lin_api_..."
```

### Team ID

Find your Team ID in Linear:
- **Settings → General** → Look for Team ID
- Or extract from URLs: `https://linear.app/YOUR_TEAM/...` → Go to team settings

## Sync Modes

### Pull Only (Linear → bd)

Import issues from Linear without pushing local changes:

```bash
bd linear sync --pull

# Filter by state
bd linear sync --pull --state open    # Only open issues
bd linear sync --pull --state closed  # Only closed issues
bd linear sync --pull --state all     # All issues (default)
```

### Push Only (bd → Linear)

Export local issues to Linear without pulling:

```bash
bd linear sync --push

# Create only (don't update existing Linear issues)
bd linear sync --push --create-only

# Disable automatic external_ref update
bd linear sync --push --update-refs=false
```

### Bidirectional Sync

Full two-way sync with conflict detection and resolution:

```bash
# Default: newer timestamp wins conflicts
bd linear sync

# Always prefer local version on conflicts
bd linear sync --prefer-local

# Always prefer Linear version on conflicts
bd linear sync --prefer-linear
```

### Dry Run

Preview what would happen without making changes:

```bash
bd linear sync --dry-run
```

## Data Mapping

### Priority Mapping

Linear and Beads use different priority semantics:

| Linear | Meaning | Beads | Meaning |
|--------|---------|-------|---------|
| 0 | No priority | 4 | Backlog |
| 1 | Urgent | 0 | Critical |
| 2 | High | 1 | High |
| 3 | Medium | 2 | Medium |
| 4 | Low | 3 | Low |

**Default mapping** (Linear → Beads):
- 0 (no priority) → 4 (backlog)
- 1 (urgent) → 0 (critical)
- 2 (high) → 1 (high)
- 3 (medium) → 2 (medium)
- 4 (low) → 3 (low)

**Custom mappings:**

```bash
# Override default mappings
bd config set linear.priority_map.0 2    # No priority -> Medium (instead of Backlog)
bd config set linear.priority_map.1 1    # Urgent -> High (instead of Critical)
```

### State Mapping

Map Linear workflow states to bd statuses:

| Linear State Type | Beads Status |
|-------------------|--------------|
| backlog | open |
| unstarted | open |
| started | in_progress |
| completed | closed |
| canceled | closed |

**Custom state mappings** (for custom workflow states):

```bash
# Map by state type
bd config set linear.state_map.started in_progress

# Map by state name (for custom workflow states)
bd config set linear.state_map.in_review in_progress
bd config set linear.state_map.blocked blocked
bd config set linear.state_map.on_hold blocked
bd config set linear.state_map.testing in_progress
bd config set linear.state_map.deployed closed
```

### Label to Issue Type

Infer bd issue type from Linear labels:

| Linear Label | Beads Type |
|--------------|------------|
| bug, defect | bug |
| feature, enhancement | feature |
| epic | epic |
| chore, maintenance | chore |
| task | task |

**Custom label mappings:**

```bash
bd config set linear.label_type_map.incident bug
bd config set linear.label_type_map.improvement feature
bd config set linear.label_type_map.tech_debt chore
bd config set linear.label_type_map.story feature
```

### Relation Mapping

Map Linear relations to bd dependencies:

| Linear Relation | Beads Dependency |
|-----------------|------------------|
| blocks | blocks |
| blockedBy | blocks (inverted) |
| duplicate | duplicates |
| related | related |
| (parent) | parent-child |

**Custom relation mappings:**

```bash
bd config set linear.relation_map.causes discovered-from
bd config set linear.relation_map.duplicate related
```

## Conflict Resolution

Conflicts occur when both local and Linear versions are modified since the last sync.

### Timestamp-based (Default)

The newer version wins:

```bash
bd linear sync  # Newer timestamp wins
```

### Prefer Local

Local bd version always wins:

```bash
bd linear sync --prefer-local
```

Use when:
- Local is your source of truth
- You've made deliberate changes locally

### Prefer Linear

Linear version always wins:

```bash
bd linear sync --prefer-linear
```

Use when:
- Linear is your source of truth
- You want to accept team changes

## Workflows

### Workflow 1: Initial Import from Linear

First-time import of existing Linear issues:

```bash
# Configure credentials
bd config set linear.api_key "lin_api_..."
bd config set linear.team_id "team-uuid"

# Check status
bd linear status

# Import all issues
bd linear sync --pull

# See what was imported
bd stats
bd list --json
```

### Workflow 2: Daily Sync

Regular synchronization:

```bash
# Pull latest from Linear (incremental since last sync)
bd linear sync --pull

# Do local work
bd update bd-123 --status in_progress
# ... work ...
bd close bd-123 --reason "Fixed"

# Push changes to Linear
bd linear sync --push

# Or do full bidirectional sync
bd linear sync
```

### Workflow 3: Create Local Issues, Push to Linear

Create issues locally and sync to Linear:

```bash
# Create issue locally
bd create "Fix authentication bug" -t bug -p 1

# Push to Linear (creates new Linear issue, updates external_ref)
bd linear sync --push

# Verify
bd show bd-abc  # Should have external_ref pointing to Linear
```

### Workflow 4: Migrate to bd

Full migration from Linear to bd:

```bash
# Import all issues
bd linear sync --pull --state all

# Preview import
bd stats

# Continue using bd locally, push updates back to Linear
bd linear sync  # Regular bidirectional sync
```

### Workflow 5: Read-Only Linear Mirror

Mirror Linear issues locally without pushing back:

```bash
# Only ever pull, never push
bd linear sync --pull

# Set up a cron job or alias
alias bd-mirror="bd linear sync --pull"
```

## Status & Debugging

### Check Sync Status

```bash
bd linear status
```

Shows:
- Configuration status (API key, team ID)
- Last sync timestamp
- Issues with Linear links
- Issues pending push (local only)

### JSON Output

```bash
bd linear status --json
bd linear sync --json
```

### Verbose Output

The sync command shows progress:
- Number of issues pulled/pushed
- Conflicts detected and resolved
- Errors and warnings

## Configuration Reference

All configuration keys for Linear integration:

```bash
# Required
linear.api_key          # Linear API key (or LINEAR_API_KEY env var)
linear.team_id          # Linear team UUID

# Automatic (set by bd)
linear.last_sync        # ISO8601 timestamp of last sync

# ID generation (optional)
linear.id_mode          # hash (default) or db (let bd generate IDs)
linear.hash_length      # Hash length 3-8 (default: 6)

# Priority mapping (Linear 0-4 to Beads 0-4)
linear.priority_map.0   # No priority -> ? (default: 4/backlog)
linear.priority_map.1   # Urgent -> ? (default: 0/critical)
linear.priority_map.2   # High -> ? (default: 1/high)
linear.priority_map.3   # Medium -> ? (default: 2/medium)
linear.priority_map.4   # Low -> ? (default: 3/low)

# State mapping (Linear state type/name to Beads status)
linear.state_map.backlog     # (default: open)
linear.state_map.unstarted   # (default: open)
linear.state_map.started     # (default: in_progress)
linear.state_map.completed   # (default: closed)
linear.state_map.canceled    # (default: closed)
linear.state_map.<custom>    # Map custom state names

# Label to issue type mapping
linear.label_type_map.bug         # (default: bug)
linear.label_type_map.defect      # (default: bug)
linear.label_type_map.feature     # (default: feature)
linear.label_type_map.enhancement # (default: feature)
linear.label_type_map.epic        # (default: epic)
linear.label_type_map.chore       # (default: chore)
linear.label_type_map.maintenance # (default: chore)
linear.label_type_map.task        # (default: task)
linear.label_type_map.<custom>    # Map custom labels

# Relation mapping (Linear relation type to Beads dependency type)
linear.relation_map.blocks    # (default: blocks)
linear.relation_map.blockedBy # (default: blocks)
linear.relation_map.duplicate # (default: duplicates)
linear.relation_map.related   # (default: related)
```

## Troubleshooting

### "Linear API key not configured"

Set the API key:

```bash
bd config set linear.api_key "lin_api_YOUR_KEY"
# Or
export LINEAR_API_KEY="lin_api_YOUR_KEY"
```

### "Linear team ID not configured"

Set the team ID:

```bash
bd config set linear.team_id "YOUR_TEAM_UUID"
```

### "GraphQL errors: Not authorized"

- Verify your API key is correct
- Check that the API key has access to the team
- Ensure the key hasn't been revoked

### "Rate limited"

Linear has API rate limits. The client automatically retries with exponential backoff:
- 3 retries with increasing delays
- If still failing, wait and retry later

### "Conflict detection failed"

- Check network connectivity
- Verify API key permissions
- Check `bd linear status` for configuration issues

### Sync seems slow

For large projects, initial sync fetches all issues. Subsequent syncs are incremental (only issues changed since `linear.last_sync`).

## Limitations

- **Single team**: Sync is configured per-team (one team_id per bd project)
- **No attachments**: Attachments are not synced
- **No comments**: Comments are not synced (only description)
- **Custom fields**: Linear custom fields are not mapped
- **Projects**: Linear projects are not mapped (use labels for categorization)
- **Cycles**: Linear cycles/sprints are not mapped

## See Also

- [CONFIG.md](../../docs/CONFIG.md) - Full configuration documentation
- [Jira Import Example](../jira-import/) - Similar integration for Jira
- [Linear GraphQL API](https://developers.linear.app/docs/graphql/working-with-the-graphql-api)

---

## Example Session

```bash
# Initial setup
$ bd init --quiet
$ bd config set linear.api_key "lin_api_abc123..."
$ bd config set linear.team_id "team-uuid-456"

# Check status
$ bd linear status
Linear Sync Status
==================

Team ID:      team-uuid-456
API Key:      lin_...c123
Last Sync:    Never

Total Issues: 0
With Linear:  0
Local Only:   0

# Pull from Linear
$ bd linear sync --pull
→ Pulling issues from Linear...
  Full sync (no previous sync timestamp)
✓ Pulled 47 issues (47 created, 0 updated)

✓ Linear sync complete

# Check what we got
$ bd stats
Issues: 47 (42 open, 5 closed)
Types:  23 task, 15 bug, 7 feature, 2 epic

# Create a local issue
$ bd create "New bug from testing" -t bug -p 1
Created: bd-a1b2c3

# Push to Linear
$ bd linear sync --push
→ Pushing issues to Linear...
  Created: bd-a1b2c3 -> TEAM-148
✓ Pushed 1 issues (1 created, 0 updated)

✓ Linear sync complete

# Full bidirectional sync
$ bd linear sync
→ Pulling issues from Linear...
  Incremental sync since 2025-01-17 10:30:00
✓ Pulled 3 issues (0 created, 3 updated)
→ Pushing issues to Linear...
✓ Pushed 2 issues (0 created, 2 updated)

✓ Linear sync complete
```
