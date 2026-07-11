---
title: Codex
---

Use Beads with Codex through the `beads` skill, managed `AGENTS.md` guidance, and native Codex hooks.

```bash
bd setup codex
bd setup codex --check
```

Project setup writes:

- `.agents/skills/beads/` for the Beads skill.
- `AGENTS.md` with a managed Beads section.
- `.codex/config.toml` with `[features].hooks = true`.
- `.codex/hooks.json` with the Beads hook fallback.

`bd init` runs this project setup by default unless `--skip-agents` or `--stealth` is used. Global setup uses `bd setup codex --global` and writes under `$CODEX_HOME` when set, otherwise `~/.codex`.

Codex 0.129.0+ supports `/hooks`, compact lifecycle hooks, and hook-provided developer context. Beads uses that lifecycle to inject `bd prime` on session start and recover context after compaction. Use `/hooks` to inspect or toggle the installed handlers.

## Hook Lifecycle

- `SessionStart` (`startup|resume|clear`) injects full `bd prime` output.
- `PreCompact` (`manual|auto`) checks `bd prime --memories-only` and warns if Beads context is unavailable.
- `PostCompact` (`manual|auto`) records that the session needs a Beads refresh.
- `UserPromptSubmit` injects full `bd prime` once after compaction, then clears the refresh marker.

`PreCompact` alone does not inject context because Codex ignores plain stdout from compact hooks. The post-compact marker plus first-prompt refresh is the reliable recovery path.

The Beads Codex plugin stores hooks at `plugins/beads/.codex-plugin/hooks/hooks.json` and declares them as `"hooks": "./hooks/hooks.json"`. Without the plugin, `bd setup codex` installs the same hook config in `.codex/hooks.json` and enables `[features].hooks = true`.
