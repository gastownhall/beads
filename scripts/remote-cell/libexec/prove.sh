#!/usr/bin/env bash
# Stronger local proof: smoke + exclusive claim race + two-agent isolation of tokens.
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "$0")" && pwd)/common.sh"

need_cmd python3
load_cell_env

log "prove: re-run smoke"
"$ROOT/libexec/smoke.sh"

log "prove: invite second agent (alice) if needed"
if [[ ! -f "$DATA/invites/alice.env" ]] || ! gateway_up "$(awk '$1=="alice"{print $2}' "$DATA/ports.tsv" 2>/dev/null | head -1)"; then
  "$ROOT/libexec/invite.sh" alice
fi

export RC_ADMIN_URL="$REMOTE_CELL_GATEWAY_URL"
export RC_PROJECT="$REMOTE_CELL_PROJECT_ID"
export RC_ADMIN_TOKEN_FILE="${REMOTE_CELL_ADMIN_TOKEN_FILE:-$REMOTE_CELL_TOKEN_FILE}"
export RC_ALICE_ENV="$DATA/invites/alice.env"
export RC_HTTP_CLIENT="$(cd "$(dirname "$0")" && pwd)/http_client.py"

log "prove: exclusive claim race (admin vs alice)"
python3 - <<'PY'
import os, sys, uuid
sys.path.insert(0, os.path.dirname(os.environ["RC_HTTP_CLIENT"]))
from http_client import terminal, issue_id_from, call

def load_env(path):
    env = {}
    for line in open(path):
        line=line.strip()
        if not line or line.startswith("#") or "=" not in line: continue
        k,v=line.split("=",1); env[k]=v
    return env

project = os.environ["RC_PROJECT"]
admin_url = os.environ["RC_ADMIN_URL"]
admin_token = open(os.environ["RC_ADMIN_TOKEN_FILE"]).read().strip()
alice = load_env(os.environ["RC_ALICE_ENV"])
alice_url = alice["BEADS_GATEWAY_URL"]
alice_token = alice["BEADS_TOKEN"]

# create via admin
key = "race-c-" + uuid.uuid4().hex[:12]
st, doc = terminal(admin_url, project, admin_token, "issue.create", {
    "title": f"[beads-perf-lab] claim race {key}",
    "type": "task",
    "priority": 2,
}, key=key)
assert st in (200,201,202) and doc.get("status")=="succeeded", doc
iid = issue_id_from(doc)
assert iid, doc
print("issue", iid)

# concurrent-ish claims: fire admin then alice quickly (true threads)
import concurrent.futures

def claim(url, token, who):
    st, doc = terminal(url, project, token, "issue.claim", {"id": iid}, key=f"claim-{who}-{uuid.uuid4().hex[:8]}")
    ok = st in (200,201,202) and doc.get("status")=="succeeded"
    return who, ok, doc.get("status"), doc.get("error_message") or doc.get("error_code") or ""

with concurrent.futures.ThreadPoolExecutor(max_workers=2) as ex:
    futs = [
        ex.submit(claim, admin_url, admin_token, "admin"),
        ex.submit(claim, alice_url, alice_token, "alice"),
    ]
    results = [f.result() for f in futs]

wins = [r for r in results if r[1]]
print("claim_results", results)
assert len(wins) == 1, f"expected exactly 1 claim winner, got {results}"
print("CLAIM_RACE_PASS winner=", wins[0][0])

# wrong token on admin URL should fail auth (alice token against admin gateway)
st, doc = call(admin_url, project, alice_token, "GET", f"/v1/projects/{project}/ping")
print("cross_token_ping", st, doc)
assert st in (401, 403), f"expected auth reject for foreign token, got {st} {doc}"
print("TOKEN_ISOLATION_PASS")
print("PROVE_PASS")
PY

log "prove passed"
