# Beads Scripts

Utility scripts for maintaining the beads project.

## release.sh (⭐ The Easy Button)

**One-command release** from version bump to local installation.

### Usage

```bash
# Full release (does everything)
./scripts/release.sh 0.9.3

# Preview what would happen
./scripts/release.sh 0.9.3 --dry-run
```

### What It Does

This master script automates the **entire release process**:

1. ✅ Kills running daemons (avoids version conflicts)
2. ✅ Runs tests and linting
3. ✅ Bumps version in all files
4. ✅ Commits and pushes version bump
5. ✅ Creates and pushes git tag
6. ✅ Updates Homebrew formula
7. ✅ Upgrades local brew installation
8. ✅ Verifies everything works

**After this script completes, your system is running the new version!**

### Examples

```bash
# Release version 0.9.3
./scripts/release.sh 0.9.3

# Preview a release (no changes made)
./scripts/release.sh 1.0.0 --dry-run
```

### Prerequisites

- Clean git working directory
- All changes committed
- golangci-lint installed
- Homebrew installed (for local upgrade)
- Push access to steveyegge/beads and steveyegge/homebrew-beads

### Output

The script provides colorful, step-by-step progress output:
- 🟨 Yellow: Current step
- 🟩 Green: Step completed
- 🟥 Red: Errors
- 🟦 Blue: Section headers

### What Happens Next

After the script finishes:
- GitHub Actions builds binaries for all platforms (~5 minutes)
- PyPI package is published automatically
- Users can `brew upgrade bd` to get the new version
- GitHub Release is created with binaries and changelog

---

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

---

## update-homebrew.sh

Automatically updates the Homebrew formula with GoReleaser release artifacts.

### Usage

```bash
# Update formula after pushing git tag
./scripts/update-homebrew.sh 0.9.3

# Use custom tap directory
TAP_DIR=~/homebrew-beads ./scripts/update-homebrew.sh 0.9.3
```

### What It Does

This script automates the Homebrew formula update process:

1. **Waits** for GitHub Actions release build (~5 minutes, checks every 30s)
2. **Downloads** checksums.txt from the GitHub release
3. **Extracts** SHA256s for all platform-specific binaries:
   - macOS ARM64 (Apple Silicon)
   - macOS AMD64 (Intel)
   - Linux AMD64
   - Linux ARM64
4. **Clones/updates** the homebrew-beads tap repository
5. **Updates** Formula/bd.rb with new version and all SHA256s
6. **Commits and pushes** the changes

### Important Notes

- **Run AFTER pushing the git tag** - the script waits for GitHub Actions to finish
- **Uses GoReleaser artifacts**, not source tarballs (fixed in v0.23.0)
- **Automatically waits** up to 7.5 minutes for release build to complete
- **Updates all platforms** in a single operation

### Examples

```bash
# Standard usage (after git tag push)
git tag v0.9.3 && git push origin v0.9.3
./scripts/update-homebrew.sh 0.9.3

# Custom tap directory
TAP_DIR=/path/to/homebrew-beads ./scripts/update-homebrew.sh 0.9.3
```

### Why This Script Exists

Previously, the Homebrew formula update was manual and error-prone:
- Used source tarball SHA256 instead of GoReleaser artifacts (wrong!)
- Required manually computing 4 separate SHA256s
- Easy to forget updating all platforms
- No automation for waiting on GitHub Actions

This script fixes all those issues and is now used by `release.sh`.

---

## sign-windows.sh

Signs Windows executables with an Authenticode certificate using osslsigncode.

### Usage

```bash
# Sign a Windows executable
./scripts/sign-windows.sh path/to/bd.exe

# Environment variables required for signing:
export WINDOWS_SIGNING_CERT_PFX_BASE64="<base64-encoded-pfx>"
export WINDOWS_SIGNING_CERT_PASSWORD="<certificate-password>"
```

### What It Does

This script is called automatically by GoReleaser during the release process:

1. **Decodes** the PFX certificate from base64
2. **Signs** the Windows executable using osslsigncode
3. **Timestamps** the signature using DigiCert's RFC3161 server
4. **Replaces** the original binary with the signed version
5. **Verifies** the signature was applied correctly

### Prerequisites

- `osslsigncode` installed (`apt install osslsigncode` or `brew install osslsigncode`)
- EV code signing certificate exported as PFX file
- GitHub secrets configured:
  - `WINDOWS_SIGNING_CERT_PFX_BASE64` - base64-encoded PFX file
  - `WINDOWS_SIGNING_CERT_PASSWORD` - certificate password

### Graceful Degradation

If the signing secrets are not configured:
- The script prints a warning and exits successfully
- GoReleaser continues without signing
- The release proceeds with unsigned Windows binaries

This allows releases to work before a certificate is acquired.

### Why This Script Exists

Windows code signing helps reduce antivirus false positives that affect Go binaries.
Kaspersky and other AV software commonly flag unsigned Go executables as potentially
malicious due to heuristic detection. See `docs/ANTIVIRUS.md` for details.

---

## Future Scripts

Additional maintenance scripts may be added here as needed.
