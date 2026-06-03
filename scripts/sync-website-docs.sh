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
        cat "$source"
    } > "$target"
}

write_doc \
    "$REPO_ROOT/docs/COMMUNITY_TOOLS.md" \
    "$REPO_ROOT/website/docs/community-tools.md" \
    "community-tools" \
    "Community Tools" \
    "/community-tools"

latest_version="$(grep -oE '"[0-9][^"]*"' "$REPO_ROOT/website/versions.json" 2>/dev/null | head -1 | tr -d '"' || true)"
if [[ -n "$latest_version" && -d "$REPO_ROOT/website/versioned_docs/version-$latest_version" ]]; then
    write_doc \
        "$REPO_ROOT/docs/COMMUNITY_TOOLS.md" \
        "$REPO_ROOT/website/versioned_docs/version-$latest_version/community-tools.md" \
        "community-tools" \
        "Community Tools" \
        "/community-tools"

    sidebar="$REPO_ROOT/website/versioned_sidebars/version-$latest_version-sidebars.json"
    if [[ -f "$sidebar" ]]; then
        node - "$sidebar" <<'JS'
const fs = require('fs');

const sidebarPath = process.argv[2];
const data = JSON.parse(fs.readFileSync(sidebarPath, 'utf8'));
const items = data.docsSidebar;

if (!items.includes('community-tools')) {
  const referenceIndex = items.findIndex(
    item => item && typeof item === 'object' && item.label === 'Reference'
  );
  const insertAt = referenceIndex === -1 ? items.length : referenceIndex;
  items.splice(insertAt, 0, 'community-tools');
  fs.writeFileSync(sidebarPath, `${JSON.stringify(data, null, 2)}\n`);
}
JS
    fi
fi
