#!/usr/bin/env bash
# Local cell health (NOT `bd doctor`). Prefer: make health
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "$0")" && pwd)/common.sh"

BRIEF=0
[[ "${1:-}" == "--brief" ]] && BRIEF=1

ok=0; warn=0; fail=0
check() {
  local name="$1"; shift
  if "$@"; then
    [[ $BRIEF -eq 1 ]] || printf '  ok  %s\n' "$name"
    ok=$((ok+1))
  else
    printf '  FAIL %s\n' "$name"
    fail=$((fail+1))
  fi
}
soft() {
  local name="$1"; shift
  if "$@"; then
    [[ $BRIEF -eq 1 ]] || printf '  ok  %s\n' "$name"
    ok=$((ok+1))
  else
    printf '  warn %s\n' "$name"
    warn=$((warn+1))
  fi
}

[[ $BRIEF -eq 1 ]] || echo "remote-cell health (standalone; not bd doctor)"

check "bd-gateway or gateway binary" test -x "$BIN/bd-gateway" -o -x "$BIN/gateway"
check "cell-provision or lab-bootstrap binary" test -x "$BIN/cell-provision" -o -x "$BIN/lab-bootstrap"
check "bd-cell binary (schema-matched)" test -x "$BIN/bd-cell"
check "dolt on PATH" command -v dolt >/dev/null
check "Dolt listening 127.0.0.1:13360" dolt_up

if [[ -f "$CELL_ENV" ]]; then
  set -a; # shellcheck disable=SC1090
  source "$CELL_ENV"; set +a
  port="${REMOTE_CELL_GATEWAY_PORT:-7707}"
  check "gateway listening :$port" gateway_up "$port"
  check "cell.env project id set" test -n "${REMOTE_CELL_PROJECT_ID:-}"
  check "admin token file" test -f "${REMOTE_CELL_ADMIN_TOKEN_FILE:-/dev/null}"
  soft "runtime root" test -d "${REMOTE_CELL_RUNTIME_ROOT:-/nonexistent}"
  # live probe
  if gateway_up "$port" && [[ -f "${REMOTE_CELL_ADMIN_TOKEN_FILE:-}" ]]; then
    if python3 - <<PY
import os,sys
sys.path.insert(0, "${ROOT}/libexec")
from http_client import call
url=os.environ.get("REMOTE_CELL_GATEWAY_URL","http://127.0.0.1:${port}")
project=os.environ["REMOTE_CELL_PROJECT_ID"]
token=open(os.environ["REMOTE_CELL_ADMIN_TOKEN_FILE"]).read().strip()
st,doc=call(url, project, token, "GET", "/healthz")
sys.exit(0 if st==200 else 1)
PY
    then
      [[ $BRIEF -eq 1 ]] || printf '  ok  %s\n' "GET /healthz"
      ok=$((ok+1))
    else
      printf '  FAIL %s\n' "GET /healthz"
      fail=$((fail+1))
    fi
  fi
else
  printf '  warn %s\n' "cell not initialized (no data/cell.env)"
  warn=$((warn+1))
fi

soft "evidence index present" test -f "$ROOT/docs/EVIDENCE.md"

echo "summary: ok=$ok warn=$warn fail=$fail"
[[ "$fail" -eq 0 ]]
