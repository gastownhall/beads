---
description: Check beads and plugin versions
---

Check the installed versions of beads components and verify compatibility.

**Note:** This command checks that your `bd` CLI version is compatible with the plugin and provides update instructions.

1. Run `bd version` to get the CLI version
2. Check the plugin version (0.9.2)
3. Compare versions and report any mismatches

Display:
- bd CLI version (from `bd version`)
- Plugin version (0.9.2)
- Compatibility status (✓ compatible or ⚠️ update needed)

If versions are mismatched, provide instructions:
- Update bd CLI: `curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash`
- Update plugin: `/plugin update beads`
- Restart Claude Code after updating

Suggest checking for updates if the user is on an older version.
