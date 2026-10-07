#!/usr/bin/env bash
# MCP Python package gate.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MCP_DIR="$REPO_ROOT/integrations/beads-mcp"

# shellcheck source=../../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"

tmpdir=""
cleanup() {
    if [[ -n "$tmpdir" ]]; then
        rm -rf "$tmpdir"
    fi
}
trap cleanup EXIT

prepare_bd_binary() {
    tmpdir="$(mktemp -d)"
    local target="$tmpdir/bd"

    if [[ -n "${BEADS_TEST_BD_BINARY:-}" ]]; then
        cp "$BEADS_TEST_BD_BINARY" "$target"
    else
        go build -o "$target" ./cmd/bd
    fi

    chmod +x "$target"
    export PATH="$tmpdir:$PATH"
    bd version
}

mcp_uv_sync() {
    cd "$MCP_DIR"
    uv sync --all-groups --locked
}

mcp_ruff() {
    cd "$MCP_DIR"
    uv run ruff check src/beads_mcp tests
}

mcp_mypy() {
    cd "$MCP_DIR"
    uv run mypy src/beads_mcp
}

mcp_pytest() {
    cd "$MCP_DIR"
    # Explicit worker count, not "auto": CI runners' reported CPU count
    # doesn't always match the cgroup quota, and -n 8 is pinned to measured
    # timing (tools/f3, 2026-10) on both 2 vCPU (2.7x) and 4 vCPU (~3x)
    # runners. pytest-xdist is a pinned dev dependency (pyproject.toml).
    uv run pytest -n 8 --durations=50
}

mcp_build() {
    cd "$MCP_DIR"
    rm -rf dist
    uv build
}

cd "$REPO_ROOT"

ci_time "prepare bd for MCP package" -- prepare_bd_binary
ci_time "mcp uv sync" -- mcp_uv_sync
ci_time "mcp ruff check" -- mcp_ruff
ci_time "mcp mypy" -- mcp_mypy
ci_time "mcp pytest" -- mcp_pytest
ci_time "mcp build" -- mcp_build
