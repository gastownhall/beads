#!/usr/bin/env bash
# Maintainer / CI entrypoint for experimental remote-cell.
# Usage:
#   GATEWAY_SRC=/path/to/tree ./scripts/remote-cell/ci/run.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ -z "${GATEWAY_SRC:-${BEADS_SRC:-}}" ]]; then
  if [[ -d /tmp/beads-pr5-fix/scripts/bench-remote-server/gateway ]]; then
    export GATEWAY_SRC=/tmp/beads-pr5-fix
  fi
fi

export GATEWAY_SRC="${GATEWAY_SRC:-${BEADS_SRC:-}}"
echo "GATEWAY_SRC=$GATEWAY_SRC"

make reset >/dev/null 2>&1 || true
make all

echo "CI remote-cell: ALL PASS"
