#!/usr/bin/env bash
# migrate-sqlite-to-current.sh — Upgrade a SQLite-era bd project to current embedded Dolt
#
# Usage:
#   ./scripts/migrate-sqlite-to-current.sh [path/to/project]
#
# If no path is given, uses the current directory.
#
# What it does:
#   1. Finds .beads/beads.db (the SQLite-era database)
#   2. Exports issues to JSONL format
#   3. Normalizes SQLite type quirks (integer booleans, etc.)
#   4. Runs bd init --from-jsonl to create the new embedded Dolt database
#   5. Attempts to restore dependencies and labels from SQLite
#
# Requirements: sqlite3, jq, bd (current version)

set -euo pipefail

PROJECT_DIR="${1:-.}"
BEADS_DIR="$PROJECT_DIR/.beads"
DB_PATH="$BEADS_DIR/beads.db"

# --- Preflight checks ---

if [ ! -f "$DB_PATH" ]; then
    echo "Error: No SQLite database found at $DB_PATH"
    echo "This script is for upgrading SQLite-era (v0.30–v0.50) bd projects."
    exit 1
fi

for cmd in sqlite3 jq bd; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Error: $cmd is required but not installed."
        exit 1
    fi
done

echo "Found SQLite database: $DB_PATH"

# --- Step 1: Export issues ---

echo "Exporting issues..."
sqlite3 "$DB_PATH" ".mode json" "SELECT * FROM issues;" | \
    jq -c '.[]' > "$BEADS_DIR/issues.jsonl"

count=$(wc -l < "$BEADS_DIR/issues.jsonl")
echo "  Exported $count issues"

# --- Step 2: Normalize SQLite types ---
# SQLite has no native bool/array/JSON types. Exported data contains:
#   - Integer booleans: "ephemeral":0 instead of "ephemeral":false
#   - String arrays: "waiters":"" instead of "waiters":[]
#   - String JSON: "metadata":"" instead of "metadata":{}

echo "Normalizing SQLite types..."
jq -c '
walk(if type == "object" then
    with_entries(
        if (.key | test("^(ephemeral|pinned|is_template|crystallizes|no_history)$")) and (.value | type == "number")
        then .value = (.value != 0)
        elif (.key | test("^(waiters)$")) and (.value | type == "string")
        then .value = (if .value == "" then [] else (.value | split(",") | map(select(. != ""))) end)
        elif (.key | test("^(metadata)$")) and (.value | type == "string")
        then .value = (if .value == "" then {} else (.value | fromjson? // {}) end)
        else . end
    )
else . end)
' "$BEADS_DIR/issues.jsonl" > "$BEADS_DIR/issues.jsonl.tmp" && \
    mv "$BEADS_DIR/issues.jsonl.tmp" "$BEADS_DIR/issues.jsonl"

# --- Step 3: Extract dependencies and labels (for replay after import) ---

deps_json=$(sqlite3 "$DB_PATH" ".mode json" "SELECT * FROM dependencies;" 2>/dev/null) || deps_json="[]"
labels_json=$(sqlite3 "$DB_PATH" ".mode json" "SELECT * FROM labels;" 2>/dev/null) || labels_json="[]"

# --- Step 4: Move old artifacts aside ---

echo "Moving old SQLite files aside..."
for f in "$BEADS_DIR/beads.db" "$BEADS_DIR/beads.db-wal" "$BEADS_DIR/beads.db-shm" \
         "$BEADS_DIR/metadata.json" "$BEADS_DIR/config.json" "$BEADS_DIR/config.yaml"; do
    [ -f "$f" ] && mv "$f" "${f}.pre-migration" 2>/dev/null || true
done
for d in "$BEADS_DIR/embeddeddolt" "$BEADS_DIR/dolt"; do
    [ -d "$d" ] && mv "$d" "${d}.pre-migration" 2>/dev/null || true
done

# --- Step 5: Import ---

echo "Initializing new database..."
(cd "$PROJECT_DIR" && bd init --from-jsonl --quiet --non-interactive)
echo "  Import complete"

# --- Step 6: Replay dependencies and labels ---

dep_count=0
if [ "$deps_json" != "[]" ] && [ -n "$deps_json" ]; then
    echo "Restoring dependencies..."
    echo "$deps_json" | jq -r '.[] | "\(.issue_id) \(.depends_on_id)"' 2>/dev/null | \
    while read -r issue_id depends_on_id; do
        if (cd "$PROJECT_DIR" && bd dep add "$issue_id" "$depends_on_id" 2>/dev/null); then
            dep_count=$((dep_count + 1))
        fi
    done
    echo "  Restored dependencies"
fi

label_count=0
if [ "$labels_json" != "[]" ] && [ -n "$labels_json" ]; then
    echo "Restoring labels..."
    echo "$labels_json" | jq -r '.[] | "\(.issue_id) \(.label)"' 2>/dev/null | \
    while read -r issue_id label; do
        if (cd "$PROJECT_DIR" && bd label add "$issue_id" "$label" 2>/dev/null); then
            label_count=$((label_count + 1))
        fi
    done
    echo "  Restored labels"
fi

# --- Done ---

echo ""
echo "Migration complete. Verify with: bd list --all"
echo ""
echo "Old SQLite files preserved as .pre-migration in $BEADS_DIR"
echo "Once satisfied, you can remove them:"
echo "  rm $BEADS_DIR/*.pre-migration"
