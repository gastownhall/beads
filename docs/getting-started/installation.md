---
title: Installation
---

Complete installation guide for all platforms.

## Components Overview

Beads has several components - here's what they are and when you need them:

| Component | What It Is | When You Need It |
|-----------|------------|------------------|
| **bd CLI** | Core command-line tool | Always - this is the foundation |
| **Claude Code Plugin** | Slash commands + enhanced UX | Optional - if you want `/beads:ready`, `/beads:create` commands |
| **MCP Server (beads-mcp)** | Model Context Protocol interface | Only for MCP-only environments (Claude Desktop, Amp) |

**How they relate:**
- The **bd CLI** is the core - install it first via Homebrew, npm, or script
- The **Plugin** enhances Claude Code with slash commands but *requires* the CLI installed
- The **MCP server** is an *alternative* to the CLI for environments without shell access

**Important:** Beads is installed system-wide, not cloned into your project. The `.beads/` directory in your project only contains the issue database.

**Typical setups:**

| Environment | What to Install |
|-------------|-----------------|
| Claude Code, Cursor, Windsurf | bd CLI (+ optional Plugin for Claude Code) |
| GitHub Copilot (VS Code) | bd CLI + MCP server |
| Claude Desktop (no shell) | MCP server only |
| Terminal / scripts | bd CLI only |
| CI/CD pipelines | bd CLI only |

**Are they mutually exclusive?** No - you can have CLI + Plugin + MCP all installed. They don't conflict. But most users only need the CLI.

## Quick Install (Recommended)

### Homebrew (macOS/Linux)

```bash
brew install beads
```

Homebrew core's `beads` formula is the supported Homebrew package. If you
previously installed the old tap formula as `bd`, see
[Migrating from the old Homebrew tap](/getting-started/upgrading#homebrew) to
switch to the core formula.

**Why Homebrew?**
- Simple one-command install
- Automatic updates via `brew upgrade`
- No need to install Go
- Handles PATH setup automatically

### Mise-en-place (macOS/Linux/Windows)

You can install beads using [mise](https://mise.jdx.dev) from the latest GitHub release:

```bash
mise install github:gastownhall/beads
mise use -g github:gastownhall/beads
```

The `-g` enables beads globally. To enable project-specific versions, omit it.

**Why Mise?**
- Same as Homebrew: simple, updates via `mise up`, works without Go, handles PATH
- Supports all platforms
- Always the latest release
- May optionally use a different release version for specific projects

Mise's Go backend follows the same caveats as `go install`; prefer the release backend above.

### Quick Install Script (All Platforms)

```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

The installer will:
- Detect your platform (macOS/Linux/FreeBSD, amd64/arm64)
- Verify downloaded release archives against release `checksums.txt`
- Fall back to the supported `go install` modes if Go is available
- Fall back to building from source if needed
- Guide you through PATH setup if necessary

On macOS, the script preserves the downloaded binary signature by default. If you explicitly want ad-hoc local re-signing, opt in:

```bash
BEADS_INSTALL_RESIGN_MACOS=1 curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### Comparison of Installation Methods

| Method | Best For | Updates | Prerequisites | Notes |
|--------|----------|---------|---------------|-------|
| **Homebrew** | macOS/Linux users | `brew upgrade beads` | Homebrew | Recommended. Handles everything automatically |
| **Mise** | All platforms | `mise up` | mise | Installs the latest GitHub release |
| **npm** | JS/Node.js projects | `npm update -g @beads/bd` | Node.js | Convenient if npm is your ecosystem |
| **bun** | JS/Bun.js projects | `bun install -g --trust @beads/bd` | Bun.js | Convenient if bun is your ecosystem |
| **Install script** | Quick setup, CI/CD | Re-run script | curl, bash | Good for automation and one-liners |
| **go install (nocgo)** | Go developers, simplest install | Re-run command | Go 1.24+ | **Server-mode only** (no embedded Dolt) |
| **go install (cgo)** | Go developers wanting embedded mode | Re-run command | Go 1.24+, C compiler | Full embedded-Dolt support |
| **From source** | Contributors only | `git pull && go build` | Go, git | Full control, can modify code |
| **AUR (Arch)** | Arch Linux users | `yay -Syu` | yay/paru | Community-maintained |

**TL;DR:** Use Homebrew if available. Use npm if you're in a Node.js environment. Use the script for quick one-off installs or CI.

## Go Install and Build Dependencies

Use Homebrew, npm, or the install script if you do not specifically need `go install`.

`go install` has two supported modes that give different capabilities:

- **Server-mode only (nocgo, simplest):** `CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest`. Works on any machine with a Go toolchain, no C compiler needed. Produces a server-mode-only binary — you must run an external `dolt sql-server` and use `bd init --server`. See [Dolt](/architecture/dolt) for server-mode setup.
- **Embedded-capable (cgo):** `CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest`. Requires a C compiler (gcc/clang on Unix, MinGW on Windows). Produces a binary with the default embedded-Dolt backend — `bd init` Just Works.

ICU headers are not required. The embedded-capable command uses `gms_pure_go` so go-mysql-server uses Go's stdlib regexp instead of ICU.

Use the `github.com/steveyegge/beads` path for `go install`. The repository now lives under `gastownhall/beads`, but released Go modules still declare `github.com/steveyegge/beads` for compatibility.

If you don't have a preference, `brew install beads` or the install script give you the embedded-capable build with no fuss.

### Build Dependencies (Contributors Only)

<Note>
These dependencies are only needed if you build from source. If you installed via Homebrew, npm, or the install script, skip this section entirely.
</Note>

Building from source requires a C compiler (for CGO / embedded Dolt). ICU is
not required — all builds use the `gms_pure_go` tag which selects Go's
stdlib `regexp` instead of ICU regex. See
[ICU-POLICY.md](https://github.com/gastownhall/beads/blob/main/engdocs/ICU-POLICY.md)
for details.

macOS (Homebrew):
```bash
brew install zstd
```

Linux (Debian/Ubuntu):
```bash
sudo apt-get install -y libzstd-dev
```

Linux (Fedora/RHEL):
```bash
sudo dnf install -y libzstd-devel
```

For maintainers only: if you intentionally need to run
[scripts/test-icu-path.sh](https://github.com/gastownhall/beads/blob/main/scripts/test-icu-path.sh)
(which exercises the leftover ICU code path), install ICU headers:
`brew install icu4c` (macOS) or `sudo apt-get install -y libicu-dev` (Linux).
This is not needed for normal development.

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
bd setup claude   # Claude Code - installs SessionStart hooks
bd setup cursor   # Cursor IDE - creates .cursor/rules/beads.mdc
bd setup aider    # Aider - creates .aider.conf.yml
bd setup codex    # Codex CLI - installs Beads skill, AGENTS.md guidance, and native hooks
bd setup factory  # Factory.ai Droid - creates/updates AGENTS.md
bd setup mux      # Mux - creates/updates AGENTS.md
```

**How it works:**
- `bd init` creates or updates `AGENTS.md` and installs project Claude/Codex integrations by default unless you use `--skip-agents` or `--stealth`
- Editor hooks/rules inject `bd prime` automatically on session start
- Codex 0.129.0+ uses native `/hooks` for startup and compaction-aware context refresh
- `bd prime` provides ~1-2k tokens of workflow context
- You use `bd` CLI commands directly
- Git hooks (installed by `bd init`) refresh exports and legacy fallbacks; `bd dolt push/pull` syncs the database
- `bd onboard` prints the small manual snippet for unsupported agents or custom instruction files

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

Upgrade checklist:

1. With your current `bd`, sync remote-backed databases before installing the
   new binary:
   `bd dolt push`
   `bd dolt pull`
2. Back up before migration:
   `bd export --all -o .beads/backup/pre-migrate-$(date +%Y%m%d).jsonl`
3. Upgrade using the command for your install method below.
4. After upgrading:
   `bd info --whats-new`
   `bd hooks install`
   `bd version`
5. If crossing a schema migration on a remote-backed database, only the
   designated migrator runs:
   `bd migrate --force`
   `bd dolt push`

Other clones should install the new binary and run `bd bootstrap`, not
independently migrate. For the full procedure, see [Upgrading](/getting-started/upgrading).

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
3. **Configure your agent**: See [IDE Setup](/getting-started/ide-setup), or run `bd setup --list`
