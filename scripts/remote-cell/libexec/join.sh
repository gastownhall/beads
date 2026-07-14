#!/usr/bin/env bash
# Person: install invite into ~/.config/beads (no Dolt knowledge).
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "$0")" && pwd)/common.sh"

INVITE="${1:-}"
[[ -f "$INVITE" ]] || die "usage: join.sh /path/to/person.env"

set -a
# shellcheck disable=SC1090
source "$INVITE"
set +a
[[ -n "${BEADS_GATEWAY_URL:-}" ]] || die "invite missing BEADS_GATEWAY_URL"
[[ -n "${BEADS_PROJECT_ID:-}" ]] || die "invite missing BEADS_PROJECT_ID"
[[ -n "${BEADS_TOKEN:-}" ]] || die "invite missing BEADS_TOKEN"

DEST_DIR="${BEADS_REMOTE_CONFIG_DIR:-$HOME/.config/beads}"
DEST="$DEST_DIR/remote-cell.env"
mkdir -p "$DEST_DIR"
umask 077
cp -f "$INVITE" "$DEST"
chmod 600 "$DEST"

cat >"$DEST_DIR/remote.json" <<EOF
{
  "experimental": true,
  "gateway_url": "${BEADS_GATEWAY_URL}",
  "project_id": "${BEADS_PROJECT_ID}",
  "token_env": "BEADS_TOKEN",
  "note": "Token lives in remote-cell.env — never commit either file."
}
EOF
chmod 600 "$DEST_DIR/remote.json"

log "joined remote cell"
log "  credentials: $DEST"
log "  load: set -a; source $DEST; set +a"
