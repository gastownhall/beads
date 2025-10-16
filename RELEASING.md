# Release Process

Quick guide for releasing a new version of beads.

## Pre-Release Checklist

1. **Run tests and build**:
   ```bash
   go test ./...
   golangci-lint run ./...
   go build -o bd ./cmd/bd
   ```

2. **Update CHANGELOG.md**:
   - Add version heading: `## [0.9.X] - YYYY-MM-DD`
   - Summarize changes under: Added, Fixed, Changed, Performance, Community
   - Update Version History section
   - Add Upgrade Guide section if needed

3. **Commit changelog**:
   ```bash
   git add CHANGELOG.md
   git commit -m "Add 0.9.X release notes"
   ```

## Version Bump

Use the automated script to update all version files:

```bash
./scripts/bump-version.sh 0.9.X --commit
```

This updates:
- `cmd/bd/version.go`
- `.claude-plugin/plugin.json`
- `.claude-plugin/marketplace.json`
- `integrations/beads-mcp/pyproject.toml`
- `integrations/beads-mcp/src/beads_mcp/__init__.py`
- `README.md`
- `PLUGIN.md`

## Publish to All Channels

### 1. Create Git Tag

```bash
git tag v0.9.X
git push origin main
git push origin v0.9.X
```

### 2. Publish to PyPI (MCP Server)

```bash
cd integrations/beads-mcp

# Clean and rebuild
rm -rf dist/ build/ src/*.egg-info
uv build

# Upload to PyPI
python3 -m twine upload dist/*
# Username: __token__
# Password: <your PyPI API token>
```

See [integrations/beads-mcp/PYPI.md](integrations/beads-mcp/PYPI.md) for detailed PyPI instructions.

### 3. Update Homebrew Formula

The formula needs the SHA256 of the tag tarball:

```bash
# Compute SHA256 from tag
curl -sL https://github.com/steveyegge/beads/archive/refs/tags/v0.9.X.tar.gz | shasum -a 256

# Clone tap repo (if not already)
git clone https://github.com/steveyegge/homebrew-beads /tmp/homebrew-beads
cd /tmp/homebrew-beads

# Update Formula/bd.rb:
# - url: https://github.com/steveyegge/beads/archive/refs/tags/v0.9.X.tar.gz
# - sha256: <computed SHA256>

# Commit and push
git add Formula/bd.rb
git commit -m "Update bd to 0.9.X"
git push origin main
```

Users can then upgrade with:
```bash
brew update
brew upgrade bd
```

### 4. Create GitHub Release

1. Go to https://github.com/steveyegge/beads/releases/new
2. Choose tag: `v0.9.X`
3. Title: `v0.9.X`
4. Description: Copy from CHANGELOG.md
5. Attach binaries (optional - GitHub Actions can automate this)
6. Publish release

## Post-Release

1. **Verify installations**:
   ```bash
   # Homebrew
   brew update && brew upgrade bd && bd version
   
   # PyPI
   pip install --upgrade beads-mcp
   beads-mcp --help
   ```

2. **Announce** (optional):
   - Project Discord/Slack
   - Twitter/social media
   - README badges

## Troubleshooting

### Stale dist/ directory
Always `rm -rf dist/` before `uv build` to avoid uploading old versions.

### PyPI version conflict
PyPI doesn't allow re-uploading same version. Increment version number even for fixes.

### Homebrew SHA256 mismatch
Wait a few seconds after pushing tag for GitHub to make tarball available, then recompute SHA256.

### Missing PyPI credentials
Set up API token at https://pypi.org/manage/account/token/ and use `__token__` as username.

## Automation Ideas (Future)

Consider GitHub Actions to:
- Run tests on tag push
- Auto-build and publish to PyPI
- Auto-update Homebrew formula
- Create GitHub release with binaries

## Related Documentation

- [CHANGELOG.md](CHANGELOG.md) - Release history
- [scripts/README.md](scripts/README.md) - Version bump script details
- [integrations/beads-mcp/PYPI.md](integrations/beads-mcp/PYPI.md) - Detailed PyPI guide
