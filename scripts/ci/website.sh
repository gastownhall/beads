#!/usr/bin/env bash
# Website package gate.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WEBSITE_DIR="$REPO_ROOT/website"

# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"

website_npm_ci() {
    cd "$WEBSITE_DIR"
    npm ci
}

website_typecheck() {
    cd "$WEBSITE_DIR"
    npm run typecheck
}

generate_llms_full() {
    cd "$REPO_ROOT"
    ./scripts/generate-llms-full.sh
}

sync_website_docs() {
    cd "$REPO_ROOT"
    ./scripts/sync-website-docs.sh
}

website_build() {
    cd "$WEBSITE_DIR"
    npm run build
}

ci_time "website npm ci" -- website_npm_ci
ci_time "sync website docs" -- sync_website_docs
ci_time "website typecheck" -- website_typecheck
ci_time "generate llms-full" -- generate_llms_full
ci_time "website build" -- website_build
