#!/usr/bin/env bash
# Start Dolt for the experimental cell on 127.0.0.1:13360.
# Host `dolt` is required (passwordless root over loopback — lab-compatible).
# Docker is intentionally NOT the default: the official image breaks passwordless root.
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "$0")" && pwd)/common.sh"

need_cmd dolt
ensure_dirs

if dolt_up; then
  log "Dolt already listening on 127.0.0.1:13360"
  exit 0
fi

log "starting host Dolt sql-server on 127.0.0.1:13360"
free_port 13360
nohup dolt sql-server --host 127.0.0.1 --port 13360 \
  --data-dir "$DATA/dolt-host" --loglevel warning \
  >"$DATA/logs/dolt.log" 2>&1 &
echo $! >"$DATA/dolt.pid"
wait_tcp 127.0.0.1 13360 "Dolt" 80
