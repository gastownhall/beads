---
id: installation
title: Installation
sidebar_position: 1
---

# Installing bd

Complete installation guide for all platforms.

## Quick Install (Recommended)

### Homebrew (macOS/Linux)

```bash
brew install beads
```

**Why Homebrew?**
- Simple one-command install
- Automatic updates via `brew upgrade`
- No need to install Go
- Handles PATH setup automatically

### Quick Install Script (All Platforms)

```bash
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
```

The installer will:
- Detect your platform (macOS/Linux/FreeBSD, amd64/arm64)
- Install via `go install` if Go is available
- Fall back to building from source if needed
- Guide you through PATH setup if necessary

## Platform-Specific Installation

### macOS

**Via Homebrew** (recommended):
```bash
brew install beads
```

**Via go install**:
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
```

**From source**:
```bash
git clone https://github.com/steveyegge/beads
cd beads
go build -o bd ./cmd/bd
sudo mv bd /usr/local/bin/
```

### Linux

**Via Homebrew** (works on Linux too):
```bash
brew install beads
```

**Arch Linux** (AUR):
```bash
# Install from AUR
yay -S beads-git
# or
paru -S beads-git
```

**Via go install**:
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
```

### FreeBSD

**Via quick install script**:
```bash
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
```

**Via go install**:
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
```

### Windows 11

Beads ships with native Windows support—no MSYS or MinGW required.

**Prerequisites:**
- [Go 1.24+](https://go.dev/dl/) installed (add `%USERPROFILE%\go\bin` to your `PATH`)
- Git for Windows

**Via PowerShell script**:
```pwsh
irm https://raw.githubusercontent.com/steveyegge/beads/main/install.ps1 | iex
```

**Via go install**:
```pwsh
go install github.com/steveyegge/beads/cmd/bd@latest
```

## IDE and Editor Integrations

### CLI + Hooks (Recommended)

The recommended approach for Claude Code, Cursor, Windsurf, and other editors with shell access:

```bash
# 1. Install bd CLI (see Quick Install above)
brew install beads

# 2. Initialize in your project
cd your-project
bd init --quiet

# 3. Setup editor integration (choose one)
bd setup claude   # Claude Code - installs SessionStart/PreCompact hooks
bd setup cursor   # Cursor IDE - creates .cursor/rules/beads.mdc
bd setup aider    # Aider - creates .aider.conf.yml
```

**How it works:**
- Editor hooks/rules inject `bd prime` automatically on session start
- `bd prime` provides ~1-2k tokens of workflow context
- You use `bd` CLI commands directly
- Git hooks (installed by `bd init`) auto-sync the database

**Why this is recommended:**
- **Context efficient** - ~1-2k tokens vs 10-50k for MCP tool schemas
- **Lower latency** - Direct CLI calls, no MCP protocol overhead
- **Universal** - Works with any editor that has shell access

### MCP Server (Alternative)

Use MCP only when CLI is unavailable (Claude Desktop, Sourcegraph Amp without shell):

```bash
# Using uv (recommended)
uv tool install beads-mcp

# Or using pip
pip install beads-mcp
```

**Configuration for Claude Desktop** (macOS):

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

## Verifying Installation

After installing, verify bd is working:

```bash
bd version
bd help
```

## Troubleshooting

### `bd: command not found`

bd is not in your PATH:

```bash
# Check if installed
go list -f {{.Target}} github.com/steveyegge/beads/cmd/bd

# Add Go bin to PATH (add to ~/.bashrc or ~/.zshrc)
export PATH="$PATH:$(go env GOPATH)/bin"
```

### `zsh: killed bd` or crashes on macOS

This is typically caused by CGO/SQLite compatibility issues:

```bash
# Build with CGO enabled
CGO_ENABLED=1 go install github.com/steveyegge/beads/cmd/bd@latest
```

## Updating bd

### Homebrew

```bash
brew upgrade bd
```

### go install

```bash
go install github.com/steveyegge/beads/cmd/bd@latest
```

## Next Steps

After installation:

1. **Initialize a project**: `cd your-project && bd init`
2. **Learn the basics**: See [Quick Start](/getting-started/quickstart)
3. **Configure your agent**: See [IDE Setup](/getting-started/ide-setup)
