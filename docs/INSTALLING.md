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

### Quick Install Script (All Platforms)

```bash
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
```

The installer will:
- Detect your platform (macOS/Linux/FreeBSD, amd64/arm64)
- Install via `go install` if Go is available
- Fall back to building from source if needed
- Guide you through PATH setup if necessary

### Comparison of Installation Methods

| Method | Best For | Updates | Prerequisites | Notes |
|--------|----------|---------|---------------|-------|
| **Homebrew** | macOS/Linux users | `brew upgrade beads` | Homebrew | Recommended. Handles everything automatically |
| **npm** | JS/Node.js projects | `npm update -g @beads/bd` | Node.js | Convenient if npm is your ecosystem |
| **bun** | JS/Bun.js projects | `bun install -g --trust @beads/bd` | Bun.js | Convenient if bun is your ecosystem |
| **Install script** | Quick setup, CI/CD | Re-run script | curl, bash | Good for automation and one-liners |
| **go install** | Go developers | Re-run command | Go 1.24+ | Builds from source, always latest |
| **From source** | Contributors, custom builds | `git pull && go build` | Go, git | Full control, can modify code |
| **AUR (Arch)** | Arch Linux users | `yay -Syu` | yay/paru | Community-maintained |

**TL;DR:** Use Homebrew if available. Use npm if you're in a Node.js environment. Use the script for quick one-off installs or CI.

## Build Dependencies (go install / from source)

If you install via `go install` or build from source, you need system dependencies for CGO:

macOS (Homebrew):
```bash
brew install icu4c zstd
```

Linux (Debian/Ubuntu):
```bash
sudo apt-get install -y libicu-dev libzstd-dev
```

Linux (Fedora/RHEL):
```bash
sudo dnf install -y libicu-devel libzstd-devel
```

If you see `unicode/uregex.h` missing on macOS, `icu4c` is keg-only. Use:
```bash
ICU_PREFIX="$(brew --prefix icu4c)"
CGO_CFLAGS="-I${ICU_PREFIX}/include" CGO_CPPFLAGS="-I${ICU_PREFIX}/include" CGO_LDFLAGS="-L${ICU_PREFIX}/lib" go install github.com/steveyegge/beads/cmd/bd@latest
```

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

Thanks to [@v4rgas](https://github.com/v4rgas) for maintaining the AUR package!

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

### FreeBSD

**Via Quick Install Script**:
```bash
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
```

**Via go install**:
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
```

### Windows 11

Beads now ships with native Windows support—no MSYS or MinGW required.

**Prerequisites:**
- [Go 1.24+](https://go.dev/dl/) installed (add `%USERPROFILE%\go\bin` to your `PATH`)
- Git for Windows

**Via PowerShell script**:
```pwsh
irm https://raw.githubusercontent.com/steveyegge/beads/main/install.ps1 | iex
```

The script installs a prebuilt Windows release if available. Go is only required for `go install` or building from source.

**Dolt backend on Windows:** Supported via pure-Go regex backend. Windows builds automatically use Go's stdlib `regexp` instead of ICU regex to avoid CGO/header dependencies. If you need full ICU regex semantics, use Linux/macOS (or WSL) with ICU installed.

**Via go install**:
```pwsh
go install github.com/steveyegge/beads/cmd/bd@latest
```

ICU is **not required** on Windows. The regex backend uses pure Go automatically.

**From source**:
```pwsh
git clone https://github.com/steveyegge/beads
cd beads
make build
# Or without Make:
go build -tags gms_pure_go -o bd.exe ./cmd/bd
Move-Item bd.exe $env:USERPROFILE\AppData\Local\Microsoft\WindowsApps\
```

The `-tags gms_pure_go` flag tells go-mysql-server to use Go's stdlib regexp instead of ICU.
Additionally, the vendored go-icu-regex library has a Windows-specific pure-Go implementation
(`regex_windows.go`) that avoids ICU entirely. No C compiler or ICU libraries are needed.

**Verify installation**:
```pwsh
bd version
```

**Windows notes:**
- The background daemon listens on a loopback TCP endpoint recorded in `.beads\bd.sock`
- Keep that metadata file intact
- Allow `bd.exe` loopback traffic through any host firewall

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
```

### Claude Code Plugin (Optional)

For enhanced UX with slash commands:

```bash
# In Claude Code
/plugin marketplace add steveyegge/beads
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

### `bd: command not found`

bd is not in your PATH. Either:

```bash
# Check if installed
go list -f {{.Target}} github.com/steveyegge/beads/cmd/bd

# Add Go bin to PATH (add to ~/.bashrc or ~/.zshrc)
export PATH="$PATH:$(go env GOPATH)/bin"

# Or reinstall
go install github.com/steveyegge/beads/cmd/bd@latest
```

### `zsh: killed bd` or crashes on macOS

Some users report crashes when running `bd init` or other commands on macOS. This is typically caused by CGO/SQLite compatibility issues.

**Workaround:**
```bash
# Build with CGO enabled
CGO_ENABLED=1 go install github.com/steveyegge/beads/cmd/bd@latest

# Or if building from source
git clone https://github.com/steveyegge/beads
cd beads
CGO_ENABLED=1 go build -o bd ./cmd/bd
sudo mv bd /usr/local/bin/
```

If you installed via Homebrew, this shouldn't be necessary as the formula already enables CGO. If you're still seeing crashes with the Homebrew version, please [file an issue](https://github.com/steveyegge/beads/issues).

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
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
```

### PowerShell Installer (Windows)

```pwsh
irm https://raw.githubusercontent.com/steveyegge/beads/main/install.ps1 | iex
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

```bash
go install github.com/steveyegge/beads/cmd/bd@latest
```

### From source

```bash
cd beads
git pull
go build -o bd ./cmd/bd
sudo mv bd /usr/local/bin/
```

## After Upgrading (Recommended)

```bash
bd info --whats-new
bd hooks install
bd daemons killall
bd version
```

## Uninstalling

To completely remove Beads from a repository, see [UNINSTALLING.md](UNINSTALLING.md).
