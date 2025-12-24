#!/bin/bash
# sign-windows.sh - Sign Windows executables using osslsigncode
#
# This script signs Windows binaries with an Authenticode certificate.
# It's designed to be called from GoReleaser hooks or CI/CD pipelines.
#
# Required environment variables:
#   WINDOWS_SIGNING_CERT_PFX_BASE64 - Base64-encoded PFX certificate file
#   WINDOWS_SIGNING_CERT_PASSWORD   - Password for the PFX certificate
#
# Optional environment variables:
#   TIMESTAMP_SERVER - RFC3161 timestamp server URL (default: DigiCert)
#
# Usage:
#   ./sign-windows.sh <path-to-exe>
#   ./sign-windows.sh dist/bd-windows-amd64_windows_amd64_v1/bd.exe
#
# For GoReleaser integration, add to .goreleaser.yml:
#   builds:
#     - id: bd-windows-amd64
#       hooks:
#         post:
#           - ./scripts/sign-windows.sh "{{ .Path }}"

set -euo pipefail

# Configuration
TIMESTAMP_SERVER="${TIMESTAMP_SERVER:-http://timestamp.digicert.com}"
CERT_FILE="/tmp/signing-cert.pfx"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

cleanup() {
    # Remove temporary certificate file
    if [[ -f "$CERT_FILE" ]]; then
        rm -f "$CERT_FILE"
    fi
}

trap cleanup EXIT

# Check for required argument
if [[ $# -lt 1 ]]; then
    log_error "Usage: $0 <path-to-exe>"
    exit 1
fi

EXE_PATH="$1"

# Verify file exists
if [[ ! -f "$EXE_PATH" ]]; then
    log_error "File not found: $EXE_PATH"
    exit 1
fi

# Check for required environment variables
if [[ -z "${WINDOWS_SIGNING_CERT_PFX_BASE64:-}" ]]; then
    log_warn "WINDOWS_SIGNING_CERT_PFX_BASE64 not set - skipping code signing"
    log_info "To enable Windows code signing:"
    log_info "  1. Obtain an EV code signing certificate"
    log_info "  2. Export it as a PFX file"
    log_info "  3. Base64 encode: base64 -i cert.pfx"
    log_info "  4. Add as GitHub secret: WINDOWS_SIGNING_CERT_PFX_BASE64"
    exit 0
fi

if [[ -z "${WINDOWS_SIGNING_CERT_PASSWORD:-}" ]]; then
    log_error "WINDOWS_SIGNING_CERT_PASSWORD not set"
    exit 1
fi

# Check for osslsigncode
if ! command -v osslsigncode &> /dev/null; then
    log_error "osslsigncode not found. Install it with:"
    log_error "  Ubuntu/Debian: apt-get install osslsigncode"
    log_error "  macOS: brew install osslsigncode"
    exit 1
fi

log_info "Signing Windows executable: $EXE_PATH"

# Decode certificate from base64
echo "$WINDOWS_SIGNING_CERT_PFX_BASE64" | base64 -d > "$CERT_FILE"

# Sign the executable
osslsigncode sign \
    -pkcs12 "$CERT_FILE" \
    -pass "$WINDOWS_SIGNING_CERT_PASSWORD" \
    -n "beads - AI-supervised issue tracker" \
    -i "https://github.com/steveyegge/beads" \
    -t "$TIMESTAMP_SERVER" \
    -in "$EXE_PATH" \
    -out "${EXE_PATH}.signed"

# Replace original with signed version
mv "${EXE_PATH}.signed" "$EXE_PATH"

log_info "Successfully signed: $EXE_PATH"

# Verify the signature
log_info "Verifying signature..."
osslsigncode verify -in "$EXE_PATH" || {
    log_warn "Signature verification returned non-zero (may still be valid)"
}

log_info "Windows code signing complete"
