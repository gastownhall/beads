---
id: upgrading
title: Upgrading
sidebar_position: 4
---

# Upgrading bd

## Upgrade Commands

Use the command that matches your install method:

| Install method | Command |
|---|---|
| Homebrew | `brew upgrade beads` |
| Install script (macOS/Linux) | `curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh \| bash` |
| PowerShell (Windows) | `irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 \| iex` |
| npm | `npm update -g @beads/bd` |
| go install (server-mode only) | `CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest` |
| go install (embedded-capable) | `CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest` |

## After Upgrading

```bash
bd info --whats-new     # Check what changed
bd hooks install        # Update git hooks to match new version
bd migrate --dry-run    # Check for database migrations
bd migrate              # Apply if needed
```

Git hooks are versioned with bd - outdated hooks may miss auto-sync features. If using Dolt server mode, restart with `bd dolt stop && bd dolt start`.

## Recovery

If an upgrade causes issues:

```bash
bd dolt pull                        # Restore from remote
bd backup restore [path] --force    # Or restore from backup
```
