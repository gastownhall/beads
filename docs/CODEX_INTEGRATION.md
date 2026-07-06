# Codex Integration

Beads supports Codex through the shared `beads` skill, `AGENTS.md` guidance, and native Codex lifecycle hooks.

## Quick Setup

```bash
bd setup codex
```

`bd init` also runs this project setup by default unless `--skip-agents` or `--stealth` is used. Project setup writes:

- `.agents/skills/beads/` for the Beads skill.
- `AGENTS.md` with a managed Beads section.
- `.codex/config.toml` with `[features].hooks = true`.
- `.codex/hooks.json` with the Beads hook fallback.

Global setup uses `bd setup codex --global` and writes under `$CODEX_HOME` when set, otherwise `~/.codex`.

## Plugin-Managed Hooks

The bundled Codex plugin keeps its hook config at `plugins/beads/.codex-plugin/hooks/hooks.json`; `plugins/beads/.codex-plugin/plugin.json` declares it as `"hooks": "./hooks/hooks.json"`. On Codex 0.129.0+, use `/hooks` to inspect or toggle these handlers.

The plugin and `bd setup codex` fallback install the same lifecycle:

- `SessionStart` with matcher `startup|resume|clear`: injects full `bd prime` output as developer context.
- `PreCompact` with matcher `manual|auto`: checks `bd prime --memories-only` and surfaces a warning if Beads context is unavailable.
- `PostCompact` with matcher `manual|auto`: records that the Codex session needs a Beads refresh.
- `UserPromptSubmit`: if a refresh marker exists, injects full `bd prime` output once and clears the marker.

`PreCompact` cannot preserve Beads context by printing text; compact hook plain stdout is ignored by Codex. Beads therefore uses `PostCompact` plus the next `UserPromptSubmit` to recover context after successful manual or automatic compaction.

Refresh markers are stored in a user cache/temp directory keyed by Codex `session_id` and workspace path. They are not written to tracked files or to the Beads database.

## Manual Fallback

If you do not use the plugin, `bd setup codex` manages `.codex/hooks.json` directly. The equivalent manual shape is:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|resume|clear",
        "hooks": [{ "type": "command", "command": "bd codex-hook SessionStart" }]
      }
    ],
    "PreCompact": [
      {
        "matcher": "manual|auto",
        "hooks": [{ "type": "command", "command": "bd codex-hook PreCompact" }]
      }
    ],
    "PostCompact": [
      {
        "matcher": "manual|auto",
        "hooks": [{ "type": "command", "command": "bd codex-hook PostCompact" }]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [{ "type": "command", "command": "bd codex-hook UserPromptSubmit" }]
      }
    ]
  }
}
```

Ensure `config.toml` enables:

```toml
[features]
hooks = true
```
