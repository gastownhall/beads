# Beads Scripts

Utility scripts for maintaining the beads project.

## release.sh (Gateway to the Release Molecule)

Creates the tracked `beads-release` workflow.

### Usage

```bash
# Create the release workflow
./scripts/release.sh 0.9.3

# Preview what would happen
./scripts/release.sh 0.9.3 --dry-run
```

### What It Does

This script does not run the full release itself. It creates a `beads-release`
molecule (wisp) and shows the next steps. The molecule owns the staged release
work, ledger entries, and CI gate.

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
- Push access to steveyegge/beads

### Output

The script provides colorful, step-by-step progress output:
- 🟨 Yellow: Current step
- 🟩 Green: Step completed
- 🟥 Red: Errors
- 🟦 Blue: Section headers

### What Happens Next

After the script creates the molecule:
- Work the molecule steps or hand it off
- Let the CI gate handle the release wait without polling
- Follow the documented verification steps for GitHub, Homebrew, PyPI, and npm

---

## update-versions.sh

Updates the version number across all beads components for local/manual version-file edits.

### Usage

```bash
# Update versions locally
./scripts/update-versions.sh 0.9.3
```

### What It Does

Updates version in all these files:
- `cmd/bd/version.go` - bd CLI version constant
- `claude-plugin/.claude-plugin/plugin.json` - Plugin version
- `.claude-plugin/marketplace.json` - Marketplace plugin version
- `integrations/beads-mcp/pyproject.toml` - MCP server version
- `npm-package/package.json` - npm package version
- `README.md` - Alpha status version
- `default.nix` - Nix package version
- `cmd/bd/winres/*` - Windows version metadata

### Features

- **Validates** semantic versioning format (MAJOR.MINOR.PATCH)
- **Verifies** all versions match after update
- **Shows** git diff of changes
- **Cross-platform** compatible (macOS and Linux)

### Examples

```bash
# Bump to 0.9.3 and review changes
./scripts/update-versions.sh 0.9.3
# Review the diff, then manually commit if appropriate
```

### Why This Script Exists

Previously, version bumps only updated `cmd/bd/version.go`, leaving other components out of sync. This script ensures all version numbers stay consistent across the project.

### Safety

- Validates version format before making any changes
- Verifies all versions match after update

## bump-version.sh

Deprecated shim. It now exits and points at:

- `bd mol wisp beads-release --var version=X.Y.Z` for full releases
- `./scripts/update-versions.sh X.Y.Z` for local version-file updates

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
