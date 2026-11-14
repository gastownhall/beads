---
title: Gemini CLI
description: Set up beads for Gemini CLI with SessionStart hooks that run bd prime and GEMINI.md workflow guidance
---

Use Beads with Gemini CLI through SessionStart hooks and `GEMINI.md` guidance.

```bash
bd setup gemini
bd setup gemini --check
```

By default, setup installs global hooks in `~/.gemini/settings.json` and a Beads section in your user-level `~/.gemini/GEMINI.md`, which Gemini CLI loads in every project, so there the section applies only in repositories that use beads. A global install leaves the current directory's files alone. For project-local hooks and a section in the project's `GEMINI.md`, use:

```bash
bd setup gemini --project
```

Run from your home directory or from `~/.gemini`, `--project` is the global install: it uses `~/.gemini/settings.json` and `~/.gemini/GEMINI.md`, and does not create `~/GEMINI.md`.

The hook runs `bd prime --hook-json` so Gemini receives compact Beads workflow context at session start. The `GEMINI.md` section is a short pointer to `bd prime`. In an existing `~/.gemini/GEMINI.md` your text is kept, and the section goes ahead of the `## Gemini Added Memories` section older Gemini CLI releases keep there, so memories they save stay outside it.

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
