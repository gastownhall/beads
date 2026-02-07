#!/bin/bash
# gen-winres.sh - Generate Windows PE resource files (.syso) for bd.exe
#
# Embeds version info, application manifest, and metadata into Windows binaries.
# This helps reduce antivirus false positives by making the binary look like
# professionally packaged software (legitimate PE metadata).
#
# Usage:
#   ./scripts/gen-winres.sh              # Use version from version.go
#   ./scripts/gen-winres.sh 0.49.4       # Explicit version
#
# Requires: go-winres (go install github.com/tc-hib/go-winres@latest)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WINRES_DIR="$REPO_ROOT/cmd/bd/winres"
OUT_PREFIX="$REPO_ROOT/cmd/bd/rsrc"

# Determine version
if [[ $# -ge 1 ]]; then
    VERSION="$1"
else
    VERSION=$(grep 'Version = ' "$REPO_ROOT/cmd/bd/version.go" | sed 's/.*"\(.*\)".*/\1/')
fi

echo "[winres] Generating Windows PE resources for bd v${VERSION}"

# Check for go-winres
if ! command -v go-winres &> /dev/null; then
    echo "[winres] Installing go-winres..."
    go install github.com/tc-hib/go-winres@latest
fi

# Generate .syso files
go-winres make \
    --in "$WINRES_DIR/winres.json" \
    --out "$OUT_PREFIX" \
    --product-version "$VERSION" \
    --file-version "$VERSION"

echo "[winres] Generated:"
ls -la "$REPO_ROOT"/cmd/bd/rsrc_windows_*.syso 2>/dev/null || echo "[winres] (no .syso files found - check go-winres output)"
