# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**beads** (command: `bd`) is a Dolt-powered issue tracker for AI-supervised coding workflows. Git integration is optional — see `BEADS_DIR` + `--stealth` for git-free operation. We dogfood our own tool.

**IMPORTANT**: See [AGENTS.md](../AGENTS.md) for complete workflow instructions, bd commands, and development guidelines.

## Architecture Overview

### Three-Layer Design

1. **Storage Layer** (`internal/storage/`)
   - **Dolt** in `storage/dolt/` — version-controlled SQL database with cell-level merge
   - Common types and interfaces in `storage.go`

2. **Database Runtime Layer**
   - Embedded mode runs Dolt in-process through `internal/storage/embeddeddolt/`
   - Server mode uses `internal/doltserver/` and `internal/storage/db/`
   - Proxy and pidfile helpers live under `internal/storage/db/`
   - Storage-facing server adapters live under `internal/storage/doltserver/`

3. **CLI Layer** (`cmd/bd/`)
   - Cobra-based commands (one file per command: `create.go`, `list.go`, etc.)
   - Direct database access (embedded mode for standalone, server mode for orchestrator)
   - All commands support `--json` for programmatic use
   - Main entry point in `main.go`

### Storage Architecture

Beads uses **Dolt** as its storage backend — a version-controlled SQL database:

```
Dolt DB (.beads/dolt/)
    ↕ Dolt commits (automatic per write)
    ↕ Dolt push/pull (native sync)
Remote (Dolt remotes: DoltHub, S3, GCS, etc.)
```

- **Write path**: CLI → Dolt → auto-commit to Dolt history
- **Read path**: Direct SQL queries against Dolt
- **Sync**: Dolt handles versioning and sync natively via `bd dolt push` / `bd dolt pull`
- **Hash-based IDs**: Automatic collision prevention (v0.20+)

Core implementation:
- Dolt storage: `internal/storage/dolt/`
- Embedded runtime: `internal/storage/embeddeddolt/`
- Server runtime: `internal/doltserver/`, `internal/storage/db/`, and `internal/storage/doltserver/`
- Sync commands: `cmd/bd/dolt_*.go`, `cmd/bd/sync_*.go`

### Key Data Types

See `internal/types/types.go`:
- `Issue`: Core work item (title, description, status, priority, etc.)
- `Dependency`: Four types (blocks, related, parent-child, discovered-from)
- `Label`: Flexible tagging system
- `Comment`: Threaded discussions
- `Event`: Full audit trail

## Development Command Source

Use [AGENT_INSTRUCTIONS.md](../AGENT_INSTRUCTIONS.md#testing-commands-no-ambiguity)
for the current command policy. This file should not duplicate command matrices
or version-management workflows.

> **Do NOT** use `go build -o bd` or `go install` directly — they create
> stale binaries that shadow `~/.local/bin/bd`. Always use `make install`.

## Testing Philosophy

Testing guidance lives in [TESTING.md](TESTING.md) and
[TESTING_PHILOSOPHY.md](TESTING_PHILOSOPHY.md). Architecture-specific notes for
Claude are limited to where tests touch agent setup, hooks, or instruction-file
generation.

## Important Notes

- **Always read AGENTS.md first** - it has the complete workflow
- Check for duplicates proactively: `bd duplicates --auto-merge`
- Use `--json` flags for all programmatic use

## Key Files

- **AGENTS.md** - Complete workflow and development guide (READ THIS!)
- **README.md** - User-facing documentation
- **ADVANCED.md** - Advanced features (rename, merge, compaction)
- **docs/LABELS.md** - Complete label system guide
- **docs/CONFIG.md** - Configuration system

## When Adding Features

See AGENTS.md "Adding a New Command" and "Adding Storage Features" sections for step-by-step guidance.
