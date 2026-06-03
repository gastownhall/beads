#!/usr/bin/env bash
# Mirror selected root documentation into the Docusaurus website tree.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

write_doc() {
    local source="$1"
    local target="$2"
    local id="$3"
    local title="$4"
    local slug="$5"

    if [[ ! -f "$source" ]]; then
        echo "missing source doc: $source" >&2
        exit 1
    fi

    mkdir -p "$(dirname "$target")"
    {
        cat <<EOF
---
id: $id
title: $title
slug: $slug
---

EOF
        rewrite_links "$source"
    } > "$target"
}

rewrite_links() {
    local source="$1"

    case "${source#$REPO_ROOT/}" in
        docs/LABELS.md)
            sed \
                -e 's@](CLI_REFERENCE.md#state-labels-as-cache)@](/cli-reference/set-state)@g' \
                -e 's#](../README.md#](https://github.com/gastownhall/beads/blob/main/README.md#g' \
                -e 's#](../AGENTS.md#](https://github.com/gastownhall/beads/blob/main/AGENTS.md#g' \
                -e 's#](ADVANCED.md#](/reference/advanced#g' \
                "$source"
            ;;
        docs/METADATA.md)
            sed \
                -e 's#](PROJECT_CHARTER.md#](https://github.com/gastownhall/beads/blob/main/docs/PROJECT_CHARTER.md#g' \
                "$source"
            ;;
        *)
            cat "$source"
            ;;
    esac
}

doc_specs=(
    "docs/SYNC_CONCEPTS.md|website/docs/core-concepts/sync-concepts.md|sync-concepts|Sync Concepts|/core-concepts/sync-concepts"
    "docs/LABELS.md|website/docs/core-concepts/labels.md|labels|Labels|/core-concepts/labels"
    "docs/METADATA.md|website/docs/core-concepts/metadata.md|metadata|Issue Metadata|/core-concepts/metadata"
    "docs/UNINSTALLING.md|website/docs/recovery/uninstalling.md|uninstalling|Uninstalling|/recovery/uninstalling"
    "docs/ANTIVIRUS.md|website/docs/reference/antivirus.md|antivirus|Antivirus False Positives|/reference/antivirus"
    "docs/COMMUNITY_TOOLS.md|website/docs/community-tools.md|community-tools|Community Tools|/community-tools"
)

for spec in "${doc_specs[@]}"; do
    IFS='|' read -r source target id title slug <<<"$spec"
    write_doc \
        "$REPO_ROOT/$source" \
        "$REPO_ROOT/$target" \
        "$id" \
        "$title" \
        "$slug"
done

latest_version="$(grep -oE '"[0-9][^"]*"' "$REPO_ROOT/website/versions.json" 2>/dev/null | head -1 | tr -d '"' || true)"
if [[ -n "$latest_version" && -d "$REPO_ROOT/website/versioned_docs/version-$latest_version" ]]; then
    for spec in "${doc_specs[@]}"; do
        IFS='|' read -r source target id title slug <<<"$spec"
        versioned_target="${target#website/docs/}"
        write_doc \
            "$REPO_ROOT/$source" \
            "$REPO_ROOT/website/versioned_docs/version-$latest_version/$versioned_target" \
            "$id" \
            "$title" \
            "$slug"
    done

    sidebar="$REPO_ROOT/website/versioned_sidebars/version-$latest_version-sidebars.json"
    if [[ -f "$sidebar" ]]; then
        node - "$sidebar" <<'JS'
const fs = require('fs');

const sidebarPath = process.argv[2];
const data = JSON.parse(fs.readFileSync(sidebarPath, 'utf8'));
const items = data.docsSidebar;

function category(label) {
  return items.find(item => item && typeof item === 'object' && item.label === label);
}

function insertAfter(list, after, id) {
  if (list.includes(id)) return;
  const index = list.indexOf(after);
  list.splice(index === -1 ? list.length : index + 1, 0, id);
}

insertAfter(category('Core Concepts').items, 'core-concepts/hash-ids', 'core-concepts/sync-concepts');
insertAfter(category('Core Concepts').items, 'core-concepts/sync-concepts', 'core-concepts/labels');
insertAfter(category('Core Concepts').items, 'core-concepts/labels', 'core-concepts/metadata');
insertAfter(category('Recovery').items, 'recovery/sync-failures', 'recovery/uninstalling');
insertAfter(category('Reference').items, 'reference/troubleshooting', 'reference/antivirus');

if (!items.includes('community-tools')) {
  const referenceIndex = items.findIndex(
    item => item && typeof item === 'object' && item.label === 'Reference'
  );
  const insertAt = referenceIndex === -1 ? items.length : referenceIndex;
  items.splice(insertAt, 0, 'community-tools');
}

fs.writeFileSync(sidebarPath, `${JSON.stringify(data, null, 2)}\n`);
JS
    fi
fi
