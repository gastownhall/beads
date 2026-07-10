# Setup Command Reference

Last reviewed: 2026-05-08

Freshness source: `cmd/bd/setup*.go` and `internal/recipes/`.

**For:** Setting up beads integration with AI coding tools
**Version:** current CLI behaviour; verify recipe lists against the freshness
source above.

## Overview

The `bd setup` command uses a **recipe-based architecture** to configure beads integration with AI coding tools. Recipes define where workflow instructions are written—built-in recipes handle popular tools, and you can add custom recipes for any tool.

### `bd prime` as SSOT

`bd prime` is the **single source of truth** for operational workflow commands. The beads section in each tool's instruction file provides a pointer to `bd prime` for hook-enabled agents (Claude, Gemini) or the full command reference for AGENTS-first agents (Factory, Mux). Codex uses a Beads skill, generated `AGENTS.md` guidance, and native Codex hooks.

### Template Profiles

Each integration uses one of two **profiles** that control how much content is written to tool instruction files (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, or `.github/copilot-instructions.md`):

| Profile | Used By | Content |
|---------|---------|---------|
| `full` | Factory, Codex, Mux, OpenCode | Complete command reference, issue types, priorities, workflow |
| `minimal` | Claude Code, GitHub Copilot CLI, Gemini CLI | Pointer to `bd prime`, quick reference only (~60% smaller) |

Hook-enabled agents (Claude, Copilot CLI, Gemini) use the `minimal` profile because `bd prime` injects full context at session start. AGENTS-first agents use the `full` profile because their instruction file remains the primary integration surface. Skill-aware agents use `.agents/skills/beads/SKILL.md`, with project `AGENTS.md` or global `$CODEX_HOME/AGENTS.md`/`~/.codex/AGENTS.md` telling Codex when to use the skill.

**Profile precedence:** If a file already has a `full` profile section and a `minimal` profile tool installs to the same file (e.g., via symlinks), the `full` profile is preserved to avoid information loss.

### Policy Profiles

Template profiles control how much text gets installed. Policy profiles control what an agent is authorized to do at handoff:

| Policy | Default Scope | Commit/Push Guidance |
|--------|---------------|----------------------|
| `conservative` | Standalone projects, unknown projects, and one-off assistance | Use `bd` for task tracking, then report changed files, validation, and proposed commands. Do not commit, push, or run Dolt remote sync without explicit user or orchestrator approval. |
| `minimal` | Hook-first integrations where `bd prime` carries the detailed workflow | Same git authority as conservative; the installed file stays short and points to `bd prime`. |
| `team-maintainer` | Repositories that explicitly delegate session close to agents | Agents may close beads, run quality gates, commit, run `bd dolt push`, and `git push` as part of routine work. Current "do not commit" or "do not push" instructions still override the profile. |

The generated Beads block and `bd prime` default to conservative git authority. Set the profile explicitly with the `agent.profile` config key or the `BD_AGENT_PROFILE` environment variable (values: `conservative`, `minimal`, `team-maintainer`; env var takes precedence; an unrecognized value falls back to `conservative`):

```bash
bd config set agent.profile team-maintainer
# or, for a single session/process:
BD_AGENT_PROFILE=team-maintainer bd prime
```

`bd prime` layers this explicit knob on top of its existing per-branch git-authority checks (stealth mode, no git remote, ephemeral branch, `no-push`); those hard constraints still take precedence, and `team-maintainer` remains subordinate to any explicit "do not commit"/"do not push" instruction. Beads does not infer team-maintainer authority merely because a remote exists; it must be set via this knob (or, for tools without config access, via top-level project instructions).

### Built-in Recipes

| Recipe | Path | Integration Type |
|--------|------|-----------------|
| `cursor` | `.cursor/rules/beads.mdc` | Rules file |
| `windsurf` | `.windsurf/rules/beads.md` | Rules file |
| `cody` | `.cody/rules/beads.md` | Rules file |
| `kilocode` | `.kilocode/rules/beads.md` | Rules file |
| `claude` | `~/.claude/settings.json` + `CLAUDE.md` | SessionStart/PreCompact hooks + minimal section |
| `copilot` | `.copilot-plugin/plugin.json` + `.github/copilot-instructions.md` | native Copilot plugin hooks + repository instructions |
| `gemini` | `~/.gemini/settings.json` + `GEMINI.md` | SessionStart/PreCompress hooks + minimal section |
| `factory` | `AGENTS.md` | Marked section |
| `codex` | `.agents/skills/beads/SKILL.md` + `AGENTS.md` + `.codex/` | Beads agent skill + generated guidance + native hooks |
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
bd setup copilot    # GitHub Copilot CLI plugin + instructions
bd setup gemini     # Gemini CLI
bd setup factory    # Factory.ai Droid
bd setup codex      # Beads agent skill + AGENTS.md guidance + native hooks
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
- Dolt remote sync explanation
- Important rules for AI agents

The beads section is wrapped in HTML comments (`<!-- BEGIN/END BEADS INTEGRATION -->`) with metadata for safe updates. The begin marker includes version, profile, and hash metadata (e.g., `<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:19cc25d9 -->`) for freshness detection. Legacy markers without metadata are auto-upgraded on the next install or update.

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
- To treat commit, push, and Dolt remote sync as policy-controlled handoff actions
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

Codex reads project instructions from `AGENTS.md` in the current working directory or project root, and global instructions from `$CODEX_HOME/AGENTS.md` when `CODEX_HOME` is set, otherwise `~/.codex/AGENTS.md`. The Codex setup path installs the generic `beads` agent skill, writes managed Codex guidance, enables native hooks, and installs a fallback hooks file when the Beads Codex plugin is not managing hooks.

### Installation

```bash
bd setup codex          # Project Beads skill + AGENTS.md guidance + native hooks
bd setup codex --global # Global Beads skill + guidance + native hooks
```

### What Gets Installed

**Project install** (`bd setup codex`):
- Creates or updates `.agents/skills/beads/SKILL.md`
- Creates or updates `.agents/skills/beads/agents/openai.yaml`
- Creates or updates project `AGENTS.md` with a marked section generated by `bd setup codex`
- Creates or updates `.codex/config.toml` with native hooks enabled
- Creates or updates `.codex/hooks.json` unless hooks are plugin-managed

**Global install** (`bd setup codex --global`):
- Creates or updates `~/.agents/skills/beads/SKILL.md`
- Creates or updates `~/.agents/skills/beads/agents/openai.yaml`
- Creates or updates `$CODEX_HOME/AGENTS.md` when `CODEX_HOME` is set, otherwise `~/.codex/AGENTS.md`, with a marked section generated by `bd setup codex --global`
- Creates or updates global Codex hook configuration under `$CODEX_HOME` when set, otherwise `~/.codex`

### Flags

| Flag | Description |
|------|-------------|
| `--check` | Check the Beads agent skill, managed Codex `AGENTS.md` guidance, and native hooks |
| `--remove` | Remove the Beads agent skill, managed Codex `AGENTS.md` guidance, and native hooks |
| `--global` | Install/check/remove the global skill, global Codex guidance, and native hooks |

### Notes

- Restart Codex if it's already running to pick up the new instructions.
- The plugin package under `plugins/beads/` is separate from `bd setup codex`. The setup command writes a small setup-only skill and managed guidance into the target repository or user-level `.agents` directory.
- `bd init` runs project `bd setup codex` automatically unless `--skip-agents` or `--stealth` is used.
- In worktree/shared/`BEADS_DIR` setups, use `bd where` to confirm the resolved workspace; the integration does not require a local `./.beads`.
- `bd setup codex` uses its own marker pair (`BEGIN/END BEADS CODEX SETUP`), distinct from the `BEGIN/END BEADS INTEGRATION` markers used by `bd setup factory` and `bd setup mux`. Running both `bd setup codex` and `bd setup factory`/`mux` against the same `AGENTS.md` will leave two managed sections side by side; each `bd setup … --check` only inspects its own section, and `bd setup … --remove` only removes its own section.

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
- `SessionStart` hook: Runs `bd prime --hook-json` when a session starts, resumes, clears, or restarts after compaction

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

The hook calls `bd prime --hook-json` which:
1. Outputs workflow context wrapped in the SessionStart JSON envelope Claude Code expects
2. Prints persistent memories near the top so hook-output previews do not hide them
3. Starts with a truncation warning telling agents to read the full persisted hook output when the host caps previews
4. Ensures Claude always knows how to use beads
5. Follows resolved workspace semantics, so `bd where` is the right diagnostic check when local `./.beads` is absent

For low-token hooks that only need durable project facts, use `bd prime --memories-only`.

This is more context-efficient than MCP tools (~1-2k tokens vs 10-50k for MCP schemas).

## Gemini CLI

Gemini CLI integration uses a SessionStart hook to automatically inject beads workflow context when a session opens.

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
- `SessionStart` hook: Runs `bd prime --hook-json` when a new session starts, wrapped in the JSON envelope Gemini's hook contract requires

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
2. Prints persistent memories near the top so hook-output previews do not hide them
3. Starts with a truncation warning telling agents to read the full persisted hook output when the host caps previews
4. Ensures Gemini always knows how to use beads

For low-token hooks that only need durable project facts, use `bd prime --memories-only`.

This works similarly to Claude Code integration, using Gemini CLI's hook system (SessionStart event). Unlike Claude Code, Gemini requires hook stdout to be valid JSON — `bd prime --hook-json` wraps the markdown in the required envelope.

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
- To treat commit, push, and Dolt remote sync as policy-controlled handoff actions
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
| Context injection | AGENTS.md | Skill + AGENTS.md | AGENTS.md | Hooks + CLAUDE.md | Hooks + GEMINI.md | Rules file | Config file |
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
