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
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

The installer will:
- Detect your platform (macOS/Linux/FreeBSD, amd64/arm64)
- Fall back to the supported `go install` modes if Go is available
- Fall back to building from source if needed
- Guide you through PATH setup if necessary

## Go Install and Build Dependencies

Use Homebrew, npm, or the install script if you do not specifically need `go install`.

`go install` has two supported modes:

- **Server-mode only:** `CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest`
- **Embedded-capable:** `CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest`

ICU headers are not required. The embedded-capable command uses `gms_pure_go` so go-mysql-server uses Go's stdlib regexp instead of ICU.

## Platform-Specific Installation

### macOS

**Via Homebrew** (recommended):
```bash
brew install beads
```

**Via go install** (server-mode only):
```bash
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
```

**Via go install** (embedded-capable):
```bash
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

**From source**:
```bash
git clone https://github.com/gastownhall/beads
cd beads
make build
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

**Via go install** (server-mode only):
```bash
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
```

**Via go install** (embedded-capable):
```bash
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

### FreeBSD

**Via quick install script**:
```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

**Via go install** (server-mode only):
```bash
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
```

### Windows 11

Beads ships with native Windows support—no MSYS or MinGW required.

**Prerequisites:**
- [Go 1.24+](https://go.dev/dl/) installed (add `%USERPROFILE%\go\bin` to your `PATH`)
- Git for Windows

**Via PowerShell script**:
```pwsh
irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex
```

The script installs a prebuilt Windows release if available. Go is only required for `go install` or building from source.

**Via go install** (server-mode only):
```pwsh
$env:CGO_ENABLED="0"; go install github.com/steveyegge/beads/cmd/bd@latest
```

**Via go install** (embedded-capable):
```pwsh
$env:CGO_ENABLED="1"; $env:GOFLAGS="-tags=gms_pure_go"; go install github.com/steveyegge/beads/cmd/bd@latest
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

# Or reinstall with the recommended installer
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### `zsh: killed bd` or crashes on macOS

This is typically caused by CGO/SQLite compatibility issues:

```bash
# Install an embedded-capable build
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

## Updating bd

### Quick install script (macOS/Linux/FreeBSD)

```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### PowerShell installer (Windows)

```pwsh
irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex
```

### Homebrew

```bash
brew upgrade beads
```

### go install

```bash
# Server-mode only
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest

# Embedded-capable
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

For post-upgrade steps (hooks, migrations), see [Upgrading](/getting-started/upgrading).

## Next Steps

After installation:

1. **Initialize a project**: `cd your-project && bd init`
2. **Learn the basics**: See [Quick Start](/getting-started/quickstart)
3. **Configure your agent**: See [IDE Setup](/getting-started/ide-setup)
