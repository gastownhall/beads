#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "WARNING: scripts/test-cgo.sh is deprecated." >&2
echo "WARNING: Use $SCRIPT_DIR/test-icu-path.sh for the explicit ICU-only path, or ./scripts/test.sh for normal validation." >&2

exec "$SCRIPT_DIR/test-icu-path.sh" "$@"
