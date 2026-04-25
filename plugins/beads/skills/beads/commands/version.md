---
description: Check beads and plugin versions
---

Check the installed versions of beads components and verify compatibility.

Use the CLI and plugin manifests to:
1. Run `bd version` via bash to get the CLI version
2. Check the installed plugin manifest version
3. Compare versions and report any mismatches

Display:
- bd CLI version (from `bd version`)
- Plugin version (from the installed plugin manifest, if available)
- Compatibility status (✓ compatible or ⚠️ update needed)

If versions are mismatched, provide instructions:
- Update bd CLI: `curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash`
- Update plugin: reinstall or update the Beads plugin through the active agent's plugin manager
- Restart the agent client after updating

Suggest checking for updates if the user is on an older version.
