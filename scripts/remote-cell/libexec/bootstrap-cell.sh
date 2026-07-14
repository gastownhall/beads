#!/usr/bin/env bash
# Compat wrapper — prefer make init-cell / libexec/init-cell.sh
set -euo pipefail
exec "$(cd "$(dirname "$0")" && pwd)/init-cell.sh" "$@"
