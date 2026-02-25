---
id: claude-code
title: Claude Code
sidebar_position: 1
---

# Claude Code Integration

How to use beads with Claude Code.

## Setup

### Quick Setup

```bash
bd setup claude
```

This installs:
- **SessionStart hook** - Runs `bd prime` on session start
- **PreCompact hook** - Runs `bd sync` before context compaction

### Manual Setup

Add to your Claude Code hooks configuration:

```json
{
  "hooks": {
    "SessionStart": ["bd prime"],
    "PreCompact": ["bd sync"]
  }
}
```

### Verify Setup

```bash
bd setup claude --check
```

## How It Works

1. **Session starts** → `bd prime` injects ~1-2k tokens of context
2. **You work** → Use `bd` CLI commands directly
3. **Session compacts** → `bd sync` saves work to git
4. **Session ends** → Changes synced via git

## Essential Commands for Agents

### Creating Issues

```bash
# Always include description for context
bd create "Fix authentication bug" \
  --description="Login fails with special characters in password" \
  -t bug -p 1 --json

# Link discovered issues
bd create "Found SQL injection" \
  --description="User input not sanitized in query builder" \
  --deps discovered-from:bd-42 --json
```

### Working on Issues

```bash
# Find ready work
bd ready --json

# Start work
bd update bd-42 --claim --json

# Complete work
bd close bd-42 --reason "Fixed in commit abc123" --json
```

### Querying

```bash
# List open issues
bd list --status open --json

# Show issue details
bd show bd-42 --json

# Check blocked issues
bd blocked --json
```

### Syncing

```bash
# ALWAYS run at session end
bd sync
```

## Best Practices

### Always Use `--json`

```bash
bd list --json          # Parse programmatically
bd create "Task" --json # Get issue ID from output
bd show bd-42 --json    # Structured data
```

### Always Include Descriptions

```bash
# Good
bd create "Fix auth bug" \
  --description="Login fails when password contains quotes" \
  -t bug -p 1 --json

# Bad - no context for future work
bd create "Fix auth bug" -t bug -p 1 --json
```

### Link Related Work

```bash
# When you discover issues during work
bd create "Found related bug" \
  --deps discovered-from:bd-current --json
```

### Sync Before Session End

```bash
# ALWAYS run before ending
bd sync
```

## Plugin (Optional)

For enhanced UX with slash commands:

```bash
# In Claude Code
/plugin marketplace add steveyegge/beads
/plugin install beads
# Restart Claude Code
```

Adds slash commands:
- `/beads:ready` - Show ready work
- `/beads:create` - Create issue
- `/beads:show` - Show issue
- `/beads:update` - Update issue
- `/beads:close` - Close issue

## Troubleshooting

### Context not injected

```bash
# Check hook setup
bd setup claude --check

# Manually prime
bd prime
```

### Changes not syncing

```bash
# Force sync
bd sync

# Check system health
bd doctor
```

### Database not found

```bash
# Initialize beads
bd init --quiet
```

## See Also

- [MCP Server](/integrations/mcp-server) - For MCP-only environments
- [IDE Setup](/getting-started/ide-setup) - Other editors
