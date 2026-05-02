# Instructions for AI Agents Working on Beads

> **Reading order**: Architecture → `docs/CLAUDE.md` | Workflow → `AGENT_INSTRUCTIONS.md` | PR maintenance → `PR_MAINTAINER_GUIDELINES.md` | Quick ref → `AGENTS.md`
>
> Run `bd prime` for AI-optimized context. Run `bd memories` to see persistent learnings.

## Project Overview

This is **beads** (command: `bd`), a Dolt-powered issue tracker designed for AI-supervised coding workflows. We dogfood our own tool!

## Issue Tracking

We use bd (beads) for issue tracking instead of Markdown TODOs or external tools.

### Quick Reference

```bash
# Find ready work (no blockers)
bd ready --json

# Find ready work including future deferred issues
bd ready --include-deferred --json

# Create new issue
bd create "Issue title" -t bug|feature|task -p 0-4 -d "Description" --json

# Create issue with due date and defer (GH#820)
bd create "Task" --due=+6h              # Due in 6 hours
bd create "Task" --defer=tomorrow       # Hidden from bd ready until tomorrow
bd create "Task" --due="next monday" --defer=+1h  # Both

# Update issue status
bd update <id> --status in_progress --json

# Update issue with due/defer dates
bd update <id> --due=+2d                # Set due date
bd update <id> --defer=""               # Clear defer (show immediately)

# Link discovered work
bd dep add <discovered-id> <parent-id> --type discovered-from

# Complete work
bd close <id> --reason "Done" --json

# Show dependency tree
bd dep tree <id>

# Get issue details
bd show <id> --json

# Query issues by time-based scheduling (GH#820)
bd list --deferred              # Show issues with defer_until set
bd list --defer-before=tomorrow # Deferred before tomorrow
bd list --defer-after=+1w       # Deferred after one week from now
bd list --due-before=+2d        # Due within 2 days
bd list --due-after="next monday" # Due after next Monday
bd list --overdue               # Due date in past (not closed)
```

### Workflow

1. **Check for ready work**: Run `bd ready` to see what's unblocked
2. **Claim your task**: `bd update <id> --claim` (atomic compare-and-swap)
3. **Work on it**: Implement, test, document
4. **Discover new work**: If you find bugs or TODOs, create issues:
   - `bd create "Found bug in auth" -t bug -p 1 --json`
   - Link it: `bd dep add <new-id> <current-id> --type discovered-from`
5. **Complete**: `bd close <id> --reason "Implemented"`

### Issue Types

- `bug` - Something broken that needs fixing
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature composed of multiple issues
- `chore` - Maintenance work (dependencies, tooling)

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (nice-to-have features, minor bugs)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

### Dependency Types

- `blocks` - Hard dependency (issue X blocks issue Y)
- `related` - Soft relationship (issues are connected)
- `parent-child` - Epic/subtask relationship
- `discovered-from` - Track issues discovered during work

Only `blocks` dependencies affect the ready work queue.

## Development Guidelines

### Code Standards

- **Go version**: see `go.mod` for the required version (currently 1.26+)
- **Build tag**: `-tags gms_pure_go` is **MANDATORY** for all builds and tests
- **Build/Install**: `make build` / `make install` (never bare `go build` or `go install`)
- **Testing**: `make test` (never bare `go test ./...` — it misses required tags)
- **Linting**: `golangci-lint run ./...` (baseline warnings documented in docs/LINTING.md)
- **Documentation**: Update relevant .md files

### File Organization

```
beads/
├── cmd/bd/              # CLI commands (Cobra, one file per command)
├── internal/
│   ├── types/           # Core data types
│   ├── config/          # Configuration
│   ├── configfile/      # Config file handling
│   └── storage/         # Storage layer
│       ├── dolt/        # Dolt implementation (primary)
│       └── embeddeddolt/ # Embedded Dolt (CGO-dependent)
├── integrations/        # MCP server, external integrations
├── examples/            # Integration examples
└── *.md                 # Documentation
```

### Before Committing

1. **Run tests**: `make test` (or `./scripts/test.sh`)
2. **Run linter**: `golangci-lint run ./...` (ignore baseline warnings)
3. **Update docs**: If you changed behavior, update README.md or other docs
4. **Install git hooks**: `bd hooks install` (auto-commits Dolt changes)

### Git Workflow

bd uses **Dolt** as its primary database with automatic versioning:

```bash
# Each bd write auto-commits to Dolt history
bd create "Issue title" -p 1

# Sync with remote
bd dolt push    # Push Dolt data
bd dolt pull    # Pull Dolt data

# Git workflow (code changes only)
git add <files>
git commit -m "Your message"
git push
```

Install git hooks via `bd hooks install` for automatic sync.

## Current Project Status

Run `bd stats` to see overall progress.

### Active Areas

- **Core CLI**: Mature, always room for polish
- **Examples**: Growing collection of agent integrations
- **Documentation**: Comprehensive but can always improve
- **MCP Server**: Available at `integrations/beads-mcp/`
- **Claude/Codex Plugin**: Shared plugin package at `plugins/beads/`

## Common Tasks

### Adding a New Command

1. Create file in `cmd/bd/`
2. Add to root command in `cmd/bd/main.go`
3. Implement with Cobra framework
4. Add `--json` flag for agent use
5. Add tests in `cmd/bd/*_test.go`
6. Document in README.md

### Adding Storage Features

1. Add Dolt SQL schema changes in `internal/storage/dolt/`
2. Add migration if needed
3. Update `internal/types/types.go` if new types
4. Implement in `internal/storage/dolt/` (queries, issues, etc.)
5. Add tests (both unit and embedded via `//go:build cgo`)
6. Update export/import in `cmd/bd/export.go` and `cmd/bd/import.go`

### Adding Examples

1. Create directory in `examples/`
2. Add README.md explaining the example
3. Include working code
4. Link from `examples/README.md`
5. Mention in main README.md

## Questions?

- Check existing issues: `bd list`
- Look at recent commits: `git log --oneline -20`
- Read the docs: README.md, ADVANCED.md, docs/CONFIG.md
- Create an issue if unsure: `bd create "Question: ..." -t task -p 2`

## Important Files

- **README.md** - Main documentation (keep this updated!)
- **AGENT_INSTRUCTIONS.md** - Detailed agent operational guide (authoritative)
- **ADVANCED.md** - Advanced features (rename, merge, compaction)
- **CONTRIBUTING.md** - Contribution guidelines
- **SECURITY.md** - Security policy
- **docs/CLAUDE.md** - Architecture overview

## Pro Tips for Agents

- Always use `--json` flags for programmatic use
- Link discoveries with `discovered-from` to maintain context
- Check `bd ready` before asking "what next?"
- Use `bd dolt push` to sync Dolt data to remote
- Use `bd dep tree` to understand complex dependencies
- Priority 0-1 issues are usually more important than 2-4

## Visual Design System

When adding CLI output features, follow these design principles for consistent, cognitively-friendly visuals.

### CRITICAL: No Emoji-Style Icons

**NEVER use large colored emoji icons** like 🔴🟠🟡🔵⚪ for priorities or status.
These cause cognitive overload and break visual consistency.

**ALWAYS use small Unicode symbols** with semantic colors applied via lipgloss:
- Status: `○ ◐ ● ✓ ❄`
- Priority: `●` (filled circle with color)

### Status Icons (use consistently across all commands)

```
○ open        - Available to work (white/default)
◐ in_progress - Currently being worked (yellow)
● blocked     - Waiting on dependencies (red)
✓ closed      - Completed (muted gray)
❄ deferred    - Scheduled for later (blue/muted)
```

### Priority Icons and Colors

Format: `● P0` (filled circle icon + label, colored by priority)

- **● P0**: Red + bold (critical)
- **● P1**: Orange (high)
- **● P2-P4**: Default text (normal)

### Issue Type Colors

- **bug**: Red (problems need attention)
- **epic**: Purple (larger scope)
- **Others**: Default text

### Design Principles

1. **Small Unicode symbols only** - NO emoji blobs (🔴🟠 etc.)
2. **Semantic colors only for actionable items** - Don't color everything
3. **Closed items fade** - Use muted gray to show "done"
4. **Icons > text labels** - More scannable, less cognitive load
5. **Consistency across commands** - Same icons in list, graph, show, etc.
6. **Tree connectors** - Use `├──`, `└──`, `│` for hierarchies (file explorer pattern)
7. **Reduce cognitive noise** - Don't show "needs:1" when it's just the parent epic

### Semantic Styles (internal/ui/styles.go)

Use exported styles from the `ui` package:

```go
// Status styles
ui.StatusInProgressStyle  // Yellow - active work
ui.StatusBlockedStyle     // Red - needs attention
ui.StatusClosedStyle      // Muted gray - done

// Priority styles
ui.PriorityP0Style        // Red + bold
ui.PriorityP1Style        // Orange

// Type styles
ui.TypeBugStyle           // Red
ui.TypeEpicStyle          // Purple

// General styles
ui.PassStyle, ui.WarnStyle, ui.FailStyle
ui.MutedStyle, ui.AccentStyle
ui.RenderMuted(text), ui.RenderAccent(text)
```

### Example Usage

```go
// Status icon with semantic color
switch issue.Status {
case types.StatusOpen:
    icon = "○"  // no color - available but not urgent
case types.StatusInProgress:
    icon = ui.StatusInProgressStyle.Render("◐")  // yellow
case types.StatusBlocked:
    icon = ui.StatusBlockedStyle.Render("●")     // red
case types.StatusClosed:
    icon = ui.StatusClosedStyle.Render("✓")      // muted
}
```

## Building and Testing

```bash
# Build (uses gms_pure_go tag, CGO)
make build

# Install to ~/.local/bin (canonical path)
make install

# Test (default local/CI path)
make test

# Test with coverage
go test -tags gms_pure_go -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Verify installed binary
bd init --prefix test
bd create "Test issue" -p 1
bd ready
```

> **WARNING**: Never use bare `go build -o bd` or `go install` — they miss the
> required `gms_pure_go` tag and create stale binaries. Always use `make install`.

## Release Process (Maintainers)

1. Update version in code (if applicable)
2. Update CHANGELOG.md (if exists)
3. Run full test suite
4. Tag release: `git tag v0.x.0`
5. Push tag: `git push origin v0.x.0`
6. GitHub Actions handles the rest

---

**Remember**: We're building this tool to help AI agents like you! If you find the workflow confusing or have ideas for improvement, create an issue with your feedback.

Happy coding!
