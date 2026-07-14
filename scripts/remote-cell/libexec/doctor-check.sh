#!/usr/bin/env bash
# Compat wrapper — prefer make health / libexec/health.sh
set -euo pipefail
exec "$(cd "$(dirname "$0")" && pwd)/health.sh" "$@"
