---
id: gemini
title: Gemini CLI
---

# Gemini CLI Integration

Use Beads with Gemini CLI through SessionStart hooks and `GEMINI.md` guidance.

```bash
bd setup gemini
bd setup gemini --check
```

By default, setup installs global hooks in `~/.gemini/settings.json`. For project-local hooks, use:

```bash
bd setup gemini --project
```

The hook runs `bd prime --hook-json` so Gemini receives compact Beads workflow context at session start. The setup also writes Beads guidance to `GEMINI.md`.

## Stealth Mode

For CI or other environments where setup should avoid git operations:

```bash
bd setup gemini --stealth
bd setup gemini --project --stealth
```

## Remove

```bash
bd setup gemini --remove
bd setup gemini --project --remove
```
