---
id: installation
title: Installation
sidebar_position: 1
---

# Installing bd

## Quick Install (Recommended)

### Homebrew (macOS/Linux)

```bash
brew install beads
```

### Install Script (macOS/Linux/FreeBSD)

```bash
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### PowerShell (Windows)

```pwsh
irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex
```

### npm

```bash
npm install -g @beads/bd
```

## Platform-Specific Notes

### macOS

Homebrew is recommended. If you specifically need `go install`, use one of the supported modes below.

```bash
# Server-mode only
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest

# Embedded-capable
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest
```

### Linux

Homebrew works on Linux. For Arch Linux: `yay -S beads-git` or `paru -S beads-git` (AUR).

### Windows

Native Windows support - no MSYS or MinGW required. The PowerShell installer above is the recommended path. Go is only required for building from source.

## Verifying Installation

```bash
bd version
bd help
```

If you see `bd: command not found`, ensure your install location is in PATH. For Homebrew and npm this is automatic. For `go install`, add `$(go env GOPATH)/bin` to your PATH.

## Building from Source

Building from source requires Go, git, and a C compiler for embedded Dolt. ICU headers are not required; builds use `gms_pure_go`.

| Platform | Command |
|----------|---------|
| macOS | `brew install zstd` |
| Debian/Ubuntu | `sudo apt-get install -y libzstd-dev` |
| Fedora/RHEL | `sudo dnf install -y libzstd-devel` |

```bash
git clone https://github.com/gastownhall/beads
cd beads
make build
```

See [CONTRIBUTING.md](https://github.com/gastownhall/beads/blob/main/CONTRIBUTING.md) for full developer setup.

## Next Steps

1. **Initialize a project**: `cd your-project && bd init`
2. **Learn the basics**: [Quick Start](/getting-started/quickstart)
3. **Configure your editor**: [IDE Setup](/getting-started/ide-setup)
4. **Upgrading later**: [Upgrading](/getting-started/upgrading)
