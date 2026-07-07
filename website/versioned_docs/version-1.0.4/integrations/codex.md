---
id: codex
title: Codex
sidebar_position: 2
---

# Codex Integration

Use Beads with Codex through the `beads` skill, managed `AGENTS.md` guidance, and native Codex hooks.

```bash
bd setup codex
bd setup codex --check
```

Codex 0.129.0+ supports `/hooks`, compact lifecycle hooks, and hook-provided developer context. Beads uses that lifecycle to inject `bd prime` on session start and recover context after compaction.

## Hook Lifecycle

- `SessionStart` (`startup|resume|clear`) injects full `bd prime` output.
- `PreCompact` (`manual|auto`) checks `bd prime --memories-only` and warns if Beads context is unavailable.
- `PostCompact` (`manual|auto`) records that the session needs a Beads refresh.
- `UserPromptSubmit` injects full `bd prime` once after compaction, then clears the refresh marker.

`PreCompact` alone does not inject context because Codex ignores plain stdout from compact hooks. The post-compact marker plus first-prompt refresh is the reliable recovery path.

The Beads Codex plugin declares `"hooks": "./hooks/hooks.json"`. Without the plugin, `bd setup codex` installs the same hook config in `.codex/hooks.json` and enables `[features].hooks = true`.
