# Claude Code Skill for Beads

A comprehensive Claude Code skill that teaches Claude how to use beads effectively for issue tracking in multi-session coding workflows.

## What is This?

This is a [Claude Code](https://claude.com/claude-code) skill - a markdown-based instruction set that teaches Claude AI how to use beads. While the [beads plugin](../../.claude-plugin/) provides slash commands and MCP tools for basic operations, this skill complements it by teaching the **philosophy and patterns** of effective beads usage.

## What Does It Provide?

**Main skill file:**
- Core workflow patterns (discovery, execution, planning phases)
- Decision criteria for when to use bd vs TodoWrite/markdown
- Session start protocols and ready work checks
- Compaction survival patterns (critical for Claude Code context limits)
- Issue lifecycle management with self-check checklists
- Integration patterns with other tools

**Reference documentation:**
- `references/BOUNDARIES.md` - Detailed decision criteria for bd vs TodoWrite with examples
- `references/CLI_REFERENCE.md` - Complete command reference with all flags
- `references/DEPENDENCIES.md` - Deep dive into dependency types and relationship patterns
- `references/WORKFLOWS.md` - Step-by-step workflows with checklists
- `references/ISSUE_CREATION.md` - When to ask vs create issues, quality guidelines
- `references/RESUMABILITY.md` - Making issues resumable across sessions with working code examples
- `references/STATIC_DATA.md` - Using bd for reference databases and glossaries

## Why is This Useful?

The skill helps Claude understand:

1. **When to use beads** - Not every task needs bd. The skill teaches when bd helps vs when markdown/TodoWrite is better (per Steve Yegge's insight about markdown "losing its way in the middle")

2. **How to structure issues** - Proper use of dependency types, issue metadata, and relationship patterns

3. **Workflow patterns** - Proactive issue creation during discovery, status maintenance during execution, dependency graphs during planning

4. **Integration with other tools** - How bd and TodoWrite can coexist, each serving its purpose

## Installation

### Prerequisites

1. Install beads CLI:
   ```bash
   curl -sSL https://raw.githubusercontent.com/steveyegge/beads/main/install.sh | bash
   ```

2. Have [Claude Code](https://claude.com/claude-code) installed

### Install the Skill

You can install this skill in two ways:

#### Option 1: Copy to Claude Code Skills Directory

```bash
# Clone this repo (if you haven't already)
git clone https://github.com/steveyegge/beads.git
cd beads/examples/claude-code-skill

# Create a symlink in your Claude Code skills directory
ln -s "$(pwd)" ~/.claude/skills/bd-issue-tracking
```

#### Option 2: Copy Files Directly

```bash
# Create the skill directory
mkdir -p ~/.claude/skills/bd-issue-tracking

# Copy the skill files
cp -r beads/examples/claude-code-skill/* ~/.claude/skills/bd-issue-tracking/
```

### Verify Installation

Restart Claude Code, then in a new session, ask:

```
Do you have the bd skill installed?
```

Claude should confirm it has access to the bd skill and can help with beads issue tracking.

## How It Works

Claude Code automatically loads skills from `~/.claude/skills/`. When this skill is installed:

1. Claude gets the core workflow from `SKILL.md` immediately
2. Claude can read reference docs when it needs detailed information
3. The skill uses progressive disclosure - quick reference in SKILL.md, details in references/

## Usage Examples

Once installed, Claude will automatically:

- Check for ready work at session start (if `.beads/` exists)
- Suggest creating bd issues for multi-session work
- Use appropriate dependency types when linking issues
- Maintain proper issue lifecycle (create → in_progress → close)
- Know when to use bd vs TodoWrite

You can also explicitly ask Claude to use beads:

```
Let's track this work in bd since it spans multiple sessions
```

```
Create a bd issue for this bug we discovered
```

```
Show me what's ready to work on in bd
```

## Relationship to Beads Plugin

This skill complements the [beads plugin](../../.claude-plugin/):

- **Plugin** (`.claude-plugin/`): Provides slash commands (`/bd-create`, `/bd-ready`) and MCP tools for basic operations
- **Skill** (this directory): Teaches Claude the patterns, philosophy, and decision-making for effective beads usage

You can use both together for the best experience:
- Plugin for quick operations
- Skill for intelligent workflow decisions

### Why CLI Instead of MCP?

This skill teaches Claude to use the bd CLI directly (via Bash commands like `bd ready`, `bd create`, etc.) rather than relying on MCP tools. This approach has several benefits:

- **Lower context usage** - No MCP server prompt loaded into every session, saving tokens
- **Works everywhere** - Only requires bd binary installed, no MCP server setup needed
- **Explicit operations** - All bd commands visible in conversation history for transparency
- **Full functionality** - CLI supports `--json` flag for programmatic parsing just like MCP

The MCP server is excellent for interactive use, but for autonomous agent workflows where context efficiency matters, direct CLI usage is more practical. The skill provides the guidance Claude needs to use the CLI effectively.

## Contributing

Found ways to improve the skill? Contributions welcome! See [CONTRIBUTING.md](../../CONTRIBUTING.md) for guidelines.

## License

Same as beads - MIT License. See [LICENSE](../../LICENSE).
