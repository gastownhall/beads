# Release Process for Beads

This document describes the complete release process for beads, including GitHub releases, Homebrew, PyPI (MCP server), and npm packages.

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Release Checklist](#release-checklist)
- [1. Prepare Release](#1-prepare-release)
- [2. GitHub Release](#2-github-release)
- [3. Homebrew Update](#3-homebrew-update)
- [4. PyPI Release (MCP Server)](#4-pypi-release-mcp-server)
- [5. npm Package Release](#5-npm-package-release)
- [6. Verify Release](#6-verify-release)
- [Hotfix Releases](#hotfix-releases)
- [Rollback Procedure](#rollback-procedure)

## Overview

A beads release involves multiple distribution channels:

1. **GitHub Release** - Binary downloads for all platforms
2. **Homebrew** - macOS/Linux package manager
3. **PyPI** - Python MCP server (`beads-mcp`)
4. **npm** - Node.js package for Claude Code for Web (`@beads/bd`)

## Prerequisites

### Required Tools

- `git` with push access to steveyegge/beads
- `goreleaser` for building binaries
- `npm` with authentication (for npm releases)
- `python3` and `twine` (for PyPI releases)
- `gh` CLI (GitHub CLI, optional but recommended)

### Required Access

- GitHub: Write access to repository and ability to create releases
- Homebrew: Write access to steveyegge/homebrew-beads
- PyPI: Maintainer access to `beads-mcp` package
- npm: Member of `@beads` organization

### Verify Setup

```bash
# Check git
git remote -v  # Should show steveyegge/beads

# Check goreleaser
goreleaser --version

# Check GitHub CLI (optional)
gh auth status

# Check npm
npm whoami  # Should show your npm username

# Check Python/twine (for MCP releases)
python3 --version
twine --version
```

## Release Checklist

Before starting a release:

- [ ] All tests passing (`go test ./...`)
- [ ] npm package tests passing (`cd npm-package && npm run test:all`)
- [ ] **CHANGELOG.md updated with release notes** (see format below)
- [ ] No uncommitted changes
- [ ] On `main` branch and up to date with origin

## 1. Prepare Release

### Update CHANGELOG.md

**IMPORTANT: Do this FIRST before running bump-version script.**

Add release notes to CHANGELOG.md:

```markdown
## [0.22.0] - 2025-11-04

### Added
- New feature X
- New command Y

### Changed
- Improved performance of Z

### Fixed
- Bug in component A

### Breaking Changes
- Changed behavior of B (migration guide)
```

Commit the CHANGELOG changes:

```bash
git add CHANGELOG.md
git commit -m "docs: Add CHANGELOG entry for v0.22.0"
git push origin main
```

### Update Version and Create Release Tag

Use the version bump script to update all version references and create the release tag:

```bash
# Dry run - shows what will change
./scripts/bump-version.sh 0.22.0

# Full release with all local installations
./scripts/bump-version.sh 0.22.0 --commit --tag --push --all
```

**Available flags:**

| Flag | Description |
|------|-------------|
| `--commit` | Create a git commit with version changes |
| `--tag` | Create annotated git tag (requires --commit) |
| `--push` | Push commit and tag to origin (requires --tag) |
| `--install` | Build and install bd to `~/go/bin` AND `~/.local/bin` |
| `--mcp-local` | Install beads-mcp from local source via uv/pip |
| `--upgrade-mcp` | Upgrade beads-mcp from PyPI (after PyPI publish) |
| `--restart-daemons` | Restart all bd daemons to pick up new version |
| `--all` | Shorthand for `--install --mcp-local --restart-daemons` |

This updates:
- `cmd/bd/version.go` - CLI version constant
- `integrations/beads-mcp/pyproject.toml` - MCP server version
- `integrations/beads-mcp/src/beads_mcp/__init__.py` - MCP Python version
- `.claude-plugin/plugin.json` - Plugin version
- `.claude-plugin/marketplace.json` - Marketplace version
- `npm-package/package.json` - npm package version
- `cmd/bd/templates/hooks/*` - Git hook versions
- `README.md` - Documentation version
- `PLUGIN.md` - Version requirements
- `CHANGELOG.md` - Creates release entry from [Unreleased]

The `--commit --tag --push` flags will:
1. Create a git commit with all version changes
2. Create an annotated tag `v0.22.0`
3. Push both commit and tag to origin

This triggers GitHub Actions to build release artifacts automatically.

**Recommended workflow:**

```bash
# 1. Update CHANGELOG.md and cmd/bd/info.go with release notes (manual step)

# 2. Bump version and install everything locally
./scripts/bump-version.sh 0.22.0 --commit --all

# 3. Test locally, then tag and push
git tag -a v0.22.0 -m "Release v0.22.0"
git push origin main
git push origin v0.22.0
```

**Alternative (step-by-step):**

```bash
# Just commit
./scripts/bump-version.sh 0.22.0 --commit

# Then manually tag and push
git tag -a v0.22.0 -m "Release v0.22.0"
git push origin main
git push origin v0.22.0
```

## 2. GitHub Release

### Using GoReleaser (Recommended)

GoReleaser automates binary building and GitHub release creation:

```bash
# Clean any previous builds
rm -rf dist/

# Create release (requires GITHUB_TOKEN)
export GITHUB_TOKEN="your-github-token"
goreleaser release --clean

# Or use gh CLI for token
gh auth token | goreleaser release --clean
```

This will:
- Build binaries for all platforms (macOS, Linux, Windows - amd64/arm64)
- Create checksums
- Generate release notes from CHANGELOG.md
- Upload everything to GitHub releases
- Mark as latest release

### Manual Release (Alternative)

If goreleaser doesn't work:

```bash
# Build for all platforms
./scripts/build-all-platforms.sh

# Create GitHub release
gh release create v0.22.0 \
  --title "v0.22.0" \
  --notes-file CHANGELOG.md \
  dist/*.tar.gz \
  dist/*.zip \
  dist/checksums.txt
```

### Verify GitHub Release

1. Visit https://github.com/steveyegge/beads/releases
2. Verify v0.22.0 is marked as "Latest"
3. Check all platform binaries are present:
   - `beads_0.22.0_darwin_amd64.tar.gz`
   - `beads_0.22.0_darwin_arm64.tar.gz`
   - `beads_0.22.0_linux_amd64.tar.gz`
   - `beads_0.22.0_linux_arm64.tar.gz`
   - `beads_0.22.0_windows_amd64.zip`
   - `checksums.txt`

## 3. Homebrew Update

Homebrew formula is in a separate tap repository.

### Automatic Update (If Configured)

If you have goreleaser configured with Homebrew:

```bash
# Already done by goreleaser
# Check Formula/bd.rb was updated automatically
```

### Manual Update

```bash
# Clone tap repository
git clone https://github.com/steveyegge/homebrew-beads.git
cd homebrew-beads

# Update formula
# 1. Update version number
# 2. Update SHA256 checksums for macOS binaries

# Test formula
brew install --build-from-source ./Formula/bd.rb
bd version  # Should show 0.22.0

# Commit and push
git add Formula/bd.rb
git commit -m "Update bd to 0.22.0"
git push
```

### Verify Homebrew

```bash
# Update tap
brew update

# Install new version
brew upgrade bd

# Verify
bd version  # Should show 0.22.0
```

## 4. PyPI Release (MCP Server)

The MCP server is a Python package published separately to PyPI.

### Prerequisites

```bash
# Install build tools
pip install build twine

# Verify PyPI credentials
cat ~/.pypirc  # Should have token or credentials
```

### Build and Publish

```bash
# Navigate to MCP server directory
cd integrations/mcp/server

# Verify version was updated
cat pyproject.toml | grep version

# Clean old builds
rm -rf dist/ build/ *.egg-info

# Build package
python -m build

# Verify contents
tar -tzf dist/beads-mcp-0.22.0.tar.gz

# Upload to PyPI (test first)
twine upload --repository testpypi dist/*

# Verify on test PyPI
pip install --index-url https://test.pypi.org/simple/ beads-mcp==0.22.0

# Upload to production PyPI
twine upload dist/*
```

### Verify PyPI Release

```bash
# Check package page
open https://pypi.org/project/beads-mcp/

# Install and test
pip install beads-mcp==0.22.0
python -m beads_mcp --version
```

## 5. Claude Code Marketplace Update

Update the Claude Code marketplace metadata files:

```bash
# Update .claude-plugin/marketplace.json
# Change version to match current release
vim .claude-plugin/marketplace.json

# Update .claude-plugin/plugin.json if needed
vim .claude-plugin/plugin.json

# Commit changes
git add .claude-plugin/
git commit -m "chore: Update Claude Code marketplace to v0.22.0"
```

**Note:** These files define how beads appears in Claude Code's plugin marketplace. Version should match the release version.

## 6. npm Package Release

The npm package wraps the native binary for Node.js environments.

### Prerequisites

```bash
# Verify npm authentication
npm whoami  # Should show your username

# Verify you're in @beads org
npm org ls beads
```

### Update and Test

```bash
# Navigate to npm package
cd npm-package

# Version should already be updated by bump-version.sh
cat package.json | grep version

# Run all tests
npm run test:all

# Should see:
# ✅ All unit tests passed
# ✅ All integration tests passed
```

### Test Installation Locally

```bash
# Pack the package
npm pack

# Install globally from tarball
npm install -g ./beads-bd-0.22.0.tgz

# Verify binary downloads correctly
bd version  # Should show 0.22.0

# Test in a project
mkdir /tmp/test-npm-bd
cd /tmp/test-npm-bd
git init
bd init
bd create "Test issue" -p 1
bd list

# Cleanup
npm uninstall -g @beads/bd
rm -rf /tmp/test-npm-bd
cd -
rm beads-bd-0.22.0.tgz
```

### Publish to npm

```bash
# IMPORTANT: Ensure GitHub release with binaries is live first!
# The postinstall script downloads from GitHub releases

# Publish to npm (first time use --access public)
npm publish --access public

# Or for subsequent releases
npm publish
```

### Verify npm Release

```bash
# Check package page
open https://www.npmjs.com/package/@beads/bd

# Install and test
npm install -g @beads/bd
bd version  # Should show 0.22.0

# Test postinstall downloaded correct binary
which bd
bd --help
```

## 7. Verify Release

After all distribution channels are updated, verify each one:

### GitHub

```bash
# Download and test binary
wget https://github.com/steveyegge/beads/releases/download/v0.22.0/beads_0.22.0_darwin_arm64.tar.gz
tar -xzf beads_0.22.0_darwin_arm64.tar.gz
./bd version
```

### Homebrew

```bash
brew update
brew upgrade bd
bd version
```

### PyPI

```bash
pip install --upgrade beads-mcp
python -m beads_mcp --version
```

### npm

```bash
npm install -g @beads/bd
bd version
```

### Installation Script

```bash
# Test quick install script
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
bd version
```

## Hotfix Releases

For urgent bug fixes:

```bash
# Create hotfix branch from tag
git checkout -b hotfix/v0.22.1 v0.22.0

# Make fixes
# ... edit files ...

# Bump version to 0.22.1
./scripts/bump-version.sh 0.22.1 --commit

# Tag and release
git tag -a v0.22.1 -m "Hotfix release v0.22.1"
git push origin hotfix/v0.22.1
git push origin v0.22.1

# Follow normal release process
goreleaser release --clean

# Merge back to main
git checkout main
git merge hotfix/v0.22.1
git push origin main
```

## Rollback Procedure

If a release has critical issues:

### 1. Mark GitHub Release as Pre-release

```bash
gh release edit v0.22.0 --prerelease
```

### 2. Create Hotfix Release

Follow hotfix procedure above to release 0.22.1.

### 3. Revert Homebrew (If Needed)

```bash
cd homebrew-beads
git revert HEAD
git push
```

### 4. Deprecate npm Package (If Needed)

```bash
npm deprecate @beads/bd@0.22.0 "Critical bug, please upgrade to 0.22.1"
```

### 5. Yank PyPI Release (If Needed)

```bash
# Can't delete, but can yank (hide from pip install)
# Contact PyPI support or use web interface
```

## Automation Opportunities

### GitHub Actions

Create `.github/workflows/release.yml`:

```yaml
name: Release
on:
  push:
    tags:
      - 'v*'

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
      - uses: goreleaser/goreleaser-action@v4
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}

  npm:
    needs: goreleaser
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-node@v3
        with:
          node-version: '18'
          registry-url: 'https://registry.npmjs.org'
      - run: cd npm-package && npm publish --access public
        env:
          NODE_AUTH_TOKEN: ${{ secrets.NPM_TOKEN }}

  pypi:
    needs: goreleaser
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-python@v4
      - run: |
          cd integrations/mcp/server
          pip install build twine
          python -m build
          twine upload dist/*
        env:
          TWINE_USERNAME: __token__
          TWINE_PASSWORD: ${{ secrets.PYPI_TOKEN }}
```

## Post-Release

After a successful release:

1. **Upgrade local beads-mcp installation** to the new version:
   ```bash
   # Option 1: Use the bump-version.sh script (recommended during version bump)
   ./scripts/bump-version.sh <version> --upgrade-mcp

   # Option 2: Manual upgrade via pip (if installed globally)
   pip install --upgrade beads-mcp

   # Option 3: Manual upgrade via uv tool (if installed as a tool)
   uv tool upgrade beads-mcp

   # Verify the new version
   pip show beads-mcp | grep Version

   # Restart Claude Code or MCP session to pick up the new version
   # The MCP server will load the newly installed version
   ```

   **Note:** The `--upgrade-mcp` flag can be combined with other flags:
   ```bash
   # Update versions, commit, install bd binary, and upgrade beads-mcp all at once
   ./scripts/bump-version.sh 0.24.3 --commit --install --upgrade-mcp
   ```

2. **Restart local daemons** to pick up the new version:
   ```bash
   bd daemons killall --json
   # Daemons will auto-restart with new version on next bd command
   ```

3. **Announce** on relevant channels (Twitter, blog, etc.)
4. **Update documentation** if needed
5. **Close milestone** on GitHub if using milestones
6. **Update project board** if using project management
7. **Monitor** for issues in the first 24-48 hours

## Troubleshooting

### "Tag already exists"

```bash
# Delete tag locally and remotely
git tag -d v0.22.0
git push origin :refs/tags/v0.22.0

# Recreate
git tag -a v0.22.0 -m "Release v0.22.0"
git push origin v0.22.0
```

### "npm publish fails with EEXIST"

```bash
# Version already published, bump version
npm version patch
npm publish
```

### "Binary download fails in npm postinstall"

```bash
# Ensure GitHub release is published first
# Check binary URL is correct
# Verify version matches in package.json and GitHub release
```

### "GoReleaser build fails"

```bash
# Check .goreleaser.yml syntax
goreleaser check

# Test build locally
goreleaser build --snapshot --clean
```

## Version Numbering

Beads follows [Semantic Versioning](https://semver.org/):

- **MAJOR** (x.0.0): Breaking changes
- **MINOR** (0.x.0): New features, backwards compatible
- **PATCH** (0.0.x): Bug fixes, backwards compatible

Examples:
- `0.21.5` → `0.22.0`: New features (minor bump)
- `0.22.0` → `0.22.1`: Bug fix (patch bump)
- `0.22.1` → `1.0.0`: Stable release (major bump)

## Release Cadence

- **Minor releases**: Every 2-4 weeks (new features)
- **Patch releases**: As needed (bug fixes)
- **Major releases**: When breaking changes are necessary

## Questions?

- Open an issue: https://github.com/steveyegge/beads/issues
- Check existing releases: https://github.com/steveyegge/beads/releases
