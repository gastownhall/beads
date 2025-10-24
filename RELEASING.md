# Release Process

Quick guide for releasing a new version of beads.

## Pre-Release Checklist

1. **Kill all running daemons**:
   ```bash
   pkill -f "bd.*daemon"
   ```

2. **Run tests and build**:
   ```bash
   TMPDIR=/tmp go test ./...
   golangci-lint run ./...
   TMPDIR=/tmp go build -o bd ./cmd/bd
   ./bd version  # Verify it shows new version
   ```

3. **Skip local install** (avoid go install vs brew conflicts):
   - Use `./bd` directly from the repo for testing
   - Your system bd will be updated via brew after Homebrew formula update
   - Or temporarily: `alias bd="$PWD/bd"` if needed

4. **Update CHANGELOG.md**:
   - Add version heading: `## [0.9.X] - YYYY-MM-DD`
   - Summarize changes under: Added, Fixed, Changed, Performance, Community
   - Update Version History section
   - Add Upgrade Guide section if needed

5. **Commit changelog**:
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

**That's it!** GitHub Actions automatically handles the rest:
- GoReleaser builds and publishes binaries to GitHub Releases
- PyPI publish job uploads the MCP server package to PyPI

### 2. GitHub Secrets Setup (One-Time)

The automation requires this secret to be configured:

**PYPI_API_TOKEN**: Your PyPI API token
1. Generate token at https://pypi.org/manage/account/token/
2. Add to GitHub at https://github.com/steveyegge/beads/settings/secrets/actions
3. Name: `PYPI_API_TOKEN`
4. Value: `pypi-...` (your full token)

### 3. Manual PyPI Publish (If Needed)

If the automated publish fails, you can manually upload:

```bash
cd integrations/beads-mcp

# Clean and rebuild
rm -rf dist/ build/ src/*.egg-info
uv build

# Upload to PyPI
TWINE_USERNAME=__token__ TWINE_PASSWORD=pypi-... uv tool run twine upload dist/*
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
git config user.name "Your Name"
git config user.email "your.email@example.com"

# Update Formula/bd.rb:
# - url: https://github.com/steveyegge/beads/archive/refs/tags/v0.9.X.tar.gz
# - sha256: <computed SHA256>

# Commit and push
git add Formula/bd.rb
git commit -m "Update bd formula to v0.9.X"
git push origin main
```

Install/upgrade locally with:
```bash
brew update
brew upgrade bd  # Or: brew reinstall bd
bd version  # Verify it shows new version
```

**Note:** If you have `~/go/bin/bd` from `go install`, remove it to avoid conflicts:
```bash
rm ~/go/bin/bd  # Clear go install version
which bd        # Should show /opt/homebrew/bin/bd
```

### 4. GitHub Releases (Automated)

**GoReleaser automatically creates releases when you push tags!**

The `.github/workflows/release.yml` workflow:
- Triggers on `v*` tags
- Builds cross-platform binaries (Linux, macOS, Windows for amd64/arm64)
- Generates checksums
- Creates GitHub release with binaries and changelog
- Publishes release automatically

Just push your tag and wait ~5 minutes:
```bash
git push origin v0.9.X
```

Monitor at: https://github.com/steveyegge/beads/actions

The release will appear at: https://github.com/steveyegge/beads/releases

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

## Automation Status

✅ **Automated:**
- GitHub releases with binaries (GoReleaser + GitHub Actions)
- PyPI publish (automated via GitHub Actions)
- Cross-platform builds (Linux, macOS, Windows)
- Checksums and changelog generation

🔄 **TODO:**
- Auto-update Homebrew formula

## Related Documentation

- [CHANGELOG.md](CHANGELOG.md) - Release history
- [scripts/README.md](scripts/README.md) - Version bump script details
- [integrations/beads-mcp/PYPI.md](integrations/beads-mcp/PYPI.md) - Detailed PyPI guide
