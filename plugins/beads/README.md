# Beads Plugin

This is the shared Claude/Codex plugin package for Beads. Claude and Codex use separate manifest files, but they share the same skill tree.

## Layout

- `.codex-plugin/plugin.json` describes the Codex plugin.
- `.claude-plugin/plugin.json` describes the Claude plugin.
- `skills/beads/` contains the plugin-owned Beads skill.
- The repo marketplace entries live at `.agents/plugins/marketplace.json` and `.claude-plugin/marketplace.json`.

## Local Development

From the parent directory of this repository, add the marketplace to Codex:

```bash
codex marketplace add ./beads
```

Then use Codex `/plugins` to inspect or install the `beads` plugin.

Claude Code uses `.claude-plugin/marketplace.json`, which points at this same package root.
