# GitHub Copilot Instructions for Beads

## Project Overview

**beads** (command: `bd`) is a Git-backed issue tracker designed for AI-supervised coding workflows. We dogfood our own tool for all task tracking.

**Key Features:**
- Dependency-aware issue tracking
- Auto-sync with Git via JSONL
- AI-optimized CLI with JSON output
- Built-in daemon for background operations
- MCP server integration for Claude and other AI assistants

## Tech Stack

- **Language**: Go 1.21+
- **Storage**: Dolt (internal/storage/dolt/)
- **CLI Framework**: Cobra
- **Testing**: Go standard testing + table-driven tests
- **CI/CD**: GitHub Actions
- **MCP Server**: Python (integrations/beads-mcp/)

## Coding Guidelines

### Testing
- Always write tests for new features
- Use `t.TempDir() in Go tests` to avoid polluting production database
- Run `go test -short ./...` before committing
- Never create test issues in production DB (use temporary DB)

### Code Style
- Run `golangci-lint run ./...` before committing
- Follow existing patterns in `cmd/bd/` for new commands
- Add `--json` flag to all commands for programmatic use
- Update docs when changing behavior

### Git Workflow
- Always commit `.beads/issues.jsonl` with code changes
- Run `bd sync` at end of work sessions
- Install git hooks: `bd hooks install` (ensures DB ↔ JSONL consistency)

## Issue Tracking with bd

**CRITICAL**: This project uses **bd** for ALL task tracking. Do NOT create markdown TODO lists.

### Essential Commands

```bash
# Find work
bd ready --json                    # Unblocked issues
bd stale --days 30 --json          # Forgotten issues

# Create and manage (ALWAYS include --description)
bd create "Title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd update <id> --claim --json
bd close <id> --reason "Done" --json

# Search
bd list --status open --priority 1 --json
bd show <id> --json

# Sync (CRITICAL at end of session!)
bd sync  # Force immediate export/commit/push
```

### Workflow

1. **Check ready work**: `bd ready --json`
2. **Claim task**: `bd update <id> --claim`
3. **Work on it**: Implement, test, document
4. **Discover new work?** `bd create "Found bug" --description="What was found and why" -p 1 --deps discovered-from:<parent-id> --json`
5. **Complete**: `bd close <id> --reason "Done" --json`
6. **Sync**: `bd sync` (flushes changes to git immediately)

**IMPORTANT**: Always include `--description` when creating issues. Issues without descriptions lack context for future work.

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (default, nice-to-have)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

## Project Structure

```
beads/
├── cmd/bd/              # CLI commands (add new commands here)
├── internal/
│   ├── types/           # Core data types
│   └── storage/         # Storage layer
│       └── dolt/        # Dolt implementation
├── integrations/
│   └── beads-mcp/       # MCP server (Python)
├── examples/            # Integration examples
├── docs/                # Documentation
└── .beads/
    ├── dolt/            # Dolt database (DO NOT COMMIT)
    └── issues.jsonl     # Git-synced issue storage
```

## Available Resources

### MCP Server (Recommended)
Use the beads MCP server for native function calls instead of shell commands:
- Install: `pip install beads-mcp`
- Functions: `mcp__beads__ready()`, `mcp__beads__create()`, etc.
- See `integrations/beads-mcp/README.md`

### Scripts
- `./scripts/bump-version.sh <version> --commit` - Update all version files atomically
- `./scripts/release.sh <version>` - Complete release workflow
- `./scripts/update-homebrew.sh <version>` - Update Homebrew formula

### Key Documentation
- **AGENTS.md** - Comprehensive AI agent guide (detailed workflows, advanced features)
- **AGENT_INSTRUCTIONS.md** - Development procedures, testing, releases
- **README.md** - User-facing documentation
- **docs/CLI_REFERENCE.md** - Complete command reference

## Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Run `bd sync` at end of sessions
- ✅ Test with `t.TempDir() in Go tests`
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT create test issues in production DB
- ❌ Do NOT commit `.beads/dolt/` (JSONL only)

---

**For detailed workflows and advanced features, see [AGENTS.md](../AGENTS.md)**
