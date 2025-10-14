# Beads Scripts

Utility scripts for maintaining the beads project.

## bump-version.sh

Bumps the version number across all beads components in a single command.

### Usage

```bash
# Show usage
./scripts/bump-version.sh

# Update versions (shows diff, no commit)
./scripts/bump-version.sh 0.9.3

# Update versions and auto-commit
./scripts/bump-version.sh 0.9.3 --commit
```

### What It Does

Updates version in all these files:
- `cmd/bd/version.go` - bd CLI version constant
- `.claude-plugin/plugin.json` - Plugin version
- `.claude-plugin/marketplace.json` - Marketplace plugin version
- `integrations/beads-mcp/pyproject.toml` - MCP server version
- `README.md` - Alpha status version
- `PLUGIN.md` - Version requirements

### Features

- **Validates** semantic versioning format (MAJOR.MINOR.PATCH)
- **Verifies** all versions match after update
- **Shows** git diff of changes
- **Auto-commits** with standardized message (optional)
- **Cross-platform** compatible (macOS and Linux)

### Examples

```bash
# Bump to 0.9.3 and review changes
./scripts/bump-version.sh 0.9.3
# Review the diff, then manually commit

# Bump to 1.0.0 and auto-commit
./scripts/bump-version.sh 1.0.0 --commit
git push origin main
```

### Why This Script Exists

Previously, version bumps only updated `cmd/bd/version.go`, leaving other components out of sync. This script ensures all version numbers stay consistent across the project.

### Safety

- Checks for uncommitted changes before proceeding
- Refuses to auto-commit if there are existing uncommitted changes
- Validates version format before making any changes
- Verifies all versions match after update
- Shows diff for review before commit

## Future Scripts

Additional maintenance scripts may be added here as needed.
