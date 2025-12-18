# Beads Examples

This directory contains examples of how to integrate bd with AI agents and workflows.

## Examples

### Agent Integration
- **[python-agent/](python-agent/)** - Simple Python agent that discovers ready work and completes tasks
- **[bash-agent/](bash-agent/)** - Bash script showing the full agent workflow
- **[startup-hooks/](startup-hooks/)** - Session startup scripts for automatic bd upgrade detection
- **[claude-desktop-mcp/](claude-desktop-mcp/)** - MCP server for Claude Desktop integration

### Tools & Utilities
- **[monitor-webui/](monitor-webui/)** - Standalone web interface for real-time issue monitoring and visualization
- **[markdown-to-jsonl/](markdown-to-jsonl/)** - Convert markdown planning docs to bd issues
- **[github-import/](github-import/)** - Import issues from GitHub repositories
- **[git-hooks/](git-hooks/)** - Pre-configured git hooks for automatic export/import
<!-- REMOVED (bd-4c74): branch-merge example - collision resolution no longer needed with hash IDs -->

### Workflow Patterns
- **[contributor-workflow/](contributor-workflow/)** - OSS contributor setup with separate planning repo
- **[team-workflow/](team-workflow/)** - Team collaboration with shared repositories
- **[multi-phase-development/](multi-phase-development/)** - Organize large projects by phases (planning, MVP, iteration, polish)
- **[multiple-personas/](multiple-personas/)** - Architect/implementer/reviewer role separation
- **[protected-branch/](protected-branch/)** - Protected branch workflow for team collaboration

## Quick Start

```bash
# Try the Python agent example
cd python-agent
python agent.py

# Try the bash agent example
cd bash-agent
./agent.sh

# Install git hooks
cd git-hooks
./install.sh

# REMOVED (bd-4c74): branch-merge demo - hash IDs eliminate collision resolution
```

## Creating Your Own Agent

The basic agent workflow:

1. **Find ready work**: `bd ready --json --limit 1`
2. **Claim the task**: `bd update <id> --status in_progress --json`
3. **Do the work**: Execute the task
4. **Discover new issues**: `bd create "Found bug" --json`
5. **Link discoveries**: `bd dep add <new-id> <parent-id> --type discovered-from`
6. **Complete the task**: `bd close <id> --reason "Done" --json`

All commands support `--json` for easy parsing.
