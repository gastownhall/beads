# Release Process

Quick guide for releasing a new version of beads.

## 🚀 The Easy Way (Recommended)

Use the fully automated release script:

```bash
./scripts/release.sh 0.9.3
```

This does **everything**: version bump, tests, git tag, Homebrew update, and local installation.

See [scripts/README.md](scripts/README.md#releasesh--the-easy-button) for details.

---

## 📋 The Manual Way

If you prefer step-by-step control:

### Pre-Release Checklist

1. **Kill all running daemons (CRITICAL)**:
   ```bash
   # Kill by process name
   pkill -f "bd.*daemon"
   
   # Verify no daemons are running
   pgrep -lf "bd.*daemon" || echo "No daemons running ✓"
   
   # Alternative: find and kill by socket
   find ~/.config -name "bd.sock" -type f 2>/dev/null | while read sock; do
     echo "Found daemon socket: $sock"
   done
   ```
   
   **Why this matters**: Old daemon versions can cause:
   - Auto-flush race conditions leaving working tree dirty after commits
   - Version mismatches between client (new) and daemon (old)
   - Confusing behavior where changes appear to sync incorrectly

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
git push origin main
```

This updates:
- `cmd/bd/version.go`
- `.claude-plugin/plugin.json`
- `.claude-plugin/marketplace.json`
- `integrations/beads-mcp/pyproject.toml`
- `integrations/beads-mcp/src/beads_mcp/__init__.py`
- `README.md`
- `PLUGIN.md`

**IMPORTANT**: After version bump, rebuild the local binary:
```bash
go build -o bd ./cmd/bd
./bd version  # Should show new version
```

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

**CRITICAL**: This step must be done AFTER GitHub Actions completes the release build (~5 minutes after pushing the tag).

**Why the wait?** The Homebrew formula uses GoReleaser artifacts (platform-specific binaries), not the source tarball. These are built by GitHub Actions.

**Step 1: Wait for GitHub Actions to complete**

Monitor the release workflow at: https://github.com/steveyegge/beads/actions

Once complete, the release appears at: https://github.com/steveyegge/beads/releases/tag/v0.9.X

**Step 2: Get SHA256s from release artifacts**

GoReleaser creates a `checksums.txt` file with all artifact hashes:

```bash
# Download checksums file
curl -sL https://github.com/steveyegge/beads/releases/download/v0.9.X/checksums.txt

# Extract the SHA256s you need:
# - beads_0.9.X_darwin_arm64.tar.gz (macOS Apple Silicon)
# - beads_0.9.X_darwin_amd64.tar.gz (macOS Intel)
# - beads_0.9.X_linux_amd64.tar.gz (Linux x86_64)
# - beads_0.9.X_linux_arm64.tar.gz (Linux ARM64)
```

**Step 3: Update the formula**

```bash
# Navigate to tap repo (if already cloned) or clone it
cd /tmp/homebrew-beads || git clone https://github.com/steveyegge/homebrew-beads /tmp/homebrew-beads

# Pull latest changes
cd /tmp/homebrew-beads
git pull

# Edit Formula/bd.rb - update:
# 1. version "0.9.X" (line 4)
# 2. SHA256 for darwin_arm64 (line 10)
# 3. SHA256 for darwin_amd64 (line 13)
# 4. SHA256 for linux_amd64 (line 23)
# 5. SHA256 for linux_arm64 (line 20) - optional, often empty

# Example:
# version "0.23.0"
# on_macos do
#   if Hardware::CPU.arm?
#     url "https://github.com/steveyegge/beads/releases/download/v#{version}/beads_#{version}_darwin_arm64.tar.gz"
#     sha256 "abc123..."
#   else
#     url "https://github.com/steveyegge/beads/releases/download/v#{version}/beads_#{version}_darwin_amd64.tar.gz"
#     sha256 "def456..."

# Commit and push
git add Formula/bd.rb
git commit -m "Update bd formula to v0.9.X"
git push origin main
```

**Step 4: CRITICAL - Verify the installation works**

```bash
brew update
brew upgrade bd  # Or: brew reinstall bd
bd version  # MUST show v0.9.X - if not, the release is incomplete!
```

**⚠️ DO NOT SKIP THE VERIFICATION STEP ABOVE** - This ensures users can actually install the new version.

**Note**: Until this step is complete, users with Homebrew-installed bd will still have the old version.

**Note:** If you have an old bd binary from `go install` in your PATH, remove it to avoid conflicts:
```bash
# Find where bd is installed
which bd

# If it's in a Go toolchain path (e.g., ~/go/bin/bd or mise-managed Go), remove it
# Common locations:
rm ~/go/bin/bd                                    # Standard go install location
rm ~/.local/share/mise/installs/go/*/bin/bd      # mise-managed Go installs

# Verify you're using the correct version
which bd        # Should show /opt/homebrew/bin/bd or your package manager's path
bd version      # Should show the latest version
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

1. **Kill old daemons again**:
   ```bash
   pkill -f "bd.*daemon"
   pgrep -lf "bd.*daemon" || echo "No daemons running ✓"
   ```
   This ensures your local machine picks up the new version immediately.

2. **Verify installations**:
   ```bash
   # Homebrew
   brew update && brew upgrade bd && bd version
   
   # PyPI
   pip install --upgrade beads-mcp
   beads-mcp --help
   
   # Check daemon version matches client
   bd version --daemon  # Should match client version after first command
   ```

3. **Announce** (optional):
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
