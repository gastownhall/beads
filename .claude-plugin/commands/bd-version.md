---
description: Check beads and plugin versions
---

Check the installed versions of beads components and verify compatibility.

Use the beads MCP tools to:
1. Run `bd --version` via bash to get the CLI version
2. Check the plugin version from the environment
3. Compare versions and report any mismatches

Display:
- bd CLI version (from `bd --version`)
- Plugin version (0.9.0)
- MCP server status (from `stats` tool or connection test)
- Compatibility status (✓ compatible or ⚠️ update needed)

If versions are mismatched, provide instructions:
- Update bd CLI: `curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/install.sh | bash`
- Update plugin: `/plugin update beads`

Suggest checking for updates if the user is on an older version.
