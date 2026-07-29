#!/bin/bash
# Binary management — download old releases, build candidate.
# Extracted from cross-version-smoke-test.sh for reuse.

CACHE_DIR="${HOME}/.cache/beads-regression"
mkdir -p "$CACHE_DIR"

DOWNLOAD_TIMEOUT="${DOWNLOAD_TIMEOUT:-60}"

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
        # Verify the cached binary actually runs (shared lib deps satisfied)
        if "$cached" version >/dev/null 2>&1; then
            echo "$cached"
            return
        fi
        echo -e "  ${YELLOW:-}cached binary broken (missing libs?), rebuilding...${NC:-}" >&2
        rm -f "$cached"
    fi

    # Try downloading the release binary first
    local asset="beads_${ver_bare}_${OS}_${ARCH}.tar.gz"
    local url="https://github.com/gastownhall/beads/releases/download/${version}/${asset}"

    echo -e "  ${YELLOW:-}downloading ${version}...${NC:-}" >&2
    local tmpdir
    tmpdir=$(mktemp -d)
    if curl -fsSL --max-time "$DOWNLOAD_TIMEOUT" "$url" -o "$tmpdir/archive.tar.gz" 2>/dev/null; then
        tar -xzf "$tmpdir/archive.tar.gz" -C "$tmpdir"
        local bd_path
        bd_path=$(find "$tmpdir" -name bd -type f | head -1)
        if [ -n "$bd_path" ]; then
            cp -f "$bd_path" "$cached"
            chmod +x "$cached"
            rm -rf "$tmpdir"
            # Verify it actually runs
            if "$cached" version >/dev/null 2>&1; then
                echo "$cached"
                return
            fi
            echo -e "  ${YELLOW:-}downloaded binary unusable (missing shared libs), building from source...${NC:-}" >&2
            rm -f "$cached"
        else
            rm -rf "$tmpdir"
        fi
    else
        rm -rf "$tmpdir"
    fi

    # Fallback: build from source at the given tag
    build_from_source "$version" "$cached"
}

# Build a specific version from source by checking out its tag in a temp dir.
build_from_source() {
    local version="$1"
    local output="$2"

    echo -e "  ${YELLOW:-}building ${version} from source...${NC:-}" >&2
    local srcdir
    srcdir=$(mktemp -d)
    if ! git clone --depth 1 --branch "$version" "$PROJECT_ROOT" "$srcdir" 2>/dev/null; then
        # Tag might not exist locally; try the remote
        if ! git clone --depth 1 --branch "$version" \
            "https://github.com/gastownhall/beads.git" "$srcdir" 2>/dev/null; then
            rm -rf "$srcdir"
            echo -e "  ${RED:-}ERROR: cannot clone tag ${version}${NC:-}" >&2
            return 1
        fi
    fi

    # Use gms_pure_go to avoid ICU header dependency; fall back to plain build
    if ! (cd "$srcdir" && go build -tags gms_pure_go -o "$output" ./cmd/bd) 2>&1 | tail -5 >&2; then
        rm -rf "$srcdir"
        return 1
    fi

    chmod +x "$output"
    rm -rf "$srcdir"
    echo "$output"
}

build_candidate() {
    if [ -n "${CANDIDATE_BIN:-}" ] && [ -x "${CANDIDATE_BIN}" ]; then
        echo "$(cd "$(dirname "$CANDIDATE_BIN")" && pwd)/$(basename "$CANDIDATE_BIN")"
        return
    fi

    local candidate="$CACHE_DIR/bd-candidate-$$"
    echo -e "${YELLOW:-}Building candidate binary...${NC:-}" >&2
    (cd "$PROJECT_ROOT" && go build -tags gms_pure_go -o "$candidate" ./cmd/bd) >&2
    echo "$candidate"
}
