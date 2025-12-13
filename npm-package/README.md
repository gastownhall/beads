# @beads/bd - Beads Issue Tracker

[![npm version](https://img.shields.io/npm/v/@beads/bd)](https://www.npmjs.com/package/@beads/bd)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**Give your coding agent a memory upgrade**

Beads is a lightweight memory system for coding agents, using a graph-based issue tracker. This npm package provides easy installation of the native bd binary for Node.js environments, including Claude Code for Web.

## Installation

```bash
npm install -g @beads/bd
```

Or as a project dependency:

```bash
npm install --save-dev @beads/bd
```

## What is Beads?

Beads is an issue tracker designed specifically for AI coding agents. It provides:

- ✨ **Zero setup** - `bd init` creates project-local database
- 🔗 **Dependency tracking** - Four dependency types (blocks, related, parent-child, discovered-from)
- 📋 **Ready work detection** - Automatically finds issues with no open blockers
- 🤖 **Agent-friendly** - `--json` flags for programmatic integration
- 📦 **Git-versioned** - JSONL records stored in git, synced across machines
- 🌍 **Distributed by design** - Share one logical database via git

## Quick Start

After installation, initialize beads in your project:

```bash
bd init
```

Then tell your AI agent to use bd for task tracking instead of markdown:

```bash
echo "Use 'bd' commands for issue tracking instead of markdown TODOs" >> AGENTS.md
```

Your agent will automatically:
- Create and track issues during work
- Manage dependencies between tasks
- Find ready work with `bd ready`
- Keep long-term context across sessions

## Common Commands

```bash
# Find ready work
bd ready --json

# Create an issue
bd create "Fix bug" -t bug -p 1

# Show issue details
bd show bd-a1b2

# List all issues
bd list --json

# Update status
bd update bd-a1b2 --status in_progress

# Add dependency
bd dep add bd-f14c bd-a1b2

# Close issue
bd close bd-a1b2 --reason "Fixed"
```

## Claude Code for Web Integration

To auto-install bd in Claude Code for Web sessions, add to your SessionStart hook:

```bash
# .claude/hooks/session-start.sh
npm install -g @beads/bd
bd init --quiet
```

This ensures bd is available in every new session without manual setup.

## Platform Support

This package downloads the appropriate native binary for your platform:

- **macOS**: darwin-amd64, darwin-arm64
- **Linux**: linux-amd64, linux-arm64
- **Windows**: windows-amd64

## Full Documentation

For complete documentation, see the [beads GitHub repository](https://github.com/steveyegge/beads):

- [Complete README](https://github.com/steveyegge/beads#readme)
- [Quick Start Guide](https://github.com/steveyegge/beads/blob/main/docs/QUICKSTART.md)
- [Installation Guide](https://github.com/steveyegge/beads/blob/main/docs/INSTALLING.md)
- [FAQ](https://github.com/steveyegge/beads/blob/main/docs/FAQ.md)
- [Troubleshooting](https://github.com/steveyegge/beads/blob/main/docs/TROUBLESHOOTING.md)

## Why npm Package vs WASM?

This npm package wraps the native bd binary rather than using WebAssembly because:

- ✅ Full SQLite support (no custom VFS needed)
- ✅ All features work identically to native bd
- ✅ Better performance (native vs WASM overhead)
- ✅ Simpler maintenance

## License

MIT - See [LICENSE](LICENSE) for details.

## Support

- [GitHub Issues](https://github.com/steveyegge/beads/issues)
- [Documentation](https://github.com/steveyegge/beads)
