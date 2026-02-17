# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**beads** (command: `bd`) is a git-backed issue tracker for AI-supervised coding workflows. We dogfood our own tool.

**IMPORTANT**: See [AGENTS.md](../AGENTS.md) for complete workflow instructions, bd commands, and development guidelines.

## Architecture Overview

### Three-Layer Design

1. **Storage Layer** (`internal/storage/`)
   - **Dolt** in `storage/dolt/` — version-controlled SQL database with cell-level merge
   - Common types and interfaces in `storage.go`

2. **RPC Layer** (`internal/rpc/`)
   - Client/server architecture using Unix domain sockets (Windows named pipes)
   - Protocol defined in `protocol.go`
   - Server split into focused files: `server_core.go`, `server_issues_epics.go`, `server_labels_deps_comments.go`, etc.
   - Used by Dolt server mode for multi-writer access

3. **CLI Layer** (`cmd/bd/`)
   - Cobra-based commands (one file per command: `create.go`, `list.go`, etc.)
   - Direct database access (Dolt embedded or server mode)
   - All commands support `--json` for programmatic use
   - Main entry point in `main.go`

### Storage Architecture

Beads uses **Dolt** as its storage backend — a version-controlled SQL database:

```
Dolt DB (.beads/dolt/)
    ↕ Dolt commits (automatic per write)
    ↕ JSONL export (git hooks, for portability)
JSONL (.beads/issues.jsonl, git-tracked)
    ↕ git push/pull
Remote (shared across machines)
```

- **Write path**: CLI → Dolt → auto-commit to Dolt history
- **Read path**: Direct SQL queries against Dolt
- **Sync**: JSONL maintained via git hooks for portability; Dolt handles versioning natively
- **Hash-based IDs**: Automatic collision prevention (v0.20+)

Core implementation:
- Dolt storage: `internal/storage/dolt/`
- Export: `cmd/bd/export.go`
- Import: `cmd/bd/import.go`
- Sync: `cmd/bd/sync_helpers.go`, `cmd/bd/sync_git.go`

### Key Data Types

See `internal/types/types.go`:
- `Issue`: Core work item (title, description, status, priority, etc.)
- `Dependency`: Four types (blocks, related, parent-child, discovered-from)
- `Label`: Flexible tagging system
- `Comment`: Threaded discussions
- `Event`: Full audit trail

## Common Development Commands

```bash
# Build and test
go build -o bd ./cmd/bd
go test ./...
go test -coverprofile=coverage.out ./...

# Run linter (baseline warnings documented in docs/LINTING.md)
golangci-lint run ./...

# Version management
./scripts/bump-version.sh 0.9.3 --commit

# Local testing
./bd init --prefix test
./bd create "Test issue" -p 1
./bd ready
```

## Testing Philosophy

- Unit tests live next to implementation (`*_test.go`)
- Integration tests use real Dolt databases (via embedded server in temp dirs)
- Script-based tests in `cmd/bd/testdata/*.txt` (see `scripttest_test.go`)
- RPC layer has extensive isolation and edge case coverage

## Important Notes

- **Always read AGENTS.md first** - it has the complete workflow
- Install git hooks for JSONL sync: `bd hooks install`
- Run `bd sync` at end of agent sessions to sync with remote
- Check for duplicates proactively: `bd duplicates --auto-merge`
- Use `--json` flags for all programmatic use

## Key Files

- **AGENTS.md** - Complete workflow and development guide (READ THIS!)
- **README.md** - User-facing documentation
- **ADVANCED.md** - Advanced features (rename, merge, compaction)
- **EXTENDING.md** - How to add custom tables to the database
- **LABELS.md** - Complete label system guide
- **CONFIG.md** - Configuration system

## When Adding Features

See AGENTS.md "Adding a New Command" and "Adding Storage Features" sections for step-by-step guidance.
