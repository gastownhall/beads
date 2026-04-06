#!/bin/bash
# Binary management — download old releases, build candidate.
# Extracted from cross-version-smoke-test.sh for reuse.

CACHE_DIR="${HOME}/.cache/beads-regression"
mkdir -p "$CACHE_DIR"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
esac

download_binary() {
    local version="$1"
    local ver_bare="${version#v}"
    local cached="$CACHE_DIR/bd-${ver_bare}"

    if [ -x "$cached" ]; then
        echo "$cached"
        return
    fi

    local asset="beads_${ver_bare}_${OS}_${ARCH}.tar.gz"
    local url="https://github.com/steveyegge/beads/releases/download/${version}/${asset}"

    echo -e "  ${YELLOW:-}downloading ${version}...${NC:-}" >&2
    local tmpdir
    tmpdir=$(mktemp -d)
    if ! curl -fsSL "$url" -o "$tmpdir/archive.tar.gz" 2>/dev/null; then
        rm -rf "$tmpdir"
        return 1
    fi

    tar -xzf "$tmpdir/archive.tar.gz" -C "$tmpdir"
    local bd_path
    bd_path=$(find "$tmpdir" -name bd -type f | head -1)
    if [ -z "$bd_path" ]; then
        rm -rf "$tmpdir"
        return 1
    fi

    cp -f "$bd_path" "$cached"
    chmod +x "$cached"
    rm -rf "$tmpdir"
    echo "$cached"
}

build_candidate() {
    if [ -n "${CANDIDATE_BIN:-}" ] && [ -x "${CANDIDATE_BIN}" ]; then
        echo "$(cd "$(dirname "$CANDIDATE_BIN")" && pwd)/$(basename "$CANDIDATE_BIN")"
        return
    fi

    local candidate="$CACHE_DIR/bd-candidate-$$"
    echo -e "${YELLOW:-}Building candidate binary...${NC:-}" >&2
    (cd "$PROJECT_ROOT" && go build -o "$candidate" ./cmd/bd) >&2
    echo "$candidate"
}
