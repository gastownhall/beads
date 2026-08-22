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

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
        return
    fi
    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
        return
    fi
    echo "ERROR: no SHA-256 utility is available" >&2
    return 1
}

verify_release_archive() {
    local version="$1"
    local archive="$2"
    local expected actual
    expected=$(strict_release_sha256 "$version" "$OS" "$ARCH") || {
        echo "ERROR: no pinned release checksum for $version ($OS/$ARCH)" >&2
        return 1
    }
    actual=$(sha256_file "$archive") || return 1
    if [ "$actual" != "$expected" ]; then
        echo "ERROR: checksum mismatch for $version: got $actual, want $expected" >&2
        return 1
    fi
}

verify_release_binary_version() (
    local version="$1"
    local binary="$2"
    local bare="${version#v}"
    local output probe_root probe_timeout

    probe_timeout="${RELEASE_BINARY_VERSION_TIMEOUT:-15}"
    if ! [[ "$probe_timeout" =~ ^[1-9][0-9]*$ ]]; then
        echo "ERROR: RELEASE_BINARY_VERSION_TIMEOUT must be a positive number of seconds" >&2
        return 1
    fi
    if ! command -v timeout >/dev/null 2>&1; then
        echo "ERROR: timeout is required to verify historical release binaries safely" >&2
        return 1
    fi
    binary=$(realpath -e -- "$binary") || {
        echo "ERROR: verified $version binary cannot be resolved" >&2
        return 1
    }
    if [ ! -x "$binary" ]; then
        echo "ERROR: verified $version binary is not executable" >&2
        return 1
    fi
    probe_root=$(mktemp -d "${TMPDIR:-/tmp}/bd-release-version-probe.XXXXXX") || {
        echo "ERROR: could not create isolated release binary probe" >&2
        return 1
    }
    trap 'rm -rf -- "${probe_root:-}"' EXIT
    mkdir -p "$probe_root/run" "$probe_root/home" "$probe_root/xdg-config" \
        "$probe_root/xdg-cache" "$probe_root/xdg-data" "$probe_root/xdg-state" "$probe_root/tmp" || return 1

    if ! output=$(cd "$probe_root/run" && env -i \
        PATH=/usr/bin:/bin \
        HOME="$probe_root/home" \
        XDG_CONFIG_HOME="$probe_root/xdg-config" \
        XDG_CACHE_HOME="$probe_root/xdg-cache" \
        XDG_DATA_HOME="$probe_root/xdg-data" \
        XDG_STATE_HOME="$probe_root/xdg-state" \
        TMPDIR="$probe_root/tmp" \
        GIT_CONFIG_NOSYSTEM=1 \
        GIT_CONFIG_GLOBAL=/dev/null \
        GIT_CONFIG_SYSTEM=/dev/null \
        GIT_TERMINAL_PROMPT=0 \
        BD_DISABLE_METRICS=1 \
        BD_DISABLE_EVENT_FLUSH=1 \
        BD_NON_INTERACTIVE=1 \
        timeout --kill-after=5s "$probe_timeout" "$binary" version 2>&1); then
        echo "ERROR: verified $version binary does not run" >&2
        return 1
    fi
    if [[ " $output " != *" $bare "* ]]; then
        echo "ERROR: release binary reports unexpected version: $output" >&2
        return 1
    fi
)

download_verified_release_binary() {
    local version="$1"
    local asset expected release_dir archive binary
    asset=$(strict_release_asset "$version" "$OS" "$ARCH") || {
        echo "ERROR: no strict release manifest for $version ($OS/$ARCH)" >&2
        return 1
    }
    expected=$(strict_release_sha256 "$version" "$OS" "$ARCH") || return 1
    release_dir="$CACHE_DIR/verified/${version}-${OS}-${ARCH}-${expected}"
    archive="$release_dir/$asset"
    binary="$release_dir/bd"
    mkdir -p "$release_dir"

    if [ ! -f "$archive" ]; then
        local url tmp
        url="https://github.com/gastownhall/beads/releases/download/${version}/${asset}"
        tmp="$archive.tmp.$$"
        echo -e "  ${YELLOW:-}downloading verified ${version}...${NC:-}" >&2
        if ! curl -fsSL --max-time "$DOWNLOAD_TIMEOUT" --retry 3 --retry-all-errors --retry-delay 2 "$url" -o "$tmp"; then
            rm -f "$tmp"
            echo "ERROR: could not download pinned release asset for $version" >&2
            return 1
        fi
        if ! verify_release_archive "$version" "$tmp"; then
            rm -f "$tmp"
            return 1
        fi
        mv -f "$tmp" "$archive"
    fi
    verify_release_archive "$version" "$archive" || return 1

    local extract_dir extracted
    extract_dir=$(mktemp -d)
    if ! tar -xzf "$archive" -C "$extract_dir"; then
        rm -rf "$extract_dir"
        echo "ERROR: could not extract pinned release asset for $version" >&2
        return 1
    fi
    extracted=$(find "$extract_dir" -name bd -type f | head -1)
    if [ -z "$extracted" ]; then
        rm -rf "$extract_dir"
        echo "ERROR: pinned release asset for $version contains no bd binary" >&2
        return 1
    fi
    cp -f "$extracted" "$binary"
    chmod +x "$binary"
    rm -rf "$extract_dir"
    verify_release_binary_version "$version" "$binary" || return 1
    printf '%s\n' "$binary"
}

# v0.9.1 has no release asset. Its historical CLI reports 0.9.0 (dev), so
# qualification is the module checksum and VCS origin, never self-reporting.
build_verified_v091_source_binary() (
    local scratch module_json source_dir build_dir binary temporary toolchain_root toolchain_go toolchain
    scratch=$(mktemp -d) || return 1
    temporary=""
    trap 'rm -rf -- "$scratch"; [ -z "$temporary" ] || rm -f -- "$temporary"' EXIT
    toolchain_root=$(env GOTOOLCHAIN="$SOURCE_TAG_SQLITE_GO_TOOLCHAIN" go env GOROOT) || return 1
    toolchain_go=$(realpath -e -- "$toolchain_root/bin/go") || return 1
    [ -x "$toolchain_go" ] || return 1
    toolchain=$(cd "$scratch" && env GOTOOLCHAIN=local "$toolchain_go" env GOVERSION) || return 1
    [ "$toolchain" = "$SOURCE_TAG_SQLITE_GO_TOOLCHAIN" ] || {
        echo "ERROR: v0.9.1 source build requires $SOURCE_TAG_SQLITE_GO_TOOLCHAIN, got $toolchain" >&2
        return 1
    }
    module_json=$(cd "$scratch" && env GO111MODULE=on GOFLAGS=-modcacherw GOWORK=off \
        GOTOOLCHAIN=local GOMODCACHE="$scratch/mod" \
        GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE= \
        "$toolchain_go" mod download -json "$SOURCE_TAG_SQLITE_MODULE@$SOURCE_TAG_SQLITE_VERSION") || {
        echo 'ERROR: could not download v0.9.1 through sum.golang.org' >&2
        return 1
    }
    jq -e --arg path "$SOURCE_TAG_SQLITE_MODULE" --arg version "$SOURCE_TAG_SQLITE_VERSION" \
        --arg sum "$SOURCE_TAG_SQLITE_MODULE_SUM" --arg gomod_sum "$SOURCE_TAG_SQLITE_GOMOD_SUM" \
        --arg commit "$SOURCE_TAG_SQLITE_COMMIT" --arg ref "$SOURCE_TAG_SQLITE_REF" '
        .Path == $path and
        .Version == $version and
        .Sum == $sum and
        .GoModSum == $gomod_sum and
        .Origin.VCS == "git" and
        .Origin.Hash == $commit and
        .Origin.Ref == $ref and
        (.Dir | type == "string" and length > 0)
    ' <<< "$module_json" >/dev/null || {
        echo 'ERROR: v0.9.1 module provenance did not match the reviewed source tag' >&2
        return 1
    }
    source_dir=$(realpath -e -- "$(jq -r .Dir <<< "$module_json")") || return 1
    [ -d "$source_dir" ] && [ ! -L "$source_dir" ] &&
        [[ "$source_dir/" == "$scratch/mod/"* ]] || {
        echo 'ERROR: verified v0.9.1 module has no safe source directory' >&2
        return 1
    }
    build_dir="$CACHE_DIR/verified-source/${SOURCE_TAG_SQLITE_VERSION}-${SOURCE_TAG_SQLITE_COMMIT}-${SOURCE_TAG_SQLITE_GO_TOOLCHAIN}-linux-amd64-cgo1"
    binary="$build_dir/bd"
    mkdir -p "$build_dir"
    cp -f "$source_dir/go.mod" "$scratch/source.mod" &&
        cp -f "$source_dir/go.sum" "$scratch/source.sum" &&
        chmod u+w "$scratch/source.mod" "$scratch/source.sum" || return 1
    temporary="$binary.tmp.$$"
    if ! (cd "$source_dir" && env GOFLAGS=-modcacherw GOWORK=off GOTOOLCHAIN=local \
        GOMODCACHE="$scratch/mod" GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE= \
        GOOS=linux GOARCH=amd64 CGO_ENABLED=1 \
        "$toolchain_go" build -trimpath -modfile="$scratch/source.mod" -o "$temporary" ./cmd/bd); then
        echo 'ERROR: could not build verified v0.9.1 source for linux/amd64 with CGO' >&2
        return 1
    fi
    chmod +x "$temporary" && mv -f "$temporary" "$binary" || return 1
    temporary=""
    printf '%s\n' "$binary"
)

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
