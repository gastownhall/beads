#!/usr/bin/env bash
# Smoke: healthz + ping + create + idempotent retry (must succeed).
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "$0")" && pwd)/common.sh"

need_cmd python3
load_cell_env
gateway_up "${REMOTE_CELL_GATEWAY_PORT:-7707}" || die "gateway not listening — run: make init-cell"

export RC_URL="$REMOTE_CELL_GATEWAY_URL"
export RC_PROJECT="$REMOTE_CELL_PROJECT_ID"
export RC_TOKEN_FILE="${REMOTE_CELL_ADMIN_TOKEN_FILE:-$REMOTE_CELL_TOKEN_FILE}"
export RC_HTTP_CLIENT="$(cd "$(dirname "$0")" && pwd)/http_client.py"

log "smoke against $RC_URL project=$RC_PROJECT"
python3 - <<'PY'
import os, sys, uuid
sys.path.insert(0, os.path.dirname(os.environ["RC_HTTP_CLIENT"]))
from http_client import call, terminal, issue_id_from

url = os.environ["RC_URL"]
project = os.environ["RC_PROJECT"]
token = open(os.environ["RC_TOKEN_FILE"]).read().strip()

st, doc = call(url, project, token, "GET", "/healthz")
print("healthz", st, doc)
assert st == 200 and isinstance(doc, dict) and doc.get("status") == "ok", doc

st, doc = call(url, project, token, "GET", f"/v1/projects/{project}/ping")
print("ping", st, doc)
assert st == 200 and isinstance(doc, dict) and doc.get("status") == "ok", doc

key = "smoke-" + uuid.uuid4().hex[:16]
st, doc = terminal(url, project, token, "issue.create", {
    "title": "[beads-perf-lab] remote-cell smoke hello",
    "description": "automated smoke",
    "type": "task",
    "priority": 2,
}, key=key)
print("create", st, doc.get("status"), issue_id_from(doc))
assert st in (200, 201, 202) and doc.get("status") == "succeeded", doc
iid = issue_id_from(doc)
assert iid, doc

st2, doc2 = terminal(url, project, token, "issue.create", {
    "title": "[beads-perf-lab] remote-cell smoke hello",
    "description": "automated smoke",
    "type": "task",
    "priority": 2,
}, key=key)
print("create_retry", st2, doc2.get("status"), issue_id_from(doc2))
assert st2 in (200, 201, 202) and doc2.get("status") == "succeeded", doc2
iid2 = issue_id_from(doc2)
assert iid2 == iid, (iid, iid2)

print("SMOKE_PASS", iid)
PY
log "smoke passed"
