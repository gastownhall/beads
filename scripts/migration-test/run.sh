#!/usr/bin/env bash
set -euo pipefail

# Compatibility entry point for the focused historical-Dolt upgrade corpus.
# Keep this filename stable for local and CI callers; all behavior belongs in
# the explicit authentic-binary test rather than recipe discovery.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$SCRIPT_DIR/legacy-bridge-test.sh"
exec "$SCRIPT_DIR/historical-dolt-upgrade-test.sh" "$@"
