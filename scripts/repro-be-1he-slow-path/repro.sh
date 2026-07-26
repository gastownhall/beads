#!/usr/bin/env bash
# Repro for be-1he: `dolt remote -v` against a multi-DB server root
# takes ~12 s when repo_state.json is absent.
#
# Usage:
#   ./scripts/repro-be-1he-slow-path/repro.sh [--no-cleanup]
#
# Requirements:
#   - dolt in PATH
#   - time command (bash built-in)
#
# What this demonstrates:
#   A multi-DB server root has .dolt/sql-server.info but no repo_state.json;
#   `dolt remote -v` in such a dir takes ~12 s before failing with
#   "not a valid dolt repository." That's raw upstream dolt CLI behavior,
#   reproduced here directly, unmediated by bd.
#
#   What be-1he ships against that slowness:
#     - Layer 2 (internal/storage/doltutil/remotes.go): ListCLIRemotes caps
#       its own `dolt remote -v` subprocess at 2 s when the target directory
#       lacks repo_state.json (this script's SERVER_ROOT case) and at a
#       generous 30 s otherwise, so a slow-but-valid remote list from a real
#       repo is never mistaken for "remote absent". This script calls the
#       raw `dolt` binary directly, not bd's ListCLIRemotes, so the timings
#       below are never capped — they demonstrate the underlying dolt
#       slowness the cap protects against.
#     - Layer 3 (cmd/bd/version_tracking.go): a read-only bd_version probe
#       before autoMigrateOnVersionBump opens the store writeable, saving an
#       unnecessary initSchema round-trip when no migration is needed.
#   (An earlier draft of this fix also described a "Layer 1" sentinel in
#   internal/storage/dolt/federation.go; that never shipped in be-1he and is
#   not part of this repro.)

set -euo pipefail

TMPDIR=$(mktemp -d)
cleanup() { rm -rf "$TMPDIR"; }
if [[ "${1:-}" != "--no-cleanup" ]]; then
    trap cleanup EXIT
fi

echo "=== be-1he repro: dolt remote -v against multi-DB server root ==="
echo "Temp dir: $TMPDIR"
echo

# ── 1. Create a multi-DB server root (the broken structure) ──────────────────
SERVER_ROOT="$TMPDIR/server_root"
mkdir -p "$SERVER_ROOT/.dolt"
# sql-server.info is what a running dolt sql-server writes.
# A server root has this file but NOT repo_state.json.
cat > "$SERVER_ROOT/.dolt/sql-server.info" <<'EOF'
[{"host":"127.0.0.1","port":3307,"unix_socket":"","database":""}]
EOF

echo "--- Server root structure ---"
find "$SERVER_ROOT" -type f
echo

# ── 2. Baseline: dolt remote -v against the broken server root (slow) ────────
echo "--- Timing 'dolt remote -v' against broken server root (no repo_state.json) ---"
echo "    Expected: ~12 s. This runs the raw dolt binary directly inside SERVER_ROOT,"
echo "    not through bd's timeout-wrapped ListCLIRemotes, so nothing here caps it."
START=$(date +%s%3N)
(cd "$SERVER_ROOT" && dolt remote -v 2>&1) || true  # expected to fail
END=$(date +%s%3N)
ELAPSED=$(( END - START ))
echo "    Elapsed: ${ELAPSED} ms"
echo

if [[ $ELAPSED -gt 5000 ]]; then
    echo "SLOW PATH CONFIRMED: elapsed ${ELAPSED}ms > 5000ms — matches the ~12s upstream"
    echo "dolt CLI failure mode this repro targets. Raw dolt call, uncapped by bd."
elif [[ $ELAPSED -gt 1500 ]]; then
    echo "SLOW PATH PARTIALLY OBSERVED: elapsed ${ELAPSED}ms — clearly slower than a"
    echo "healthy repo's ~130ms, just short of the full ~12s this time. Still a raw,"
    echo "uncapped dolt call: bd's own 2s cap (Layer 2) is not exercised by this script."
else
    echo "FAST PATH: elapsed ${ELAPSED}ms — dolt exited quickly (this dolt version may"
    echo "have fixed the underlying issue upstream)"
fi

# ── 3. Show how bd's Layer 2 cap is scoped ────────────────────────────────────
echo
echo "--- Layer 2 cap scoping: repo_state.json presence decides bd's timeout ---"
REPO_STATE="$SERVER_ROOT/.dolt/repo_state.json"
if [[ -f "$REPO_STATE" ]]; then
    echo "    repo_state.json EXISTS → bd's ListCLIRemotes uses the generous 30s cap"
    echo "    (this looks like a real repo; a slow-but-valid answer must not be"
    echo "    mistaken for 'remote absent' by callers like FindCLIRemote)."
else
    echo "    repo_state.json ABSENT → bd's ListCLIRemotes uses the aggressive 2s cap"
    echo "    (this is the broken multi-DB server-root case be-1he targets; there is"
    echo "    no real answer coming from here, so failing fast is safe)."
fi

# ── 4. Create a proper dolt repo for contrast ────────────────────────────────
echo
echo "--- Contrast: dolt remote -v against a proper dolt repo (fast) ---"
PROPER_REPO="$TMPDIR/proper_repo"
mkdir -p "$PROPER_REPO"
(cd "$PROPER_REPO" && dolt init >/dev/null 2>&1)
START=$(date +%s%3N)
(cd "$PROPER_REPO" && dolt remote -v 2>&1) || true
END=$(date +%s%3N)
ELAPSED=$(( END - START ))
echo "    Elapsed: ${ELAPSED} ms (expected < 500 ms)"

echo
echo "=== Summary ==="
echo "The historical 12 s slow path (pre-be-1he, and before an unrelated main-branch"
echo "rework of remote migration around doltutil.PersistedRemotes) was:"
echo "  bd command -> autoMigrateOnVersionBump -> writeable store open ->"
echo "  ListCLIRemotes('.beads/dolt/') -> dolt remote -v in server root -> 12 s failure"
echo
echo "What be-1he ships against the remaining exposure:"
echo "Layer 2 fix (remotes.go):          2 s cap on ListCLIRemotes when repo_state.json"
echo "                                    is absent, 30 s cap otherwise"
echo "Layer 3 fix (version_tracking.go): read-only bd_version probe avoids an"
echo "                                    unnecessary writeable store open"
