#!/usr/bin/env bash
# Mint a per-person credential for the same project.
#
# Lab gateway binds one bearer token per process, so each person gets their own
# gateway process on the next free port (same Dolt DB). Product direction is
# multi-token on one process; this keeps the current lab binary correct.
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "$0")" && pwd)/common.sh"

PERSON="${1:-}"
[[ -n "$PERSON" ]] || die "usage: invite.sh <person>"
[[ "$PERSON" =~ ^[a-zA-Z0-9_-]+$ ]] || die "person must be alphanumeric/_/-"
[[ "$PERSON" != "admin" ]] || die "admin invite is created by make init-cell"

need_cmd python3
load_cell_env
ensure_dirs
sync_runtime
GW=$(gateway_bin)

TOK_FILE="$DATA/secrets/token_${PERSON}"
IDK_FILE="$DATA/secrets/idemp_${PERSON}"
if [[ ! -f "$TOK_FILE" ]]; then write_secret "$TOK_FILE" "$(rand_hex 32)"; fi
if [[ ! -f "$IDK_FILE" ]]; then write_secret "$IDK_FILE" "$(rand_hex 32)"; fi
while [[ "$(tr -d '\n' <"$TOK_FILE")" == "$(tr -d '\n' <"$IDK_FILE")" ]]; do
  write_secret "$IDK_FILE" "$(rand_hex 32)"
done

PORT_FILE="$DATA/ports.tsv"
touch "$PORT_FILE"
PORT=$(python3 - <<PY
import socket
from pathlib import Path
used=set()
p=Path("$PORT_FILE")
if p.exists():
    for line in p.read_text().splitlines():
        parts=line.split()
        if len(parts)>=2:
            try: used.add(int(parts[1]))
            except ValueError: pass
used.add(int("${REMOTE_CELL_GATEWAY_PORT:-7707}"))
for port in range(7708, 7800):
    if port in used:
        continue
    s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        s.bind(("127.0.0.1", port)); s.close(); print(port); break
    except OSError:
        continue
else:
    raise SystemExit("no free port in 7708-7799")
PY
)

# update ports registry
grep -v "^${PERSON} " "$PORT_FILE" >"${PORT_FILE}.tmp" 2>/dev/null || true
mv -f "${PORT_FILE}.tmp" "$PORT_FILE"
echo "$PERSON $PORT" >>"$PORT_FILE"

RT_PW="$REMOTE_CELL_RUNTIME_ROOT/secrets/pw_default"
RT_TOK="$REMOTE_CELL_RUNTIME_ROOT/secrets/token_${PERSON}"
RT_IDK="$REMOTE_CELL_RUNTIME_ROOT/secrets/idemp_${PERSON}"
ln -f "$TOK_FILE" "$RT_TOK" 2>/dev/null || cp -f "$TOK_FILE" "$RT_TOK"
ln -f "$IDK_FILE" "$RT_IDK" 2>/dev/null || cp -f "$IDK_FILE" "$RT_IDK"
chmod 600 "$RT_TOK" "$RT_IDK"
QUEUE="$REMOTE_CELL_RUNTIME_ROOT/queues/q_default_${PERSON}.db"
rm -f "${QUEUE}" "${QUEUE}-wal" "${QUEUE}-shm" "${QUEUE}.lock" 2>/dev/null || true

PID_FILE="$DATA/gateway_${PERSON}.pid"
if [[ -f "$PID_FILE" ]]; then
  kill "$(cat "$PID_FILE")" 2>/dev/null || true
  rm -f "$PID_FILE"
fi
free_port "$PORT"

log "start agent gateway for $PERSON on 127.0.0.1:$PORT (same project DB)"
nohup "$GW" \
  -database "$REMOTE_CELL_DATABASE" \
  -dolt-host 127.0.0.1 -dolt-port 13360 \
  -listen "127.0.0.1:$PORT" \
  -lab-id "$REMOTE_CELL_LAB_ID" \
  -project-id "$REMOTE_CELL_PROJECT_ID" \
  -password-file "$RT_PW" \
  -token-file "$RT_TOK" \
  -idempotency-key-file "$RT_IDK" \
  -queue "$QUEUE" \
  -runtime-root "$REMOTE_CELL_RUNTIME_ROOT" \
  -subject "agent-${PERSON}" \
  -max-open 8 -max-idle 4 \
  >"$DATA/logs/gateway_${PERSON}.log" 2>&1 &
echo $! >"$PID_FILE"
wait_tcp 127.0.0.1 "$PORT" "gateway($PERSON)" 80

INVITE="$DATA/invites/${PERSON}.env"
umask 077
cat >"$INVITE" <<EOF
# Remote cell invite for ${PERSON} — do not commit
BEADS_GATEWAY_URL=http://127.0.0.1:${PORT}
BEADS_PROJECT_ID=${REMOTE_CELL_PROJECT_ID}
BEADS_TOKEN=$(tr -d '\n' <"$TOK_FILE")
BEADS_SUBJECT=agent-${PERSON}
EOF
chmod 600 "$INVITE"

log "invite written: $INVITE"
log "securely send to $PERSON → they run: make join INVITE=$INVITE"
