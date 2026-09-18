# Release Process for Beads

This document describes the complete release process for beads, including GitHub releases, Homebrew, PyPI (MCP server), and npm packages.

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Release Branches](#release-branches)
- [Release Checklist](#release-checklist)
- [1. Prepare Release](#1-prepare-release)
- [2. GitHub Release](#2-github-release)
- [3. Homebrew Update](#3-homebrew-update)
- [4. PyPI Release (MCP Server)](#4-pypi-release-mcp-server)
- [5. Plugin Marketplace Update](#5-plugin-marketplace-update)
- [6. npm Package Release](#6-npm-package-release)
- [7. Verify Release](#7-verify-release)
- [Version Numbering](#version-numbering)
- [Prerelease / Release Candidate (RC) Workflow](#prerelease--release-candidate-rc-workflow)
- [Hotfix Releases](#hotfix-releases)
- [Rollback Procedure](#rollback-procedure)

## Overview

A beads release involves multiple distribution channels:

1. **GitHub Release** - Binary downloads for all platforms
2. **Homebrew** - macOS/Linux package manager
3. **PyPI** - Python MCP server (`beads-mcp`)
4. **npm** - Node.js package for Claude Code for Web (`@beads/bd`)

### The Easy Way (Recommended)

For routine releases, run the release molecule:

```bash
bd mol wisp beads-release --var version=1.3.0
```

`.beads/formulas/beads-release.formula.toml` defines the whole flow as
resumable steps with a CI gate: preflight → CHANGELOG/`info.go` →
version bump → commit/tag/push → gate on `release.yml` → verify
GitHub/npm/PyPI/Homebrew → local install. `./scripts/release.sh 1.3.0` is a
thin gateway that prints this command; it does not run a release itself.

The rest of this document is the manual / step-by-step process, useful for
understanding what the molecule does and for handling edge cases (hotfixes,
rollbacks, manual PyPI/npm publishes).

## Prerequisites

### Required Tools

- `git` with push access to gastownhall/beads
- `goreleaser` for building binaries
- `npm` with authentication (for npm releases)
- `python3` and `twine` (for PyPI releases)
- `gh` CLI (GitHub CLI, optional but recommended)

### Required Access

- GitHub: Write access to repository and ability to create releases
- GitHub: Ability to create protected `v*` release tags. The repository should
  restrict `refs/tags/v*` creation, updates, and deletion to trusted release
  maintainers.
- PyPI: Maintainer access to `beads-mcp` package
- npm: Member of `@beads` organization

### Verify Setup

```bash
# Check git
git remote -v  # Should show gastownhall/beads

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

## Release Branches

Releases are cut from a `release/x.y.z` branch, not from whatever `main`
happens to be when the tag is pushed. `main` moves continuously and the
release-critical gates do not all run on every PR, so "tag the tip of main"
means tagging a SHA nobody has fully tested. A burned tag is never reused
(the v1.1.1 and v1.2.0 precedents), so that gamble is expensive.

**Cut the branch from a SHA that is green on both `Main` and
`Nightly Full Tests`.** The `Main` workflow runs on push to `main` and the
`Nightly Full Tests` workflow runs on a 2am UTC schedule, so the newest SHA
with both is usually a few hours behind the tip:

```bash
gh run list --workflow Main --branch main --status success --limit 5 \
  --json headSha,conclusion,createdAt
gh run list --workflow "Nightly Full Tests" --status success --limit 3 \
  --json headSha,conclusion,createdAt

git fetch origin
git branch release/1.3.0 <the-green-sha>
git push origin release/1.3.0
```

**Everything targeted at the release lands on the release branch as a PR.**
Base the PR on `release/x.y.z`, not `main`, and label it
`status/needs-review-auto` so it goes through the automated review workflow
like any other change. That includes the release-prep PR itself (version
bump, CHANGELOG, `cmd/bd/info.go`) and any fix the release validation turns
up. Nothing is pushed straight to the release branch.

**Tag from the release branch**, following [1. Prepare Release](#1-prepare-release)
onward with the branch checked out in place of `main`.

**Forward-port to `main` after the release ships.** Every commit that landed
only on the release branch — the version bump included — needs a PR back to
`main`, or the next release silently drops it and the version files disagree
with the newest tag. Do this as one PR immediately after the tag is pushed,
while the set of release-only commits is still obvious:

```bash
git log --oneline main..release/1.3.0
```

(See PR #5782 for the shape of a forward-port PR.)

## Release Checklist

Before starting a release:

- [ ] `release/x.y.z` branch cut from a SHA green on `Main` **and**
      `Nightly Full Tests` (see [Release Branches](#release-branches))
- [ ] All tests passing (`go test ./...`)
- [ ] npm package tests passing (`cd npm-package && npm run test:all`)
- [ ] **Upgrade smoke tests pass** (`make test-upgrade`) — see [Release Stability Gate](engdocs/RELEASE-STABILITY-GATE.md)
- [ ] **Regression tests pass** (`make test-regression`)
- [ ] **Every release target cross-compiles** — see
      [Cross-compile before tagging](#cross-compile-before-tagging). PR CI does
      not build them ([#5662](https://github.com/gastownhall/beads/issues/5662)),
      and a target that fails at tag time burns the tag.
- [ ] **CHANGELOG.md updated with release notes** (see format below)
- [ ] **CHANGELOG rollup checked** — nothing left under `[Unreleased]` that
      belongs in this release, and nothing filed under a *previous* release's
      section that shipped after that tag. Verify with
      `git merge-base --is-ancestor <commit> <previous-tag>` when in doubt.
- [ ] **`cmd/bd/info.go` `versionChanges` entry added** for this version
- [ ] **Breaking changes documented** with migration steps and recovery instructions
- [ ] **Docs cutover planned** — `docs/cli-docs.pin` bump plus
      `./scripts/generate-cli-docs.sh`, and the release line on the docs
      homepage (`docs/index.md`). See
      [Documentation Site (Mintlify)](#documentation-site-mintlify).
- [ ] `./scripts/check-versions.sh` passes
- [ ] No uncommitted changes
- [ ] On the `release/x.y.z` branch and up to date with origin

### Cross-compile before tagging

`.goreleaser.yml` builds darwin, linux, windows **and freebsd**, but PR CI
builds none of the cross targets — the v1.2.0 tag burned on a freebsd
compilation failure that no pre-tag gate could have caught
([#5661](https://github.com/gastownhall/beads/pull/5661) fixed the break,
[#5662](https://github.com/gastownhall/beads/issues/5662) tracks the CI gap).
Until that gap is closed, build them by hand before tagging:

```bash
goreleaser build --snapshot --clean
```

A failure here costs a rebase. The same failure after the tag is pushed costs
the tag.

## 1. Prepare Release

> The snippets below say `main` where they mean "the branch you are releasing
> from". Since [Release Branches](#release-branches), that is `release/x.y.z`:
> substitute it for `main` in every `git push` and `git checkout` here, and
> land the changes as a PR against the release branch rather than pushing
> directly.

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

Commit the CHANGELOG changes and open a PR against the release branch:

```bash
git add CHANGELOG.md
git commit -m "docs: Add CHANGELOG entry for v0.22.0"
git checkout -b release-prep/v0.22.0
git push -u origin release-prep/v0.22.0
gh pr create --base release/0.22.0 --label status/needs-review-auto
```

### Update Version and Create Release Tag

`./scripts/update-versions.sh` rewrites every version reference in one pass.
It does no git operations — commit, tag and push are yours. (`bump-version.sh`
is retired; it now only prints a pointer here.)

```bash
./scripts/update-versions.sh 0.22.0
./scripts/check-versions.sh          # must pass before you commit
```

This updates:
- `cmd/bd/version.go` — CLI version constant. This is the canonical version:
  `check-versions.sh` reads it and compares the gated files against it. Note
  that "gated" is narrower than "updated" — `default.nix` and `README.md` are
  bumped here but checked by nothing, so eyeball them.
- `integrations/beads-mcp/pyproject.toml` — MCP server version
- `integrations/beads-mcp/src/beads_mcp/__init__.py` — MCP Python version
- `integrations/beads-mcp/uv.lock` — the `beads-mcp` pin, via `uv lock`. A
  stale lock fails the release workflow's MCP gate *after* the tag exists and
  can no longer be rewritten; that burned the v1.1.1 tag. Install `uv` before
  bumping, or the script warns and skips it.
- `plugins/beads/.claude-plugin/plugin.json` — Claude plugin version
- `plugins/beads/.codex-plugin/plugin.json` — Codex plugin version
- `plugins/beads/.copilot-plugin/plugin.json` — Copilot plugin version
- `.claude-plugin/marketplace.json` — Claude marketplace version
- `npm-package/package.json` — npm package version
- `.githooks/*` — the `BEGIN`/`END BEADS INTEGRATION v<version>` markers on the
  tracked managed hook sections, held byte-equal to the `cmd/bd/hooks.go`
  generator by `TestTrackedManagedHookSectionsMatchGenerator`. Skipping them
  reddens the branch only after the push (that was the v1.2.0 bump).
- `default.nix` — Nix package version
- `cmd/bd/winres/winres.json`, `cmd/bd/winres/manifest.xml` — Windows PE
  resource metadata (the numeric fields take the base version, with any
  prerelease suffix stripped)
- `README.md` — the static Alpha version badge, if one is present

It does **not** touch `CHANGELOG.md`, `cmd/bd/info.go`, or `default.nix`'s
`vendorHash`. Write the release notes first (above), and run
`./scripts/update-nix-vendorhash.sh` if `go.mod`/`go.sum` changed since the
last tag.

Then commit and land it as a PR — **not** a direct push, and **do not tag
yet**:

```bash
git add -A
git commit -m "chore: bump version to 0.22.0"
git checkout -b release-prep/v0.22.0
git push -u origin release-prep/v0.22.0
gh pr create --base release/0.22.0 --label status/needs-review-auto
```

Tag only after that PR merges, and tag the **merged** commit:

```bash
git checkout release/0.22.0 && git pull
git tag -a v0.22.0 -m "Release v0.22.0"
git push origin v0.22.0
```

Two reasons the order matters, both of which have teeth. With branch
protection on, a direct push to the release branch is rejected — after you
have already minted the tag locally. And with a squash merge, a tag created
before the merge points at a commit that is not an ancestor of
`release/x.y.z`; goreleaser packages `CHANGELOG.md` *from the tag* into every
published archive, so the release would ship the pre-review notes.

Pushing the tag triggers GitHub Actions to build release artifacts
automatically.

The tag workflow re-runs release-critical package gates before publishing:

- `make ci-package-mcp` builds and validates the MCP package, then the PyPI job
  publishes the validated `dist/*` artifact from that gate.
- `make ci-package-npm` validates the npm wrapper package before npm publish.
  publishes GitHub release assets.

The npm publish job also waits for the macOS release assets, because the npm
`postinstall` script downloads platform-specific archives from the GitHub
release.

**Recommended workflow:**

```bash
# 1. Update CHANGELOG.md and cmd/bd/info.go with release notes (manual step)

# 2. Bump versions and verify
./scripts/update-versions.sh 0.22.0
./scripts/check-versions.sh

# 3. Land it on the release branch as a PR (label status/needs-review-auto)

# 4. Test locally, then tag the merged commit and push
git tag -a v0.22.0 -m "Release v0.22.0"
git push origin v0.22.0
```

The release workflow is intentionally gated to `refs/tags/v*`. A manual
workflow dispatch from a branch will skip publishing jobs; manual reruns must
select the release tag.

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

1. Visit https://github.com/gastownhall/beads/releases
2. Verify v0.22.0 is marked as "Latest"
3. Check all platform binaries are present:
   - `beads_0.22.0_darwin_amd64.tar.gz`
   - `beads_0.22.0_darwin_arm64.tar.gz`
   - `beads_0.22.0_linux_amd64.tar.gz`
   - `beads_0.22.0_linux_arm64.tar.gz`
   - `beads_0.22.0_windows_amd64.zip`
   - `checksums.txt`

## 3. Homebrew Update

Homebrew uses the `beads` formula in homebrew-core. Do not publish or revive
the old `bd` formula in `gastownhall/homebrew-beads`; having two independently
updated Homebrew formulas causes version drift and installs the wrong binary for
some users.

Updates to the supported Homebrew formula are handled through Homebrew core
after GitHub Release artifacts are available.

### Verify Homebrew

After the GitHub Release is published, verify the Homebrew package:

```bash
# Update Homebrew
brew update

# Install/upgrade
brew upgrade beads  # or: brew install beads

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

## 5. Plugin Marketplace Update

Update the plugin marketplace metadata files:

```bash
# Update .claude-plugin/marketplace.json
# Change version to match current release
vim .claude-plugin/marketplace.json

# Update plugins/beads/.claude-plugin/plugin.json if needed
vim plugins/beads/.claude-plugin/plugin.json

# Update plugins/beads/.codex-plugin/plugin.json if needed
vim plugins/beads/.codex-plugin/plugin.json

# Commit changes
git add .claude-plugin/ plugins/beads/.claude-plugin/ plugins/beads/.codex-plugin/
git commit -m "chore: Update plugin marketplaces to v0.22.0"
```

**Note:** These files define how beads appears in Claude Code and Codex plugin marketplaces. Version should match the release version.

### Documentation Site (Mintlify)

The published docs are the Mintlify site rooted at `docs/`, deployed from
main via the Mintlify GitHub integration — no release-time docs snapshot is
needed. The site documents the current release line only (see
engdocs/decisions/2026-07-10-mintlify-docs-overhaul.md). Day to day, the
generated CLI reference is kept fresh by `scripts/generate-cli-docs.sh` and
its PR drift gate, not by the release process.

**But the release pin IS a release-time step.** `docs/cli-docs.pin` names
the release tag the docs corpus is generated from and validated against
(engdocs/decisions/2026-07-17-docs-release-pin.md); it lags `main` between
releases by design, so any command or flag added on `main` since the last
bump is invisible to `scripts/check-doc-flags.sh` Check 4 ("covers all live
top-level CLI commands" passes vacuously for it, e.g. wy-gx5rj for `bd
sync`). Bump it as part of THIS release, not a follow-up:

```bash
# After tagging (see "Update Version and Create Release Tag" above).
# generate-cli-docs.sh builds bd from the pinned tag, so the tag must exist.
echo "v0.22.0" > docs/cli-docs.pin
./scripts/generate-cli-docs.sh
git add docs/cli-docs.pin docs/CLI_REFERENCE.md docs/cli-reference docs/docs.json
git commit -m "docs: bump CLI docs pin to v0.22.0"
```

The pin bump lands on `main`, since that is where Mintlify deploys from — fold
it into the forward-port PR described in [Release Branches](#release-branches)
rather than pushing it to the release branch (or to `main`) directly.

Skipping this step doesn't fail fast — Check 4 stays green (it validates
against the *old* pin) until the next bump, at which point every command
added across the skipped releases shows up at once as a pile of "missing
from docs/CLI_REFERENCE.md" failures with no obvious release to blame.

**The docs homepage names the release line, and it is hand-maintained.**
`docs/index.md` carries a line of the form "These docs are for the X.Y.Z
release of beads" with a link to that tag's release notes. Nothing generates
or validates it, so it goes stale silently — it still said 1.1.0 when the pin
had already moved to v1.2.2. Update it in the same commit as the pin:

```bash
rg 'These docs are for the' docs/index.md
```

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

# Version should already be updated by update-versions.sh
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
wget https://github.com/gastownhall/beads/releases/download/v0.22.0/beads_0.22.0_darwin_arm64.tar.gz
tar -xzf beads_0.22.0_darwin_arm64.tar.gz
./bd version
```

### Homebrew

```bash
brew update
brew upgrade beads
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
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
bd version
```

### Docs Cutover

```bash
cat docs/cli-docs.pin                      # the tag just released, not an older one
rg 'These docs are for the' docs/index.md  # the version just released
```

If either is stale, do the bump from the
[Documentation Site (Mintlify)](#documentation-site-mintlify) step above
before calling the release done.

### Release Notes Rollup

```bash
# [Unreleased] should be empty, and the newest dated section is this release
rg -n '^## \[' CHANGELOG.md | head -3
```

Also confirm nothing shipped after the *previous* tag is still filed under
that tag's section — the v1.2.2 section carried an entry for a commit that was
never in v1.2.2, which is invisible until someone reads the release notes and
looks for a feature their binary does not have:

```bash
git merge-base --is-ancestor <commit> <previous-tag> && echo "in the tag" || echo "MISFILED"
```

## Prerelease / Release Candidate (RC) Workflow

Release candidates let a build be validated through the full release pipeline
without promoting it to the stable channels. An RC carries a SemVer prerelease
identifier (e.g. `1.1.0-rc.1`); Python tooling normalizes this to PEP 440 form
(`1.1.0rc1`).

**How a prerelease tag differs from a stable release:**

- **GitHub release** is published and **marked as a prerelease** (goreleaser
  `release.prerelease: auto`), with binaries for all platforms.
- **Homebrew** is not updated (goreleaser `brews.skip_upload: true`; the core
  formula only tracks stable releases).
- **PyPI** and **npm** publish jobs are **skipped**. The `publish-pypi` and
  `publish-npm` jobs are gated with `!contains(github.ref_name, '-')`, so a tag
  containing a `-` never reaches the stable package channels.
- **Docs are unaffected.** The docs site publishes from `main` via the
  Mintlify GitHub integration; there is no release-time docs snapshot for
  either prereleases or stable releases.

### Cut an RC

**An RC is cut from the release branch, like everything else.** This is the
point of an RC: it validates the SHA the stable release will ship. Tagging the
tip of `main` instead validates a *different* SHA by construction — the stable
tag comes off `release/x.y.z` — and re-introduces the "tag a SHA nobody has
fully tested" failure that [Release Branches](#release-branches) exists to
prevent. Note the branch is named for the **base** version: an RC for 1.1.0 is
cut on `release/1.1.0`, not `release/1.1.0-rc.1`.

```bash
# 0. Cut (or check out) the release branch — see Release Branches above.
git checkout release/1.1.0

# 1. Update CHANGELOG.md and cmd/bd/info.go with the RC notes (manual step),
#    same as a stable release. Date the CHANGELOG section.

# 2. Bump versions. update-versions.sh accepts a prerelease identifier, and
#    also refreshes uv.lock (PEP 440 normalizes 1.1.0-rc.1 to 1.1.0rc1).
./scripts/update-versions.sh 1.1.0-rc.1
#    Windows PE numeric fields (winres file_version/product_version and the
#    manifest <assemblyIdentity> version) are set to the base version 1.1.0,
#    because PE versions must be purely numeric; gen-winres.sh strips the
#    prerelease suffix the same way at build time, and check-versions.sh
#    checks those fields against the base version for exactly this reason.

# 3. Validate locally.
./scripts/check-versions.sh

# 4. Open a PR for the RC prep against release/1.1.0 and have it reviewed.
#    RC prep should land through normal review, not auto-merge.
```

After the RC prep merges, cut the tag from the merged commit on the release
branch:

```bash
git checkout release/1.1.0 && git pull
git tag -a v1.1.0-rc.1 -m "Release candidate v1.1.0-rc.1"
git push origin v1.1.0-rc.1
```

Pushing the `v*` tag triggers the release workflow with the prerelease behavior
above. Tag creation is restricted to release maintainers; see
[Prerequisites](#prerequisites).

### Validate and promote

- Install the RC from the GitHub prerelease assets and exercise the changes it
  is gating before promoting.
- To promote to stable, bump to the base version with no suffix
  (`./scripts/update-versions.sh 1.1.0`) **on the same release branch**, so the
  stable tag lands on the SHA the RC validated. The stable release **does**
  publish to Homebrew/PyPI/npm, so follow the standard
  [Prepare Release](#1-prepare-release) steps from there.

## Hotfix Releases

For urgent bug fixes:

A hotfix is a release branch cut from the tag instead of from a green `main`
SHA; everything else — PRs against the branch, the forward-port afterwards —
is the same as [Release Branches](#release-branches).

```bash
# Create the release branch from the tag being fixed
git checkout -b release/0.22.1 v0.22.0
git push origin release/0.22.1

# Land the fix as a PR against release/0.22.1, then bump
./scripts/update-versions.sh 0.22.1
./scripts/check-versions.sh
git commit -am "chore: bump version to 0.22.1"

# Tag and release
git tag -a v0.22.1 -m "Hotfix release v0.22.1"
git push origin release/0.22.1
git push origin v0.22.1

# Forward-port the fix AND the version bump to main
git log --oneline main..release/0.22.1
```

## Rollback Procedure

If a release has critical issues:

### 1. Mark GitHub Release as Pre-release

```bash
gh release edit v0.22.0 --prerelease
```

### 2. Create Hotfix Release

Follow hotfix procedure above to release 0.22.1.

### 3. Deprecate npm Package (If Needed)

```bash
npm deprecate @beads/bd@0.22.0 "Critical bug, please upgrade to 0.22.1"
```

### 4. Yank PyPI Release (If Needed)

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
   # If installed globally
   pip install --upgrade beads-mcp

   # If installed as a uv tool
   uv tool upgrade beads-mcp

   # Verify the new version
   pip show beads-mcp | grep Version

   # Restart Claude Code or MCP session to pick up the new version
   # The MCP server will load the newly installed version
   ```

2. **Forward-port the release branch to `main`** — see
   [Release Branches](#release-branches). Include the `docs/cli-docs.pin` bump
   and the `docs/index.md` release line.

3. **Verify the upgraded CLI**:
   ```bash
   bd version
   bd doctor quick
   ```

4. **Announce** on relevant channels (Twitter, blog, etc.)
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

- **MAJOR** (x.0.0): Removals and incompatible interface changes — a command,
  flag, config key, schema table or published Go API that is deleted or
  changes shape such that a caller written against the old one cannot be
  fixed by changing its inputs.
- **MINOR** (0.x.0): New features, plus **documented behavioral changes that
  ship with migration notes**. A command that starts refusing input it used to
  accept, changes a default, or changes which rows it returns is a minor bump
  when the change is written up in CHANGELOG.md with the migration step and
  the opt-out. It is not a major bump: the interface is unchanged and the
  caller keeps working after a documented adjustment.
- **PATCH** (0.0.x): Bug fixes, no behavioral change beyond the bug.

The MINOR clause is a description of practice, not a loophole. It carries an
obligation: every behavioral break in a minor release must appear in
CHANGELOG.md under that release, name its override or migration step, and be
summarized in `cmd/bd/info.go` so `bd info --whats-new` surfaces it to agents.
A behavioral break that is *not* documented that way is a bug in the release,
not a minor bump.

**1.3.0 is a worked example.** It ships seven documented behavioral breaks —
`bd update --status <done-status>` enforcing close policy, `bd search`
including closed issues, the interactive-only last-touched fallback, `bd human
list` status handling, `bd dolt push` remote-adoption consent, Dolt port
precedence over `BEADS_DOLT_PORT`, and actor `--` decoding — plus a schema
migration and a flag rename (`--profile` → `--cpu-profile`). Every one has a
CHANGELOG entry with its override, and the release is a MINOR bump.

Examples:
- `0.21.5` → `0.22.0`: New features (minor bump)
- `0.22.0` → `0.22.1`: Bug fix (patch bump)
- `1.2.2` → `1.3.0`: New features plus documented behavioral breaks with
  migration notes (minor bump)
- `0.22.1` → `1.0.0`: Stable release (major bump)

**Prereleases:** append a SemVer prerelease identifier for release candidates,
e.g. `1.1.0-rc.1`. Prerelease tags publish a GitHub prerelease only and stay
off the stable Homebrew/PyPI/npm channels — see
[Prerelease / Release Candidate (RC) Workflow](#prerelease--release-candidate-rc-workflow).

## Release Cadence

- **Minor releases**: Every 2-4 weeks (new features)
- **Patch releases**: As needed (bug fixes)
- **Major releases**: When breaking changes are necessary

## Questions?

- Open an issue: https://github.com/gastownhall/beads/issues
- Check existing releases: https://github.com/gastownhall/beads/releases
