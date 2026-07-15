# Beads Plugin

This is the shared Claude, Codex, Copilot, and Pi plugin package for Beads. Each agent uses its own metadata, while all of them share the same skill tree.

## Layout

- `.codex-plugin/plugin.json` describes the Codex plugin.
- `.claude-plugin/plugin.json` describes the Claude plugin.
- `.copilot-plugin/plugin.json` describes the Copilot plugin.
- `package.json` describes the Pi package.
- `.pi/extensions/beads.ts` adds the shared skill tree and refreshes Beads context across Pi session lifecycle events.
- `skills/beads/` contains the plugin-owned Beads skill.
- `.codex-plugin/hooks/hooks.json` contains Codex-only lifecycle hooks for startup and compaction-aware context refresh.
- The Claude marketplace entry lives at `.claude-plugin/marketplace.json`.

## Pi

Pi loads this directory as a package. Its extension resolves `skills/` relative to the installed extension, so discovery does not depend on the directory where Pi starts. When Pi starts or replaces a session, the extension runs `bd prime` in the session working directory and injects successful output as hidden model context. It refreshes that context again after manual, threshold, or overflow compaction. Non-Beads working directories are left unchanged.

The installed Pi CLI accepts git repository roots, but does not document a git-subdirectory selector. For a durable install, keep a Beads checkout and install the plugin directory from that checkout:

```bash
git clone https://github.com/gastownhall/beads.git
pi install /absolute/path/to/beads/plugins/beads
```

To test a local Beads checkout without changing global Pi settings, point Pi at an isolated agent directory:

```bash
export PI_CODING_AGENT_DIR=/tmp/beads-pi
pi install ./plugins/beads
pi list
pi
```

Inside Pi, run `/skill:beads` to load the skill explicitly. The skill remains useful for commands and reference material, while `bd prime` is the current source of truth automatically injected by the lifecycle hooks.

Run the focused extension test from the repository root with:

```bash
npm test --prefix plugins/beads
```

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
