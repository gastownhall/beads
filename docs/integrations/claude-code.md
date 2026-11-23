---
title: Claude Code
description: Wire beads into Claude Code with a SessionStart hook that primes context, using the CLI instead of MCP
---

How to use beads with Claude Code.

## Setup

### Quick Setup

```bash
bd setup claude
```

This installs:
- **SessionStart hook** - Runs `bd prime --hook-json` when a session starts. SessionStart also fires after context compaction, so the same hook refreshes context automatically.
- **CLAUDE.md pointer** - A minimal beads section in your project's `CLAUDE.md`, or with `--global` in your user-level `~/.claude/CLAUDE.md`, where it applies only in repositories that use beads (skipped if that file is a symlink).

By default the hook is written to the project's `.claude/settings.json`. Variants:

```bash
bd setup claude --global   # Install to ~/.claude/settings.json and ~/.claude/CLAUDE.md instead
bd setup claude --stealth  # Stealth mode: flush only, no git operations
bd setup claude --remove   # Remove the hook and the CLAUDE.md section
```

If the [beads plugin](/integrations/claude-code-plugin) is enabled, `bd setup claude` skips writing hooks - the plugin provides its own, and duplicates would run `bd prime` twice per session. Like Claude Code, it takes the plugin's setting from the first settings file that names it: managed settings, `.claude/settings.local.json`, the project's `.claude/settings.json`, then `~/.claude/settings.json`, so `bd setup claude` run in a project that turns the plugin off writes the hook there, and a `--global` install run there warns that the project needs it. When only your own settings enable the plugin and the project's shared `.claude/settings.json` holds an older bare `bd prime` hook, the install replaces it with the current hook, which teammates without the plugin need, and warns that `bd prime` runs twice per session for you. When the project's shared `.claude/settings.json` enables the plugin and only your `.claude/settings.local.json` turns it off, no hook is written to the shared file, where teammates with the plugin would run `bd prime` twice; the install names the hook that already runs `bd prime` for you (a hook in `.claude/settings.local.json` is kept), and otherwise it and `--check` say how to run it for yourself. `--global` skips them only when managed settings or your user settings (`~/.claude/settings.json`) enable the plugin: a plugin enabled in a project's `.claude/settings.json` or `.claude/settings.local.json` runs in that project only. In such a project the global hook and the plugin's both run `bd prime`, and the install and `bd setup claude --check` warn that it runs twice per session there. To run it once, turn the plugin off for yourself in that project's `.claude/settings.local.json`, or enable it in `~/.claude/settings.json` and replace the global install: `bd setup claude --remove --global`, then `bd setup claude --global`.

Claude Code loads the `.claude/settings.local.json` at the repository root, or the main checkout's root in a linked worktree, in every session started in the repository. When that file already has a `bd prime` hook, such as the one an older `bd setup claude` wrote there, and the project's `.claude/settings.json` has none, `bd setup claude` run in a subdirectory or linked worktree adds no second hook and names the one that runs, and it leaves a hook in the current directory's own `.claude/settings.local.json` in place; `bd setup claude --remove` removes the hook from the root's file too. Claude Code runs an identical hook from several settings files once, but each different one, so the install and `--check` warn when two of the project's settings files run different `bd prime` commands, and name the hook to remove.

bd reads managed settings from the files in Claude Code's system directory (`managed-settings.json` and `managed-settings.d/` under `/etc/claude-code`, `/Library/Application Support/ClaudeCode` on macOS, or `C:\Program Files\ClaudeCode` on Windows) and assumes they are in force. It cannot see server-managed settings or an MDM profile. When one of those delivers a policy, Claude Code by default ignores the files, so a plugin they enable may not run, yet `bd setup claude` still leaves the hooks to it and drops an older bare `bd prime` hook from the shared `.claude/settings.json`.

### Manual Setup

Add to `.claude/settings.json` (project) or `~/.claude/settings.json` (global):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "bd prime --hook-json" }
        ]
      }
    ]
  }
}
```

The `--hook-json` flag wraps the output in the hook JSON envelope Claude Code expects. No `PreCompact` hook is needed - SessionStart fires again after compaction.

### Verify Setup

```bash
bd setup claude --check
```

## How It Works

1. **Session starts** → `bd prime` injects ~1-2k tokens of context
2. **You work** → Use `bd` CLI commands directly
3. **Session compacts** → SessionStart fires again and `bd prime` refreshes workflow context
4. **Session ends** → `bd dolt push` syncs changes

### Why CLI + hooks instead of MCP?

Context efficiency. MCP tool schemas can add 10-50k tokens to every request; `bd prime` adds ~1-2k tokens of workflow context - 10-50x less overhead, which means lower cost, lower latency, and better model attention. Prefer CLI + hooks in any environment with shell access; use the [MCP server](/integrations/mcp-server) only where the CLI is unavailable, such as Claude Desktop.

### Why not Claude Skills?

Beads doesn't ship or require Claude Skills (`.claude/skills/`). `bd prime` already delivers the workflow context, and the workflow fits a simple command set (ready → create → update → close → sync). Skills are also Claude-specific, which would break beads' editor-agnostic approach - the same CLI works in Cursor, Windsurf, and every other shell-capable editor. You can create your own Skills on top of beads, but none are needed.

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
bd dolt push
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

### Push Before Session End

```bash
# ALWAYS run before ending
bd dolt push
```

## Plugin (Optional)

For slash commands and enhanced UX, install the [beads plugin](/integrations/claude-code-plugin):

```bash
# In Claude Code
/plugin marketplace add gastownhall/beads
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
# Force push
bd dolt push

# Check system health
bd doctor
```

### Database not found

```bash
# Initialize beads
bd init --quiet
```

## See Also

- [Beads Claude Code Plugin](/integrations/claude-code-plugin) - Packaged plugin with slash commands
- [MCP Server](/integrations/mcp-server) - For MCP-only environments
- [IDE Setup](/getting-started/ide-setup) - Other editors
