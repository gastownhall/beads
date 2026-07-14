#!/usr/bin/env bash
# Stop gateways + Dolt for this cell (pid files only — no broad pkill).
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "$0")" && pwd)/common.sh"

ensure_dirs

stop_pidfile() {
  local f="$1"
  [[ -f "$f" ]] || return 0
  local pid
  pid=$(cat "$f" 2>/dev/null || true)
  if [[ -n "${pid:-}" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    sleep 0.1
    kill -9 "$pid" 2>/dev/null || true
  fi
  rm -f "$f"
}

for f in "$DATA"/gateway*.pid "$DATA"/dolt.pid; do
  stop_pidfile "$f"
done

# Free known ports in case pid files were lost
for port in 13360 7707 7708 7709 7710 7711 7712; do
  free_port "$port"
done

docker rm -f beads-remote-cell-dolt 2>/dev/null || true
log "cell stopped"
