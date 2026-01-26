# Setup Command Reference

**For:** Setting up beads integration with AI coding tools
**Version:** 0.30.0+

## Overview

The `bd setup` command uses a **recipe-based architecture** to configure beads integration with AI coding tools. Recipes define where workflow instructions are written—built-in recipes handle popular tools, and you can add custom recipes for any tool.

### Built-in Recipes

| Recipe | Path | Integration Type |
|--------|------|-----------------|
| `cursor` | `.cursor/rules/beads.mdc` | Rules file |
| `windsurf` | `.windsurf/rules/beads.md` | Rules file |
| `cody` | `.cody/rules/beads.md` | Rules file |
| `kilocode` | `.kilocode/rules/beads.md` | Rules file |
| `claude` | `~/.claude/settings.json` | SessionStart/PreCompact hooks |
| `gemini` | `~/.gemini/settings.json` | SessionStart/PreCompress hooks |
| `factory` | `AGENTS.md` | Marked section |
| `codex` | `AGENTS.md` | Marked section |
| `aider` | `.aider.conf.yml` + `.aider/` | Multi-file config |

## Quick Start

```bash
# List all available recipes
bd setup --list

# Install integration for your tool
bd setup cursor     # Cursor IDE
bd setup windsurf   # Windsurf
bd setup kilocode   # Kilo Code
bd setup claude     # Claude Code
bd setup gemini     # Gemini CLI
bd setup factory    # Factory.ai Droid
bd setup codex      # Codex CLI
bd setup aider      # Aider

# Verify installation
bd setup cursor --check
bd setup claude --check

# Print template to stdout (for inspection)
bd setup --print

# Write template to custom path
bd setup -o .my-editor/rules.md

# Add a custom recipe
bd setup --add myeditor .myeditor/rules.md
bd setup myeditor  # Now you can use it
```

## Factory.ai (Droid)

Factory.ai Droid integration uses the AGENTS.md standard, which is compatible with multiple AI coding assistants.

### Installation

```bash
# Create or update AGENTS.md with beads integration
bd setup factory
```

### What Gets Installed

Creates or updates `AGENTS.md` in your project root with:
- Issue tracking workflow instructions
- Quick command reference
- Issue types and priorities
- Auto-sync explanation
- Important rules for AI agents

The beads section is wrapped in HTML comments (`<!-- BEGIN/END BEADS INTEGRATION -->`) for safe updates.

### AGENTS.md Standard

AGENTS.md is an industry-standard format for AI coding agent instructions, supported by:
- **Factory.ai Droid** - Specialized coding agents
- **Cursor** - Also reads AGENTS.md (in addition to .cursor/rules)
- **Aider** - Can be configured to read AGENTS.md
- **Gemini CLI** - Google's command-line AI assistant
- **Jules** - Google's coding assistant
- **Codex** - OpenAI's code generation model
- **Zed** - AI-enhanced editor
- And many more emerging tools

Using AGENTS.md means one configuration file works across your entire AI tool ecosystem.

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check if beads section exists in AGENTS.md |
| `--remove` | Remove beads section from AGENTS.md |

### Examples

```bash
# Check if beads section is in AGENTS.md
bd setup factory --check
# Output: ✓ Factory.ai integration installed: AGENTS.md
#         Beads section found in AGENTS.md

# Remove beads section
bd setup factory --remove
```

### How It Works

Factory Droid and other AGENTS.md-compatible tools automatically read `AGENTS.md` from:
1. Current working directory (`./AGENTS.md`)
2. Parent directories up to repo root
3. Personal override (`~/.factory/AGENTS.md`)

The beads section teaches AI agents:
- To use `bd ready` for finding work
- To use `bd create` for tracking new issues
- To use `bd sync` at session end
- The complete workflow pattern and best practices

### Updating Existing AGENTS.md

If you already have an AGENTS.md file with other project instructions:
- `bd setup factory` will **append** the beads section
- Re-running it will **update** the existing beads section (idempotent)
- The markers (`<!-- BEGIN/END BEADS INTEGRATION -->`) ensure safe updates

### When to Use This vs Other Integrations

**Use Factory integration when:**
- ✅ You use Factory.ai Droid
- ✅ You want one config file for multiple AI tools
- ✅ You prefer the AGENTS.md standard
- ✅ Your team uses multiple AI coding assistants

**Use other integrations when:**
- ✅ You only use Claude Code → `bd setup claude` (hooks are more dynamic)
- ✅ You need tool-specific features (like Claude's stealth mode)

You can use multiple integrations simultaneously - they complement each other!

## Codex CLI

Codex reads `AGENTS.md` instructions at the start of each run/session. Adding the beads section is enough to get Codex and beads working together.

### Installation

```bash
bd setup codex
```

### What Gets Installed

Creates or updates `AGENTS.md` with the beads integration section (same markers as Factory.ai).

### Notes

- Restart Codex if it's already running to pick up the new instructions.

## Claude Code

Claude Code integration uses hooks to automatically inject beads workflow context at session start and before context compaction.

### Installation

```bash
# Global installation (recommended)
bd setup claude

# Project-only installation
bd setup claude --project

# With stealth mode (flush only, no git operations)
bd setup claude --stealth
```

### What Gets Installed

**Global installation** (`~/.claude/settings.json`):
- `SessionStart` hook: Runs `bd prime` when a new session starts
- `PreCompact` hook: Runs `bd prime` before context compaction

**Project installation** (`.claude/settings.local.json`):
- Same hooks, but only active for this project

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check if integration is installed |
| `--remove` | Remove beads hooks |
| `--project` | Install for this project only (not globally) |
| `--stealth` | Use `bd prime --stealth` (flush only, no git operations) |

### Examples

```bash
# Check if hooks are installed
bd setup claude --check
# Output: ✓ Global hooks installed: /Users/you/.claude/settings.json

# Remove hooks
bd setup claude --remove

# Install project-specific hooks with stealth mode
bd setup claude --project --stealth
```

### How It Works

The hooks call `bd prime` which:
1. Outputs workflow context for Claude to read
2. Syncs any pending changes
3. Ensures Claude always knows how to use beads

This is more context-efficient than MCP tools (~1-2k tokens vs 10-50k for MCP schemas).

## Gemini CLI

Gemini CLI integration uses hooks to automatically inject beads workflow context at session start and before context compression.

### Installation

```bash
# Global installation (recommended)
bd setup gemini

# Project-only installation
bd setup gemini --project

# With stealth mode (flush only, no git operations)
bd setup gemini --stealth
```

### What Gets Installed

**Global installation** (`~/.gemini/settings.json`):
- `SessionStart` hook: Runs `bd prime` when a new session starts
- `PreCompress` hook: Runs `bd prime` before context compression

**Project installation** (`.gemini/settings.json`):
- Same hooks, but only active for this project

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check if integration is installed |
| `--remove` | Remove beads hooks |
| `--project` | Install for this project only (not globally) |
| `--stealth` | Use `bd prime --stealth` (flush only, no git operations) |

### Examples

```bash
# Check if hooks are installed
bd setup gemini --check
# Output: ✓ Global hooks installed: /Users/you/.gemini/settings.json

# Remove hooks
bd setup gemini --remove

# Install project-specific hooks with stealth mode
bd setup gemini --project --stealth
```

### How It Works

The hooks call `bd prime` which:
1. Outputs workflow context for Gemini to read
2. Syncs any pending changes
3. Ensures Gemini always knows how to use beads

This works identically to Claude Code integration, using Gemini CLI's hook system (SessionStart and PreCompress events).

## Cursor IDE

Cursor integration creates a rules file that provides beads workflow context to the AI.

### Installation

```bash
bd setup cursor
```

### What Gets Installed

Creates `.cursor/rules/beads.mdc` with:
- Core workflow rules (track work in bd, not markdown TODOs)
- Quick command reference
- Workflow pattern (ready → claim → work → close → sync)
- Context loading instructions

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check if integration is installed |
| `--remove` | Remove beads rules file |

### Examples

```bash
# Check if rules are installed
bd setup cursor --check
# Output: ✓ Cursor integration installed: .cursor/rules/beads.mdc

# Remove rules
bd setup cursor --remove
```

### How It Works

Cursor reads `.cursor/rules/*.mdc` files and includes them in the AI's context. The beads rules file teaches the AI:
- To use `bd ready` for finding work
- To use `bd create` for tracking new issues
- To use `bd sync` at session end
- The basic workflow pattern

## Aider

Aider integration creates configuration files that teach the AI about beads, while respecting Aider's human-in-the-loop design.

### Installation

```bash
bd setup aider
```

### What Gets Installed

| File | Purpose |
|------|---------|
| `.aider.conf.yml` | Points Aider to read the instructions file |
| `.aider/BEADS.md` | Workflow instructions for the AI |
| `.aider/README.md` | Quick reference for humans |

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check if integration is installed |
| `--remove` | Remove beads configuration |

### Examples

```bash
# Check if config is installed
bd setup aider --check
# Output: ✓ Aider integration installed: .aider.conf.yml

# Remove configuration
bd setup aider --remove
```

### How It Works

Unlike Claude Code, Aider requires explicit command execution. The AI will **suggest** bd commands, which the user runs via `/run`:

```
You: What issues are ready to work on?

Aider: Let me check. Run:
/run bd ready

You: [runs the command]

Aider: Great! To claim bd-42, run:
/run bd update bd-42 --status in_progress
```

This respects Aider's philosophy of keeping humans in control while still leveraging beads for issue tracking.

## Comparison

| Feature | Factory.ai | Claude Code | Gemini CLI | Cursor | Aider |
|---------|-----------|-------------|------------|--------|-------|
| Command execution | Automatic | Automatic | Automatic | Automatic | Manual (/run) |
| Context injection | AGENTS.md | Hooks | Hooks | Rules file | Config file |
| Global install | No (per-project) | Yes | Yes | No (per-project) | No (per-project) |
| Stealth mode | N/A | Yes | Yes | N/A | N/A |
| Standard format | Yes (AGENTS.md) | No (proprietary) | No (proprietary) | No (proprietary) | No (proprietary) |
| Multi-tool compatible | Yes | No | No | No | No |

## Best Practices

1. **Start with Factory integration** - Creates AGENTS.md which works across multiple AI tools:
   ```bash
   bd setup factory
   ```

2. **Add tool-specific integrations as needed** - Claude hooks, Cursor rules, or Aider config for tool-specific features

3. **Install globally for Claude Code or Gemini CLI** - You'll get beads context in every project automatically

4. **Use stealth mode in CI/CD** - `bd setup claude --stealth` or `bd setup gemini --stealth` avoids git operations that might fail in automated environments

5. **Commit AGENTS.md to git** - This ensures all team members and AI tools get the same instructions

6. **Run `bd doctor` after setup** - Verifies the integration is working:
   ```bash
   bd doctor | grep -iE "claude|gemini"
   # Claude Integration: Hooks installed (CLI mode)
   # Gemini CLI Integration: Hooks installed
   ```

## Troubleshooting

### "Hooks not working"

1. Restart your AI tool after installation
2. Verify with `bd setup <tool> --check`
3. Check `bd doctor` output for integration status

### "Context not appearing"

For Claude Code, ensure `bd prime` works standalone:
```bash
bd prime
```

If this fails, fix the underlying beads issue first.

### "Want to switch from global to project hooks"

```bash
# Remove global hooks
bd setup claude --remove

# Install project hooks
bd setup claude --project
```

## Custom Recipes

You can add custom recipes for editors/tools not included in the built-in list.

### Adding a Custom Recipe

```bash
# Add a recipe that writes to a specific path
bd setup --add myeditor .myeditor/rules.md

# Install it
bd setup myeditor

# Check it
bd setup myeditor --check

# Remove it
bd setup myeditor --remove
```

### User Recipes File

Custom recipes are stored in `.beads/recipes.toml`:

```toml
[recipes.myeditor]
name = "myeditor"
path = ".myeditor/rules.md"
type = "file"
```

### Using Arbitrary Paths

For one-off installs without saving a recipe:

```bash
# Write template to any path
bd setup -o .my-custom-location/beads.md

# Inspect the template first
bd setup --print
```

### Recipe Types

| Type | Description | Example |
|------|-------------|---------|
| `file` | Write template to a single file | cursor, windsurf, cody, kilocode |
| `hooks` | Modify JSON settings to add hooks | claude, gemini |
| `section` | Inject marked section into existing file | factory |
| `multifile` | Write multiple files | aider |

Custom recipes added via `--add` are always type `file`.

## Related Documentation

- [CLAUDE_INTEGRATION.md](CLAUDE_INTEGRATION.md) - Design decisions for Claude Code integration
- [AIDER_INTEGRATION.md](AIDER_INTEGRATION.md) - Detailed Aider workflow guide
- [QUICKSTART.md](QUICKSTART.md) - Getting started with beads
- [CLI_REFERENCE.md](CLI_REFERENCE.md) - Full command reference
