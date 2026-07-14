#!/usr/bin/env bash
# Shared helpers for remote-cell (experimental).
set -euo pipefail

ROOT="${REMOTE_CELL_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
DATA="${REMOTE_CELL_DATA:-$ROOT/data}"
BIN="${REMOTE_CELL_BIN:-$ROOT/bin}"

# Lab gateway only accepts these runtime roots today.
if [[ "$(uname -s)" == "Darwin" ]]; then
  DEFAULT_RUNTIME="/private/tmp/beads-perf-lab-runtime"
else
  DEFAULT_RUNTIME="/var/lib/beads-perf-lab"
fi
export REMOTE_CELL_RUNTIME_ROOT="${REMOTE_CELL_RUNTIME_ROOT:-$DEFAULT_RUNTIME}"

CELL_ENV="$DATA/cell.env"

log() { printf '[remote-cell] %s\n' "$*"; }
die() { printf '[remote-cell] error: %s\n' "$*" >&2; exit 1; }

need_cmd() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }

ensure_dirs() {
  mkdir -p "$DATA"/{secrets,queues,logs,invites,public,dolt-host} "$BIN"
  mkdir -p "$REMOTE_CELL_RUNTIME_ROOT"/{secrets,queues,logs}
  chmod 700 "$REMOTE_CELL_RUNTIME_ROOT" 2>/dev/null || true
}

sync_runtime() {
  ensure_dirs
  if [[ "$DATA" == "$REMOTE_CELL_RUNTIME_ROOT" ]]; then
    return 0
  fi
  mkdir -p "$REMOTE_CELL_RUNTIME_ROOT"/{secrets,queues,logs}
  local f base
  for f in "$DATA"/secrets/*; do
    [[ -e "$f" ]] || continue
    base=$(basename "$f")
    ln -f "$f" "$REMOTE_CELL_RUNTIME_ROOT/secrets/$base" 2>/dev/null \
      || cp -f "$f" "$REMOTE_CELL_RUNTIME_ROOT/secrets/$base"
    chmod 600 "$REMOTE_CELL_RUNTIME_ROOT/secrets/$base" 2>/dev/null || true
  done
}

rand_hex() {
  local n="${1:-32}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$n"
  else
    python3 -c "import secrets; print(secrets.token_hex($n))"
  fi
}

write_secret() {
  local path="$1" value="$2"
  umask 077
  printf '%s\n' "$value" >"$path"
  chmod 600 "$path"
}

load_cell_env() {
  [[ -f "$CELL_ENV" ]] || die "no cell yet — run: make init-cell"
  set -a
  # shellcheck disable=SC1090
  source "$CELL_ENV"
  set +a
}

tcp_up() {
  local host="$1" port="$2"
  python3 - "$host" "$port" <<'PY'
import socket, sys
host, port = sys.argv[1], int(sys.argv[2])
s = socket.socket(); s.settimeout(0.4)
try:
    s.connect((host, port)); s.close(); sys.exit(0)
except Exception:
    sys.exit(1)
PY
}

dolt_up() { tcp_up 127.0.0.1 13360; }
gateway_up() { tcp_up 127.0.0.1 "${1:-7707}"; }

# Free a TCP port safely (no macOS-broken fuser -k port/tcp).
free_port() {
  local port="$1"
  python3 - "$port" <<'PY' || true
import os, signal, subprocess, sys
port = int(sys.argv[1])
try:
    out = subprocess.check_output(["lsof", f"-tiTCP:{port}", "-sTCP:LISTEN"], text=True)
except Exception:
    sys.exit(0)
for pid in {int(x) for x in out.split() if x.strip().isdigit()}:
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
PY
  sleep 0.15
}

wait_tcp() {
  local host="$1" port="$2" name="$3" tries="${4:-60}"
  local i
  for i in $(seq 1 "$tries"); do
    if tcp_up "$host" "$port"; then
      log "$name ready on $host:$port"
      return 0
    fi
    sleep 0.25
  done
  die "$name did not become ready on $host:$port"
}

gateway_bin() {
  if [[ -x "$BIN/bd-gateway" ]]; then echo "$BIN/bd-gateway"
  elif [[ -x "$BIN/gateway" ]]; then echo "$BIN/gateway"
  else die "missing gateway binary — run: make build"
  fi
}

provision_bin() {
  if [[ -x "$BIN/cell-provision" ]]; then echo "$BIN/cell-provision"
  elif [[ -x "$BIN/lab-bootstrap" ]]; then echo "$BIN/lab-bootstrap"
  else die "missing cell-provision binary — run: make build"
  fi
}
