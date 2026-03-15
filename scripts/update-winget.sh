#!/bin/bash
#
# Update winget manifest files for a new release
#
# Usage: ./scripts/update-winget.sh <version>
# Example: ./scripts/update-winget.sh 0.31.0
#

set -e

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
    echo "Usage: $0 <version>"
    echo "Example: $0 0.31.0"
    exit 1
fi

# Remove 'v' prefix if present
VERSION="${VERSION#v}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WINGET_DIR="$SCRIPT_DIR/../winget"

# Get SHA256 from release checksums
echo "Fetching SHA256 for v$VERSION..."
SHA256=$(curl -sL "https://github.com/steveyegge/beads/releases/download/v$VERSION/checksums.txt" | grep windows | awk '{print $1}')

if [ -z "$SHA256" ]; then
    echo "Error: Could not find Windows checksum for v$VERSION"
    echo "Make sure the release exists: https://github.com/steveyegge/beads/releases/tag/v$VERSION"
    exit 1
fi

# Convert to uppercase for winget
SHA256=$(echo "$SHA256" | tr '[:lower:]' '[:upper:]')

echo "SHA256: $SHA256"
echo ""
echo "Updating manifest files..."

# Update version manifest
cat > "$WINGET_DIR/SteveYegge.beads.yaml" << EOF
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.version.1.6.0.schema.json
PackageIdentifier: SteveYegge.beads
PackageVersion: $VERSION
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.6.0
EOF

# Update installer manifest
cat > "$WINGET_DIR/SteveYegge.beads.installer.yaml" << EOF
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.installer.1.6.0.schema.json
PackageIdentifier: SteveYegge.beads
PackageVersion: $VERSION
InstallerType: zip
NestedInstallerType: portable
NestedInstallerFiles:
  - RelativeFilePath: bd.exe
    PortableCommandAlias: bd
Installers:
  - Architecture: x64
    InstallerUrl: https://github.com/steveyegge/beads/releases/download/v$VERSION/beads_${VERSION}_windows_amd64.zip
    InstallerSha256: $SHA256
ManifestType: installer
ManifestVersion: 1.6.0
EOF

# Update locale manifest
cat > "$WINGET_DIR/SteveYegge.beads.locale.en-US.yaml" << EOF
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.defaultLocale.1.6.0.schema.json
PackageIdentifier: SteveYegge.beads
PackageVersion: $VERSION
PackageLocale: en-US
Publisher: Steve Yegge
PublisherUrl: https://github.com/steveyegge
PublisherSupportUrl: https://github.com/steveyegge/beads/issues
Author: Steve Yegge
PackageName: beads
PackageUrl: https://github.com/steveyegge/beads
License: MIT
LicenseUrl: https://github.com/steveyegge/beads/blob/main/LICENSE
Copyright: Copyright (c) 2024 Steve Yegge
ShortDescription: Distributed, Dolt-powered graph issue tracker for AI agents
Description: |
  beads (bd) is a distributed, Dolt-powered graph issue tracker designed for AI-supervised coding workflows.
  It provides a persistent, structured memory for coding agents, replacing messy markdown plans with a
  dependency-aware graph that allows agents to handle long-horizon tasks without losing context.
Moniker: bd
Tags:
  - issue-tracker
  - ai
  - coding-assistant
  - git
  - cli
  - developer-tools
ReleaseNotesUrl: https://github.com/steveyegge/beads/releases/tag/v$VERSION
ManifestType: defaultLocale
ManifestVersion: 1.6.0
EOF

echo ""
echo "✓ Updated winget manifests for v$VERSION"
echo ""
echo "Next steps:"
echo "1. Commit these changes"
echo "2. Fork https://github.com/microsoft/winget-pkgs"
echo "3. Copy winget/*.yaml to manifests/s/SteveYegge/beads/$VERSION/"
echo "4. Submit PR to microsoft/winget-pkgs"
