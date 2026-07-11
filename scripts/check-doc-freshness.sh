#!/bin/bash
# check-doc-freshness.sh - Validate marker-based freshness for reference docs.
#
# Usage: ./scripts/check-doc-freshness.sh
#
# Environment:
#   DOC_FRESHNESS_MAX_AGE_DAYS  Maximum accepted age for Last reviewed markers.
#                               Defaults to 90 days.
#   DOC_FRESHNESS_TODAY         Override today's date for deterministic tests.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
MAX_AGE_DAYS="${DOC_FRESHNESS_MAX_AGE_DAYS:-90}"
TODAY="${DOC_FRESHNESS_TODAY:-$(date +%F)}"
ERRORS=0

DOCS=(
    "docs/reference/configuration.md|cmd/bd/main.go;cmd/bd/config.go;internal/configfile/"
    "docs/getting-started/ide-setup.md|cmd/bd/setup*.go;internal/recipes/"
    "docs/integrations/azure-devops.md|cmd/bd/ado*.go;internal/ado/"
    "docs/reference/json-schema.md|cmd/bd/output.go;cmd/bd/errors.go;cmd/bd/protocol/json_contract_test.go"
    "docs/recovery/init-safety.md|cmd/bd/init.go;cmd/bd/init_safety.go;cmd/bd/init_safety_test.go"
    "engdocs/ERROR_HANDLING.md|cmd/bd/*.go;cmd/bd/errors.go"
    "engdocs/LINTING.md|.golangci.yml"
    "engdocs/design/otel/otel-data-model.md|internal/telemetry/;internal/storage/dolt/store.go;internal/compact/haiku.go;cmd/bd/find_duplicates.go;internal/hooks/"
)

echo "Checking reference doc freshness markers..."
echo "Max age: ${MAX_AGE_DAYS} days"
echo "Today: ${TODAY}"
echo ""

date_age_days() {
    local reviewed="$1"
    python3 - "$reviewed" "$TODAY" <<'PY'
import datetime
import sys

reviewed = datetime.date.fromisoformat(sys.argv[1])
today = datetime.date.fromisoformat(sys.argv[2])
print((today - reviewed).days)
PY
}

inventory_ref_for_doc() {
    local doc="$1"
    doc="${doc#docs/}"
    doc="${doc#engdocs/}"
    printf '%s\n' "$doc"
}

path_exists_or_glob_matches() {
    local pattern="$1"

    if [[ "$pattern" == *"*"* || "$pattern" == *"?"* || "$pattern" == *"["* ]]; then
        compgen -G "$PROJECT_ROOT/$pattern" >/dev/null
    else
        [[ -e "$PROJECT_ROOT/$pattern" ]]
    fi
}

for entry in "${DOCS[@]}"; do
    IFS='|' read -r doc sources <<<"$entry"
    doc_path="$PROJECT_ROOT/$doc"
    inventory_ref="$(inventory_ref_for_doc "$doc")"

    echo "=== $doc ==="

    if [[ ! -f "$doc_path" ]]; then
        echo "FAIL: missing document"
        ERRORS=$((ERRORS + 1))
        echo ""
        continue
    fi

    if ! grep -Fq "\`$inventory_ref\`" "$PROJECT_ROOT/engdocs/DOC_INVENTORY.md"; then
        echo "FAIL: engdocs/DOC_INVENTORY.md does not list \`$inventory_ref\`"
        ERRORS=$((ERRORS + 1))
    else
        echo "PASS: listed in engdocs/DOC_INVENTORY.md"
    fi

    reviewed_line="$(grep -E -m1 '^Last reviewed: [0-9]{4}-[0-9]{2}-[0-9]{2}$' "$doc_path" || true)"
    if [[ -z "$reviewed_line" ]]; then
        echo "FAIL: missing Last reviewed marker in YYYY-MM-DD format"
        ERRORS=$((ERRORS + 1))
    else
        reviewed="${reviewed_line#Last reviewed: }"
        if ! age_days="$(date_age_days "$reviewed" 2>/dev/null)"; then
            echo "FAIL: invalid Last reviewed date: $reviewed"
            ERRORS=$((ERRORS + 1))
        elif (( age_days < 0 )); then
            echo "FAIL: Last reviewed date is in the future: $reviewed"
            ERRORS=$((ERRORS + 1))
        elif (( age_days > MAX_AGE_DAYS )); then
            echo "FAIL: Last reviewed date is stale: $reviewed (${age_days} days old)"
            ERRORS=$((ERRORS + 1))
        else
            echo "PASS: Last reviewed marker is current: $reviewed (${age_days} days old)"
        fi
    fi

    if ! grep -Eq '^Freshness source:' "$doc_path"; then
        echo "FAIL: missing Freshness source marker"
        ERRORS=$((ERRORS + 1))
    else
        echo "PASS: Freshness source marker present"
    fi

    IFS=';' read -ra source_list <<<"$sources"
    for source in "${source_list[@]}"; do
        if ! grep -Fq "$source" "$doc_path"; then
            echo "FAIL: Freshness source does not name $source"
            ERRORS=$((ERRORS + 1))
        elif ! path_exists_or_glob_matches "$source"; then
            echo "FAIL: Freshness source path does not exist: $source"
            ERRORS=$((ERRORS + 1))
        else
            echo "PASS: Freshness source path exists: $source"
        fi
    done

    echo ""
done

echo "=== Summary ==="
if (( ERRORS > 0 )); then
    echo "FAILED: $ERRORS reference doc freshness issue(s) found"
    echo ""
    echo "To fix:"
    echo "  1. Re-review the failing doc against its Freshness source paths."
    echo "  2. Update the doc's Last reviewed marker and Freshness source list."
    echo "  3. Re-run: ./scripts/check-doc-freshness.sh"
    exit 1
fi

echo "PASSED: Reference doc freshness markers are current and checkable"
