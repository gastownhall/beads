# Setup Command Reference

**For:** Setting up beads integration with AI coding tools
**Version:** 0.30.0+

## Overview

The `bd setup` command uses a **recipe-based architecture** to configure beads integration with AI coding tools. Recipes define where workflow instructions are written—built-in recipes handle popular tools, and you can add custom recipes for any tool.

### `bd prime` as SSOT

`bd prime` is the **single source of truth** for operational workflow commands. The beads section in each tool's instruction file provides a pointer to `bd prime` for hook-enabled agents (Claude, Gemini) or the full command reference for AGENTS-first agents (Factory, Mux). The repo-local Beads agent skill under `.agents/skills/` is the skill-based instruction surface; `bd setup codex` installs it and can also add Codex project hooks.

### Profiles

Each integration uses one of two **profiles** that control how much content is written to tool instruction files (`AGENTS.md`, `CLAUDE.md`, or `GEMINI.md`):

| Profile | Used By | Content |
|---------|---------|---------|
| `full` | Factory, Mux, OpenCode | Complete command reference, issue types, priorities, workflow |
| `minimal` | Claude Code, Gemini CLI | Pointer to `bd prime`, quick reference only (~60% smaller) |

Hook-enabled agents (Claude, Gemini) use the `minimal` profile because `bd prime` injects full context at session start. AGENTS-first agents use the `full` profile because their instruction file remains the primary integration surface. Skill-aware agents use `.agents/skills/beads/SKILL.md`, with `AGENTS.md` available as an explicit fallback.

**Profile precedence:** If a file already has a `full` profile section and a `minimal` profile tool installs to the same file (e.g., via symlinks), the `full` profile is preserved to avoid information loss.

### Built-in Recipes

| Recipe | Path | Integration Type |
|--------|------|-----------------|
| `cursor` | `.cursor/rules/beads.mdc` | Rules file |
| `windsurf` | `.windsurf/rules/beads.md` | Rules file |
| `cody` | `.cody/rules/beads.md` | Rules file |
| `kilocode` | `.kilocode/rules/beads.md` | Rules file |
| `claude` | `~/.claude/settings.json` + `CLAUDE.md` | SessionStart/PreCompact hooks + minimal section |
| `gemini` | `~/.gemini/settings.json` + `GEMINI.md` | SessionStart/PreCompress hooks + minimal section |
| `factory` | `AGENTS.md` | Marked section |
| `codex` | `.agents/skills/beads/SKILL.md` | Beads agent skill |
| `mux` | `AGENTS.md` | Marked section |
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
bd setup codex      # Beads agent skill
bd setup codex --hooks  # Beads agent skill + project-local Codex hooks
bd setup mux        # Mux
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

The beads section is wrapped in HTML comments (`<!-- BEGIN/END BEADS INTEGRATION -->`) with metadata for safe updates. The begin marker includes profile and hash metadata (e.g., `<!-- BEGIN BEADS INTEGRATION profile:full hash:d4f96305 -->`) for freshness detection. Legacy markers without metadata are auto-upgraded on the next install or update.

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
| `--check` | Check if beads section exists and is current (reports `missing`, `stale`, or `current`) |
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
- To use `bd dolt push` at session end
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

Codex reads repo-local skills from `.agents/skills/`. The Codex setup path installs the generic `beads` agent skill by default, with optional project-local hooks for automatic `bd prime` injection at session start and after context compaction. `AGENTS.md` remains available as an explicit fallback for older or non-skill clients.

### Installation

```bash
bd init                 # New projects: AGENTS.md + Beads agent skill + Codex hooks
bd setup codex          # Beads agent skill
bd setup codex --hooks  # Beads agent skill + .codex/hooks.json + .codex/config.toml
bd setup codex --agents # Beads agent skill + AGENTS.md fallback
```

### What Gets Installed

**Baseline install** (`bd setup codex`):
- Creates or updates `.agents/skills/beads/SKILL.md`
- Creates or updates `.agents/skills/beads/agents/openai.yaml`

**New project init** (`bd init`):
- Creates or updates `AGENTS.md`
- Creates or updates `.agents/skills/beads/SKILL.md`
- Creates project-local Codex hook config under `.codex/`
- `--stealth` skips `AGENTS.md` and lifecycle hooks, but still installs the Beads agent skill
- `--skip-agents` skips instruction files (`AGENTS.md` and `CLAUDE.md`), but still installs the Beads agent skill and project hooks

**Hook install** (`bd setup codex --hooks`):
- Creates or updates the Beads agent skill
- Creates or updates `.codex/hooks.json` with:
  - a `SessionStart` hook that runs `bd prime --codex`
  - a `UserPromptSubmit` hook that runs `bd run-hook --codex` and reinjects `bd prime --codex` context only when the transcript shows a context compaction since the latest user message
- Creates or updates `.codex/config.toml` with `[features] codex_hooks = true`
- Creates or updates `.codex/.gitignore` so hook runtime state is not committed

**AGENTS fallback** (`bd setup codex --agents`):
- Also creates or updates `AGENTS.md` with the beads integration section

Use `--stealth` with `--hooks` to install `bd prime --codex --stealth` and `bd run-hook --codex --stealth` instead.

### Codex Plugin Package

This repository also ships a shared plugin package for plugin marketplace installs:

- `.agents/plugins/marketplace.json` - repo-local Codex marketplace entry
- `plugins/beads/.codex-plugin/plugin.json` - Codex plugin metadata
- `plugins/beads/skills/beads/` - plugin-owned Beads skill
- `plugins/beads/.claude-plugin/plugin.json` - Claude plugin metadata

The plugin package is separate from `bd setup codex`. `bd setup codex` writes a setup-only Beads skill directly into the target repository; the plugin package is the richer marketplace bundle for Claude and Codex.

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check the Beads agent skill; with `--hooks`, also verify `.codex/hooks.json` and `codex_hooks` enablement |
| `--remove` | Remove the Beads agent skill; with `--hooks`, also remove the beads-managed Codex hooks and `codex_hooks` feature flag |
| `--hooks` | Install/check/remove project-local Codex hook configuration in `.codex/` |
| `--agents` | Also install/check/remove `AGENTS.md` fallback content |
| `--stealth` | With `--hooks`, install stealth variants of the Codex prime and hook-runner commands |

### Notes

- Restart Codex if it's already running to pick up the new instructions.
- The hook install is project-local. It does not touch global Codex config.
- In worktree/shared/`BEADS_DIR` setups, use `bd where` to confirm the resolved workspace; the integration does not require a local `./.beads`.

## Mux

Mux reads layered instruction files, including workspace `AGENTS.md`. Adding the beads section is enough to get Mux and beads working together.

### Installation

```bash
bd setup mux            # Root AGENTS.md
bd setup mux --project  # Root AGENTS.md + .mux/AGENTS.md
bd setup mux --global   # Root AGENTS.md + ~/.mux/AGENTS.md
```

### What Gets Installed

Creates or updates `AGENTS.md` with the beads integration section (same markers as Factory.ai and Codex).

### Notes

- Mux instruction file behavior is documented at [https://mux.coder.com/AGENTS.md](https://mux.coder.com/AGENTS.md).
- Restart the workspace session if Mux is already running.

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check root integration (and with layer flags, also check those layers) |
| `--remove` | Remove root integration (and with layer flags, also remove those layers) |
| `--project` | Install/check/remove workspace-layer instructions in `.mux/AGENTS.md` |
| `--global` | Install/check/remove global-layer instructions in `~/.mux/AGENTS.md` |

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

**Instruction file** (`CLAUDE.md` in project root):
- Minimal-profile beads section pointing to `bd prime`
- Managed with hash/version markers for safe updates and `--check` freshness detection

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check both hooks and the managed `CLAUDE.md` beads section |
| `--remove` | Remove beads hooks and managed `CLAUDE.md` beads section |
| `--project` | Install for this project only (not globally) |
| `--stealth` | Use `bd prime --stealth` (flush only, no git operations) |

### Examples

```bash
# Check hooks + CLAUDE.md beads section
bd setup claude --check
# Output: ✓ Global hooks installed: /Users/you/.claude/settings.json
#         ✓ Claude Code integration installed: /path/to/CLAUDE.md (current)

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
4. Follows resolved workspace semantics, so `bd where` is the right diagnostic check when local `./.beads` is absent

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

**Instruction file** (`GEMINI.md` in project root):
- Minimal-profile beads section pointing to `bd prime`
- Managed with hash/version markers for safe updates and `--check` freshness detection

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check both hooks and the managed `GEMINI.md` beads section |
| `--remove` | Remove beads hooks and managed `GEMINI.md` beads section |
| `--project` | Install for this project only (not globally) |
| `--stealth` | Use `bd prime --stealth` (flush only, no git operations) |

### Examples

```bash
# Check hooks + GEMINI.md beads section
bd setup gemini --check
# Output: ✓ Global hooks installed: /Users/you/.gemini/settings.json
#         ✓ Gemini CLI integration installed: /path/to/GEMINI.md (current)

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
- To use `bd dolt push` at session end
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
/run bd update bd-42 --claim
```

This respects Aider's philosophy of keeping humans in control while still leveraging beads for issue tracking.

## Comparison

| Feature | Factory.ai | Codex | Mux | Claude Code | Gemini CLI | Cursor | Aider |
|---------|-----------|-------|-----|-------------|------------|--------|-------|
| Command execution | Automatic | Automatic | Automatic | Automatic | Automatic | Automatic | Manual (/run) |
| Context injection | AGENTS.md | AGENTS.md | AGENTS.md | Hooks + CLAUDE.md | Hooks + GEMINI.md | Rules file | Config file |
| Global install | No (per-project) | No (per-project) | No (per-project) | Yes | Yes | No (per-project) | No (per-project) |
| Stealth mode | N/A | N/A | N/A | Yes | Yes | N/A | N/A |
| Standard format | Yes (AGENTS.md) | Yes (AGENTS.md) | Yes (AGENTS.md) | No (proprietary) | No (proprietary) | No (proprietary) | No (proprietary) |
| Multi-tool compatible | Yes | Yes | Yes | No | No | No | No |

## Best Practices

1. **Start with Factory integration** - Creates AGENTS.md which works across multiple AI tools:
   ```bash
   bd setup factory
   ```

2. **Add tool-specific integrations as needed** - Claude hooks, Cursor rules, or Aider config for tool-specific features

3. **Install globally for Claude Code or Gemini CLI** - You'll get beads context in every project automatically

4. **Use stealth mode in CI/CD** - `bd setup claude --stealth` or `bd setup gemini --stealth` avoids git operations that might fail in automated environments

5. **Commit instruction files to git** - This ensures all team members and AI tools get the same instructions (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, as applicable)

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
