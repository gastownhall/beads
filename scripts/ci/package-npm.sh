#!/usr/bin/env bash
# npm package gate.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NPM_DIR="$REPO_ROOT/npm-package"
NPM_BIN="$NPM_DIR/bin/bd"

# shellcheck source=../../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"

backup_bin=""
created_bin=0

cleanup() {
    if [[ "$created_bin" -eq 1 ]]; then
        rm -f "$NPM_BIN"
    fi
    if [[ -n "$backup_bin" && -f "$backup_bin" ]]; then
        mv -f "$backup_bin" "$NPM_BIN"
    fi
}
trap cleanup EXIT

prepare_bd_binary() {
    mkdir -p "$NPM_DIR/bin"

    if [[ -e "$NPM_BIN" ]]; then
        backup_bin="$(mktemp)"
        mv -f "$NPM_BIN" "$backup_bin"
    fi

    if [[ -n "${BEADS_TEST_BD_BINARY:-}" ]]; then
        cp "$BEADS_TEST_BD_BINARY" "$NPM_BIN"
    else
        go build -o "$NPM_BIN" ./cmd/bd
    fi

    chmod +x "$NPM_BIN"
    created_bin=1
    "$NPM_BIN" version
}

npm_install() {
    cd "$NPM_DIR"
    npm install
}

npm_test_all() {
    cd "$NPM_DIR"
    npm run test:all
}

npm_pack_dry_run() {
    cd "$NPM_DIR"
    npm pack --dry-run
}

cd "$REPO_ROOT"

# The package postinstall script downloads the latest published release when
# CI is not set. Package gates validate the candidate binary prepared above.
export CI="${CI:-1}"

ci_time "prepare bd for npm package" -- prepare_bd_binary
ci_time "npm package install" -- npm_install
ci_time "npm package test:all" -- npm_test_all
ci_time "npm package pack dry-run" -- npm_pack_dry_run
