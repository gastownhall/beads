# Installing bd

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

**Why Homebrew?**
- ✅ Simple one-command install
- ✅ Automatic updates via `brew upgrade`
- ✅ No need to install Go
- ✅ Handles PATH setup automatically

### [Mise-en-place](https://mise.jdx.dev)  (macOS/Linux/Windows)

You can install beads using mise from the latest GitHub release:

```bash
mise install github:gastownhall/beads
mise use -g github:gastownhall/beads
```

**NOTE**: The `-g` enables beads globally.  To enable project-specific versions, omit that.

**Why Mise?**
- ✅ Same as Homebrew: simple, updates via `mise up`, works without Go, handles PATH
- ✅ Supports all platforms
- ✅ Always the latest release
- ✅ May optionally use a different release version for specific projects

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
| **npm** | JS/Node.js projects | `npm update -g @beads/bd` | Node.js | Convenient if npm is your ecosystem |
| **bun** | JS/Bun.js projects | `bun install -g --trust @beads/bd` | Bun.js | Convenient if bun is your ecosystem |
| **Install script** | Quick setup, CI/CD | Re-run script | curl, bash | Good for automation and one-liners |
| **go install (nocgo)** | Go developers, simplest install | Re-run command | Go 1.24+ | **Server-mode only** (no embedded Dolt) |
| **go install (cgo)** | Go developers wanting embedded mode | Re-run command | Go 1.24+, C compiler | Full embedded-Dolt support |
| **From source** | Contributors only | `git pull && go build` | Go, git | Full control, can modify code |
| **AUR (Arch)** | Arch Linux users | `yay -Syu` | yay/paru | Community-maintained |

**TL;DR:** Use Homebrew if available. Use npm if you're in a Node.js environment. Use the script for quick one-off installs or CI.

### A note on `go install` capability

`go install` supports **two build modes** that give different capabilities:

- **Nocgo (simplest, default in this doc):** `CGO_ENABLED=0 go install ...`. Works on any machine with a Go toolchain, no C compiler needed. Produces a **server-mode-only** binary — you must run an external `dolt sql-server` and use `bd init --server`. See [DOLT.md](DOLT.md) for server-mode setup.
- **Cgo (embedded-capable):** `CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install ...`. Requires a C compiler (gcc/clang on Unix, MinGW on Windows). Produces a binary with the default embedded-Dolt backend — `bd init` Just Works.

If you don't have a preference, `brew install beads` / `install.sh` give you the embedded-capable build with no fuss.

## Platform-Specific Installation

### macOS

**Via Homebrew** (recommended):
```bash
brew install beads
```

**Via go install** (server-mode only, simplest):
```bash
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
# Then: bd init --server   (requires a running dolt sql-server)
```

**Via go install** (embedded-capable, needs Xcode CLI tools):
```bash
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

**From source**:
```bash
git clone https://github.com/gastownhall/beads
cd beads
make build   # uses gms_pure_go tag and CGO
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

Thanks to [@v4rgas](https://github.com/v4rgas) for maintaining the AUR package!

**Via go install** (server-mode only, simplest):
```bash
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
# Then: bd init --server   (requires a running dolt sql-server)
```

**Via go install** (embedded-capable, needs gcc):
```bash
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

**From source**:
```bash
git clone https://github.com/gastownhall/beads
cd beads
make build   # uses gms_pure_go tag and CGO
sudo mv bd /usr/local/bin/
```

### FreeBSD

**Via Quick Install Script**:
```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

**Via go install** (server-mode only, simplest):
```bash
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
```

### Windows 11

Beads now ships with native Windows support—no MSYS or MinGW required.

**Prerequisites:**
- [Go 1.24+](https://go.dev/dl/) installed (add `%USERPROFILE%\go\bin` to your `PATH`)
- Git for Windows

**Via PowerShell script**:
```pwsh
irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex
```

The script installs a prebuilt Windows release if available and verifies the downloaded ZIP checksum against release `checksums.txt`. Go is only required for `go install` or building from source.

**Via go install** (server-mode only, simplest):
```pwsh
$env:CGO_ENABLED="0"; go install github.com/steveyegge/beads/cmd/bd@latest
# Then: bd init --server   (requires a running dolt sql-server)
```

This produces a server-mode-only binary with no C compiler requirement — the fastest path to a working `bd` on Windows.

**Via go install** (embedded-capable, needs MinGW):
```pwsh
$env:CGO_ENABLED="1"; $env:GOFLAGS="-tags=gms_pure_go"; go install github.com/steveyegge/beads/cmd/bd@latest
```

Requires MinGW-w64 gcc on your PATH. ICU is **not** required — `gms_pure_go` selects Go's stdlib `regexp`.

**From source**:
```pwsh
git clone https://github.com/gastownhall/beads
cd beads
make build   # uses gms_pure_go tag and CGO
Move-Item bd.exe $env:USERPROFILE\AppData\Local\Microsoft\WindowsApps\
```

The `-tags gms_pure_go` flag tells go-mysql-server to use Go's stdlib regexp instead of ICU.

**Verify installation**:
```pwsh
bd version
```

**Windows notes:**
- The Dolt server listens on a loopback TCP endpoint
- Allow `bd.exe` loopback traffic through any host firewall

## Build Dependencies (Contributors Only)

> **Note:** These dependencies are only needed if you build from source. If you installed via Homebrew, npm, or the install script, skip this section entirely.

Building from source requires a C compiler (for CGO / embedded Dolt). **ICU is
not required** -- all builds use the `gms_pure_go` tag which selects Go's
stdlib `regexp` instead of ICU regex. See [ICU-POLICY.md](ICU-POLICY.md) for
details.

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

> **For maintainers only:** If you intentionally need to run
> `scripts/test-icu-path.sh` (which exercises the leftover ICU code path),
> install ICU headers:
> `brew install icu4c` (macOS) or `sudo apt-get install -y libicu-dev` (Linux).
> This is not needed for normal development.

## IDE and Editor Integrations

### CLI + Hooks (Recommended for Claude Code)

**The recommended approach** for Claude Code, Cursor, Windsurf, and other editors with shell access:

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
bd setup codex    # Codex CLI - creates/updates AGENTS.md
bd setup mux      # Mux - creates/updates AGENTS.md
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
- **More sustainable** - Less compute per request

**Verify installation:**
```bash
bd setup claude --check   # Check Claude Code integration
bd setup cursor --check   # Check Cursor integration
bd setup aider --check    # Check Aider integration
bd setup codex --check    # Check Codex integration
bd setup mux --check      # Check Mux integration
```

### Claude Code Plugin (Optional)

For enhanced UX with slash commands:

```bash
# In Claude Code
/plugin marketplace add gastownhall/beads
/plugin install beads
# Restart Claude Code
```

The plugin adds:
- Slash commands: `/beads:ready`, `/beads:create`, `/beads:show`, `/beads:update`, `/beads:close`, etc.
- Task agent for autonomous execution

See [PLUGIN.md](PLUGIN.md) for complete plugin documentation.

### GitHub Copilot (VS Code)

For VS Code with GitHub Copilot:

1. **Install beads-mcp:**
   ```bash
   uv tool install beads-mcp
   ```

2. **Configure MCP** - Create `.vscode/mcp.json` in your project:
   ```json
   {
     "servers": {
       "beads": {
         "command": "beads-mcp"
       }
     }
   }
   ```

   **For all projects:** Add to VS Code user-level MCP config:

   | Platform | Path |
   |----------|------|
   | macOS | `~/Library/Application Support/Code/User/mcp.json` |
   | Linux | `~/.config/Code/User/mcp.json` |
   | Windows | `%APPDATA%\Code\User\mcp.json` |

   ```json
   {
     "servers": {
       "beads": {
         "command": "beads-mcp",
         "args": []
       }
     }
   }
   ```

3. **Initialize project:**
   ```bash
   bd init --quiet
   ```

4. **Reload VS Code**

See [COPILOT_INTEGRATION.md](COPILOT_INTEGRATION.md) for complete setup guide.

### MCP Server (Alternative - for MCP-only environments)

**Use MCP only when CLI is unavailable** (Claude Desktop, Sourcegraph Amp without shell):

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

**Configuration for Sourcegraph Amp**:

Add to your MCP settings:

```json
{
  "beads": {
    "command": "beads-mcp",
    "args": []
  }
}
```

**Trade-offs:**
- ✅ Works in MCP-only environments
- ❌ Higher context overhead (MCP schemas add 10-50k tokens)
- ❌ Additional latency from MCP protocol

See [integrations/beads-mcp/README.md](../integrations/beads-mcp/README.md) for detailed MCP server documentation.

## Verifying Installation

After installing, verify bd is working:

```bash
bd version
bd help
```

## Troubleshooting Installation

For additional troubleshooting, see [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

### `bd: command not found`

bd is not in your PATH. Either:

```bash
# Check if installed
go list -f {{.Target}} github.com/steveyegge/beads/cmd/bd

# Add Go bin to PATH (add to ~/.bashrc or ~/.zshrc)
export PATH="$PATH:$(go env GOPATH)/bin"

# Or reinstall (server-mode only, no C compiler needed)
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
```

### `zsh: killed bd` or crashes on macOS

Some users report crashes when running `bd init` or other commands on macOS. This is typically caused by CGO/SQLite compatibility issues.

**Workaround:**
```bash
# Install an embedded-capable build
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest

# Or if building from source
git clone https://github.com/gastownhall/beads
cd beads
CGO_ENABLED=1 go build -tags gms_pure_go -o bd ./cmd/bd
sudo mv bd /usr/local/bin/
```

If you installed via Homebrew, this shouldn't be necessary as the formula already enables CGO. If you're still seeing crashes with the Homebrew version, please [file an issue](https://github.com/gastownhall/beads/issues).

### Claude Code Plugin: MCP server fails to start

If the Claude Code plugin's MCP server fails immediately after installation, it's likely that `uv` is not installed or not in your PATH.

**Symptoms:**
- Plugin slash commands work, but MCP tools are unavailable
- Error logs show `command not found: uv`
- Server fails silently on startup

**Solution:**
```bash
# Install uv
curl -LsSf https://astral.sh/uv/install.sh | sh

# Restart your shell or update PATH
source ~/.local/bin/env

# Verify uv is available
which uv

# Restart Claude Code
```

See the "Claude Code Plugin" section above for alternative installation methods (Homebrew, pip).

## Next Steps

After installation:

1. **Initialize a project**: `cd your-project && bd init`
2. **Configure your agent**: Add bd instructions to `AGENTS.md` (see [README.md](../README.md#quick-start))
3. **Learn the basics**: See [QUICKSTART.md](QUICKSTART.md) for a tutorial
4. **Explore examples**: Check out the [examples/](../examples/) directory

## Updating bd

Use the update command that matches how you installed `bd`.

### Quick Install Script (macOS/Linux/FreeBSD)

```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### PowerShell Installer (Windows)

```pwsh
irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex
```

### Homebrew

```bash
brew upgrade beads
```

### npm

```bash
npm update -g @beads/bd
```

### bun

```bash
bun install -g --trust @beads/bd
```

### go install

Use whichever mode you installed with originally:

```bash
# Server-mode only
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest

# Embedded-capable
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

### From source

```bash
cd beads
git pull
make build
sudo mv bd /usr/local/bin/
```

## After Upgrading (Recommended)

```bash
bd info --whats-new
bd hooks install
bd version
```

## Uninstalling

To completely remove Beads from a repository, see [UNINSTALLING.md](UNINSTALLING.md).
