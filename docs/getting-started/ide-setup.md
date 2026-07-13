---
title: IDE Setup
description: Configure bd setup recipes, hooks, and instruction files for Claude Code, Cursor, Gemini, Copilot, and other coding agents
---

Configure your IDE or coding agent for optimal beads integration.

Last reviewed: 2026-07-10

Freshness source: `cmd/bd/setup*.go` and `internal/recipes/`.

## How `bd setup` Works

The `bd setup` command uses a **recipe-based architecture**: recipes define where beads workflow instructions are written. Built-in recipes cover popular tools, and you can add custom recipes for any other tool (see Custom Recipes below). Integrations complement each other — you can install several at once.

```bash
bd setup --list             # Show all available recipes
bd setup claude             # Install an integration (claude, cursor, gemini, ...)
bd setup claude --check     # Verify installation
bd setup claude --remove    # Uninstall
```

| Recipe | Files written | Details |
|--------|---------------|---------|
| `claude` | `.claude/settings.json` (or `~/.claude/settings.json` with `--global`) + `CLAUDE.md` section | [Claude Code](/integrations/claude-code) |
| `cursor` | `.cursor/rules/beads.mdc` + `.cursor/hooks.json` + `.agents/skills/beads/` | [Cursor](/integrations/cursor) |
| `gemini` | `~/.gemini/settings.json` (or `.gemini/settings.json` with `--project`) + `GEMINI.md` section | [Gemini CLI](/integrations/gemini) |
| `copilot` | `.copilot-plugin/plugin.json` + `.github/copilot-instructions.md` | [Copilot CLI](/integrations/copilot-cli) |
| `codex` | `.agents/skills/beads/` + `AGENTS.md` section + `.codex/` hooks | [Codex](/integrations/codex) |
| `factory` | `AGENTS.md` section | [Factory.ai Droid](/integrations/factory) |
| `mux` | `AGENTS.md` section (+ `.mux/` layers with `--project`/`--global`) | [Mux](/integrations/mux) |
| `opencode` | `AGENTS.md` section | [OpenCode](/integrations/opencode) |
| `aider` | `.aider.conf.yml` + `.aider/BEADS.md` + `.aider/README.md` | [Aider](/integrations/aider) |
| `junie` | `.junie/guidelines.md` + `.junie/mcp/mcp.json` | [Junie](/integrations/junie) |
| `windsurf` | `.windsurf/rules/beads.md` | [Windsurf](/integrations/windsurf) |
| `cody` | `.cody/rules/beads.md` | [Cody](/integrations/cody) |
| `kilocode` | `.kilocode/rules/beads.md` | [Kilo Code](/integrations/kilocode) |

`bd prime` is the single source of truth for operational workflow commands. Each integration's instruction file either points to `bd prime` (hook-enabled agents) or carries the full command reference (AGENTS-first agents).

<Note>
Commit the instruction files (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, as applicable) to git so all team members and AI tools get the same instructions.
</Note>

### Template Profiles

Each integration writes one of two **profiles** that control how much content goes into the tool's instruction file (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, or `.github/copilot-instructions.md`):

| Profile | Used by | Content |
|---------|---------|---------|
| `full` | Factory, Mux, OpenCode | Complete command reference, issue types, priorities, workflow |
| `minimal` | Claude Code, GitHub Copilot CLI, Gemini CLI | Pointer to `bd prime`, quick reference only (~60% smaller) |

Hook-enabled agents use the `minimal` profile because `bd prime` injects full context at session start. AGENTS-first agents use the `full` profile because their instruction file remains the primary integration surface. Codex is skill-based instead: it uses `.agents/skills/beads/SKILL.md`, with managed `AGENTS.md` guidance telling Codex when to use the skill.

**Profile precedence:** if a file already has a `full` profile section and a `minimal` profile tool installs to the same file (for example via symlinks), the `full` profile is preserved to avoid information loss.

### Policy Profiles

Template profiles control how much text gets installed. Policy profiles control what an agent is authorized to do at handoff:

| Policy | Default scope | Commit/push guidance |
|--------|---------------|----------------------|
| `conservative` | Standalone projects, unknown projects, and one-off assistance | Use `bd` for task tracking, then report changed files, validation, and proposed commands. Do not commit, push, or run Dolt remote sync without explicit user or orchestrator approval. |
| `minimal` | Hook-first integrations where `bd prime` carries the detailed workflow | Same git authority as conservative; the installed file stays short and points to `bd prime`. |
| `team-maintainer` | Repositories that explicitly delegate session close to agents | Agents may close beads, run quality gates, commit, run `bd dolt push`, and `git push` as part of routine work. Current "do not commit" or "do not push" instructions still override the profile. |

The generated beads section and `bd prime` default to conservative git authority. Set the profile explicitly with the `agent.profile` config key or the `BD_AGENT_PROFILE` environment variable (values: `conservative`, `minimal`, `team-maintainer`; the env var takes precedence; an unrecognized value falls back to `conservative`):

```bash
bd config set agent.profile team-maintainer
# or, for a single session/process:
BD_AGENT_PROFILE=team-maintainer bd prime
```

`bd prime` layers this explicit knob on top of its per-branch git-authority checks (stealth mode, no git remote, ephemeral branch, `no-push`); those hard constraints still take precedence, and `team-maintainer` remains subordinate to any explicit "do not commit"/"do not push" instruction. Beads never infers team-maintainer authority merely because a remote exists — it must be set via this knob (or, for tools without config access, via top-level project instructions).

### Managed Sections

`bd setup factory`, `bd setup mux`, and `bd setup opencode` append a beads section to `AGENTS.md`, wrapped in `BEGIN/END BEADS INTEGRATION` HTML-comment markers. The begin marker carries version, profile, and hash metadata (e.g. `<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:19cc25d9 -->`) so `--check` can report `missing`, `stale`, or `current`; legacy markers without metadata are auto-upgraded on the next install or update. Re-running setup updates the existing section in place (idempotent), and `--remove` deletes only the managed section — the rest of your `AGENTS.md` is untouched.

`bd setup codex` uses its own marker pair (`BEGIN/END BEADS CODEX SETUP`). Running it alongside `bd setup factory` or `bd setup mux` against the same `AGENTS.md` leaves two managed sections side by side; each recipe's `--check` inspects only its own section, and each `--remove` removes only its own section.

One `AGENTS.md` works across many tools — Factory Droid, Mux, OpenCode, Cursor, Zed, Jules, and other AGENTS.md-aware assistants — so `bd setup factory` is a good starting point when your team mixes AI tools.

## Claude Code

The recommended approach for Claude Code:

```bash
bd setup claude            # Project install: .claude/settings.json
bd setup claude --global   # Global install: ~/.claude/settings.json
```

This installs:
- **SessionStart hook** - Runs `bd prime --hook-json`, which wraps the workflow context in the JSON envelope Claude Code expects. SessionStart fires when a session starts, resumes, or clears, and again after context compaction — no separate compaction hook is needed.
- **Minimal beads section in `CLAUDE.md`** - A pointer to `bd prime`, managed with hash/version markers for safe updates and `--check` freshness detection.

If the [beads Claude Code plugin](/integrations/claude-code-plugin) is installed, hooks are plugin-managed and `bd setup claude` skips writing them, so `bd prime` doesn't fire twice per session.

**How it works:**
1. SessionStart hook runs `bd prime --hook-json` automatically
2. `bd prime` injects ~1-2k tokens of workflow context
3. You use `bd` CLI commands directly
4. Git hooks refresh exports and legacy fallbacks; Dolt remotes handle sync

**Flags:**

| Flag | Description |
|------|-------------|
| `--check` | Check both hooks and the managed `CLAUDE.md` beads section |
| `--remove` | Remove beads hooks and the managed `CLAUDE.md` beads section |
| `--global` | Install to `~/.claude/settings.json` instead of the project |
| `--stealth` | Use `bd prime --stealth --hook-json` (flush only, no git operations) — useful in CI/CD where git operations might fail |

Restart Claude Code after installation for the hooks to take effect.

**Verify installation:**
```bash
bd setup claude --check
```

### Manual Setup

If you prefer manual configuration, add the hook to your Claude Code settings:

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

## Cursor IDE

```bash
bd setup cursor            # Rules file + agent hooks + beads skill
bd setup cursor --global   # Global hooks in ~/.cursor/hooks.json
```

This creates `.cursor/rules/beads.mdc` with beads-aware rules, `.cursor/hooks.json` with agent hooks (prime on session start, restore context after compaction), and the beads agent skill in `.agents/skills/beads/`.

**Verify:**
```bash
bd setup cursor --check
```

See [Cursor](/integrations/cursor) for hook details and PATH caveats.

## Gemini CLI

```bash
bd setup gemini            # Global hooks in ~/.gemini/settings.json
bd setup gemini --project  # Project hooks in .gemini/settings.json
```

This installs a SessionStart hook running `bd prime --hook-json` — Gemini requires hook stdout to be valid JSON, and `--hook-json` wraps the markdown in the required envelope — plus a minimal beads section in `GEMINI.md`. `--stealth` works the same as for Claude Code; `--check` and `--remove` cover both the hooks and the managed `GEMINI.md` section.

**Verify:**
```bash
bd setup gemini --check
```

See [Gemini CLI](/integrations/gemini) for details.

## Aider

```bash
# Setup Aider integration
bd setup aider
```

This writes three files:

| File | Purpose |
|------|---------|
| `.aider.conf.yml` | Points Aider to read the instructions file |
| `.aider/BEADS.md` | Workflow instructions for the AI |
| `.aider/README.md` | Quick reference for humans |

Aider is human-in-the-loop: the AI **suggests** `bd` commands, and you run them with `/run`. See [Aider](/integrations/aider) for the workflow.

**Verify:**
```bash
bd setup aider --check
```

## AGENTS.md Tools: Factory, Mux, OpenCode, Codex

```bash
bd setup factory    # Factory.ai Droid — AGENTS.md section
bd setup mux        # Mux — AGENTS.md section (+ --project/--global layers)
bd setup opencode   # OpenCode — AGENTS.md section
bd setup codex      # Codex — beads skill + AGENTS.md guidance + native hooks
```

These create or update a managed section in `AGENTS.md` (see Managed Sections above). `bd init` runs the project Codex setup automatically unless `--skip-agents` or `--stealth` is used. In worktree, shared, or `BEADS_DIR` setups, use `bd where` to confirm the resolved workspace — these integrations do not require a local `./.beads`. Restart the tool after setup if it is already running.

Details: [Factory.ai Droid](/integrations/factory), [Mux](/integrations/mux), [OpenCode](/integrations/opencode), [Codex](/integrations/codex).

## GitHub Copilot

**Copilot CLI:**

```bash
bd setup copilot
```

This installs a native Copilot CLI plugin manifest (`.copilot-plugin/plugin.json`, which registers `bd prime` hooks) and repository instructions (`.github/copilot-instructions.md`). See [Copilot CLI](/integrations/copilot-cli).

**For VS Code with GitHub Copilot**, use the MCP server:

```bash
# Install MCP server
uv tool install beads-mcp
```

Create `.vscode/mcp.json` in your project:

```json
{
  "servers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

**For all projects:** Add to VS Code user-level MCP config:

| Platform | Path |
|----------|------|
| macOS | `~/Library/Application Support/Code/User/mcp.json` |
| Linux | `~/.config/Code/User/mcp.json` |
| Windows | `%APPDATA%\Code\User\mcp.json` |

```json
{
  "servers": {
    "beads": {
      "command": "beads-mcp",
      "args": []
    }
  }
}
```

Initialize beads and reload VS Code:

```bash
bd init --quiet
```

See [GitHub Copilot Integration](/integrations/github-copilot) for detailed setup.

## Context Injection with `bd prime`

All integrations use `bd prime` to inject context:

```bash
bd prime
```

This outputs a compact (~1-2k tokens) workflow reference including:
- Available commands
- Current project status
- Workflow patterns
- Best practices
- Persistent memories from `bd remember`

`bd prime` prints memories near the top and starts with a truncation warning. If your host stores the full hook output in a file and only shows a preview, have the agent read the full file before continuing.

In hook contexts, `bd prime --hook-json` wraps the output in the SessionStart JSON envelope (Claude Code, Gemini CLI, Codex). For memory-only hooks:

```bash
bd prime --memories-only
```

**Why context efficiency matters:**
- Compute cost scales with tokens
- Latency increases with context size
- Models attend better to smaller, focused contexts

## MCP Server (Alternative)

For MCP-only environments (Claude Desktop, no shell access):

```bash
# Install MCP server
pip install beads-mcp
```

Add to Claude Desktop config:
```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

**Trade-offs:**
- Works in MCP-only environments
- Higher context overhead (10-50k tokens for tool schemas)
- Additional latency from MCP protocol

See [MCP Server](/integrations/mcp-server) for detailed configuration.

## Custom Recipes

For editors or tools without a built-in recipe:

```bash
bd setup --add myeditor .myeditor/rules.md   # Save a custom recipe
bd setup myeditor                            # Install it
bd setup myeditor --check                    # Check it
bd setup myeditor --remove                   # Remove it
```

Custom recipes are stored in `.beads/recipes.toml` (adding one requires an active beads workspace):

```toml
[recipes.myeditor]
name = "myeditor"
path = ".myeditor/rules.md"
type = "file"
```

For a one-off install without saving a recipe, write the template to any path — or inspect it first:

```bash
bd setup -o .my-custom-location/beads.md
bd setup --print
```

**Recipe types:**

| Type | Description | Used by |
|------|-------------|---------|
| `file` | Write the template to a single file | windsurf, cody, kilocode |
| `hooks` | Modify JSON settings to add hooks | claude, gemini |
| `section` | Inject a marked section into an existing file | factory, codex, mux, opencode |
| `multifile` | Write multiple files | aider, copilot, junie |

Custom recipes added via `--add` are always type `file`.

## Git Hooks

Ensure git hooks are installed for export refresh and legacy fallback behavior:

```bash
bd hooks install
```

This installs:
- **pre-commit** - Runs chained hooks before commit
- **post-merge** - Runs chained hooks after pull/merge
- **pre-push** - Runs chained hooks before push
- **post-checkout** - Runs chained hooks after branch checkout
- **prepare-commit-msg** - Adds agent identity trailers for forensics

**Check hook status:**
```bash
bd hooks list   # Installed, outdated, or missing
bd info         # Shows warnings if hooks are outdated
```

## Verifying Your Setup

Run a complete health check:

```bash
# Check version
bd version

# Check project health (includes integration status)
bd doctor

# Check git hooks
bd hooks list

# Check editor integration
bd setup claude --check   # or cursor, gemini, aider, ...
```

**Troubleshooting:**
- *Hooks not working?* Restart your AI tool after installation, then re-run `bd setup claude --check` (or your tool's recipe) and check `bd doctor` output for integration status.
- *Context not appearing?* Make sure `bd prime` works standalone; if it fails, fix the underlying beads issue first.
