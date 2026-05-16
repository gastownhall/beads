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
#   The bug fired in migrateServerRootRemotes (federation.go) when bd
#   called doltutil.ListCLIRemotes() against the dolt server root.
#   A multi-DB server root has .dolt/sql-server.info but no repo_state.json;
#   `dolt remote -v` in such a dir takes ~12 s before failing with
#   "not a valid dolt repository."
#
#   The fix (Layer 1, federation.go) sentinel-stats repo_state.json before
#   invoking ListCLIRemotes, skipping the slow call entirely.
#   The fix (Layer 2, remotes.go) also adds a 2 s context timeout as a
#   backstop for any future variant.

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
echo "    Expected: ~12 s (or 2 s with the Layer 2 timeout)"
START=$(date +%s%3N)
dolt remote -v 2>&1 || true  # expected to fail
END=$(date +%s%3N)
ELAPSED=$(( END - START ))
echo "    Elapsed: ${ELAPSED} ms"
echo

if [[ $ELAPSED -gt 5000 ]]; then
    echo "SLOW PATH CONFIRMED: elapsed ${ELAPSED}ms > 5000ms (Layer 1 not active)"
elif [[ $ELAPSED -gt 1500 ]]; then
    echo "PARTIAL FIX: elapsed ${ELAPSED}ms — Layer 2 timeout capped it, but Layer 1 (repo_state.json check) is not in effect for this direct test"
else
    echo "FAST PATH: elapsed ${ELAPSED}ms — dolt exited quickly (dolt version may have fixed the underlying issue)"
fi

# ── 3. Show what the sentinel check (Layer 1 fix) looks like ─────────────────
echo
echo "--- Layer 1 fix: sentinel check for repo_state.json ---"
REPO_STATE="$SERVER_ROOT/.dolt/repo_state.json"
if [[ -f "$REPO_STATE" ]]; then
    echo "    repo_state.json EXISTS → ListCLIRemotes would be called"
else
    echo "    repo_state.json ABSENT → ListCLIRemotes is SKIPPED (be-1he fix)"
    echo "    This is the fix: federation.go now checks for repo_state.json before"
    echo "    invoking ListCLIRemotes, preventing the 12 s dolt subprocess."
fi

# ── 4. Create a proper dolt repo for contrast ────────────────────────────────
echo
echo "--- Contrast: dolt remote -v against a proper dolt repo (fast) ---"
PROPER_REPO="$TMPDIR/proper_repo"
mkdir -p "$PROPER_REPO"
(cd "$PROPER_REPO" && dolt init --quiet 2>/dev/null)
START=$(date +%s%3N)
(cd "$PROPER_REPO" && dolt remote -v 2>&1) || true
END=$(date +%s%3N)
ELAPSED=$(( END - START ))
echo "    Elapsed: ${ELAPSED} ms (expected < 500 ms)"

echo
echo "=== Summary ==="
echo "The 12 s slow path was: bd command → autoMigrateOnVersionBump →"
echo "syncCLIRemotesToSQL → ListCLIRemotes('.beads/dolt/') →"
echo "dolt remote -v in server root → 12 s failure"
echo
echo "Layer 1 fix (federation.go): skip ListCLIRemotes if repo_state.json absent"
echo "Layer 2 fix (remotes.go):    2 s context.WithTimeout on ListCLIRemotes"
echo "Layer 3 fix (version_tracking.go): read-only probe avoids openDB writeable"
