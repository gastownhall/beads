---
id: mux
title: Mux
---

# Mux Integration

Use Beads with Mux through `AGENTS.md`, optional layered Mux instruction files, and Mux hooks.

```bash
bd setup mux
bd setup mux --check
```

The default setup writes a managed Beads section to root `AGENTS.md`.

## Workspace and Global Layers

Mux also supports workspace and global instruction layers:

```bash
bd setup mux --project
bd setup mux --global
bd setup mux --project --global
```

Project setup writes `.mux/AGENTS.md` and installs Mux hook files under `.mux/`:

- `.mux/init`
- `.mux/tool_post`
- `.mux/tool_env`

Global setup writes `~/.mux/AGENTS.md`.

## Remove

```bash
bd setup mux --remove
bd setup mux --project --remove
bd setup mux --global --remove
```
