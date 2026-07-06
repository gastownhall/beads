# Beads Plugin

This is the shared Claude/Codex plugin package for Beads. Claude and Codex use separate manifest files, but they share the same skill tree.

## Layout

- `.codex-plugin/plugin.json` describes the Codex plugin.
- `.claude-plugin/plugin.json` describes the Claude plugin.
- `skills/beads/` contains the plugin-owned Beads skill.
- `.codex-plugin/hooks/hooks.json` contains Codex-only lifecycle hooks for startup and compaction-aware context refresh.
- The Claude marketplace entry lives at `.claude-plugin/marketplace.json`.

## Codex Hooks

The Codex plugin exposes native hooks through `.codex-plugin/plugin.json`, which points at `.codex-plugin/hooks/hooks.json`.
With Codex 0.129.0+, `/hooks` shows these lifecycle handlers:

- `SessionStart` runs `bd codex-hook SessionStart` for `startup|resume|clear` and injects full `bd prime` output.
- `PreCompact` runs `bd codex-hook PreCompact` for `manual|auto` and warns if `bd prime --memories-only` cannot run.
- `PostCompact` runs `bd codex-hook PostCompact` for `manual|auto` and records a one-shot refresh marker in the user cache/temp directory.
- `UserPromptSubmit` runs `bd codex-hook UserPromptSubmit` and, when a refresh marker exists, injects full `bd prime` output once before clearing it.

If the plugin is not installed, `bd setup codex` writes an equivalent `.codex/hooks.json` fallback and enables `[features].hooks = true`.

## Local Development

Claude Code uses `.claude-plugin/marketplace.json`, which points at this shared package root.
