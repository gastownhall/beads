#!/bin/bash
# generate-cli-docs.sh — Generate CLI reference docs from the live bd command tree.

set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

CHECK_MODE=0
VERSIONED_VERSION=""
BD_ARG=""
TMP_BUILD_DIR=""
TMP_OUTPUT_DIR=""

while [ "$#" -gt 0 ]; do
    case "$1" in
        --check)
            CHECK_MODE=1
            shift
            ;;
        --versioned)
            [ "$#" -ge 2 ] || { echo "Error: --versioned needs a version" >&2; exit 2; }
            VERSIONED_VERSION="$2"
            shift 2
            ;;
        -h|--help)
            cat <<'EOF'
Usage: scripts/generate-cli-docs.sh [--check] [--versioned VERSION] [path-to-bd]

Generate live CLI docs from one bd process. Historical Docusaurus snapshots are
left untouched unless --versioned VERSION is supplied by the release snapshot
workflow.
EOF
            exit 0
            ;;
        *)
            if [ -n "$BD_ARG" ]; then
                echo "Error: multiple bd binaries supplied: $BD_ARG and $1" >&2
                exit 2
            fi
            BD_ARG="$1"
            shift
            ;;
    esac
done

cleanup() {
    if [ -n "$TMP_BUILD_DIR" ]; then
        rm -rf "$TMP_BUILD_DIR"
    fi
    if [ -n "$TMP_OUTPUT_DIR" ]; then
        rm -rf "$TMP_OUTPUT_DIR"
    fi
}
trap cleanup EXIT

if [ -n "$BD_ARG" ]; then
    BD="$BD_ARG"
elif [ -x "$PROJECT_ROOT/bd" ]; then
    BD="$PROJECT_ROOT/bd"
else
    TMP_BUILD_DIR="$(mktemp -d)"
    BD="$TMP_BUILD_DIR/bd"
    echo "Building temporary bd for docs generation..."
    (cd "$PROJECT_ROOT" && CGO_ENABLED=0 go build -tags gms_pure_go -o "$BD" ./cmd/bd/)
fi

# Guard against a CGO-enabled bd: it exposes `bd federation` subcommands that CI never
# produces (scripts/ci/pr-policy.sh build_docs_binary uses env CGO_ENABLED=0 go build).
# If the resolved binary does not print the pure-go stub, rebuild pure-go internally.
if [ -x "$BD" ] && ! "$BD" federation --help 2>&1 | grep -q "Federation commands require CGO"; then
    echo "CGO-enabled bd detected at $BD; rebuilding pure-go binary for CI-consistent docs..."
    if [ -z "$TMP_BUILD_DIR" ]; then
        TMP_BUILD_DIR="$(mktemp -d)"
    fi
    BD="$TMP_BUILD_DIR/bd-pure"
    (cd "$PROJECT_ROOT" && CGO_ENABLED=0 go build -tags gms_pure_go -o "$BD" ./cmd/bd/)
fi

if [ ! -x "$BD" ]; then
    echo "Error: bd binary not found or not executable: $BD" >&2
    echo "Usage: $0 [--check] [--versioned VERSION] [path-to-bd]" >&2
    exit 1
fi

generate_all() {
    local root="$1"
    local args=(help --docs-root "$root")
    if [ -n "$VERSIONED_VERSION" ]; then
        args+=(--docs-version "$VERSIONED_VERSION")
    fi
    "$BD" "${args[@]}"
}

if [ "$CHECK_MODE" -eq 1 ]; then
    TMP_OUTPUT_DIR="$(mktemp -d)"
    mkdir -p "$TMP_OUTPUT_DIR/website"
    cp -Rf "$PROJECT_ROOT/website/docs" "$TMP_OUTPUT_DIR/website/docs"
    if [ -d "$PROJECT_ROOT/website/versioned_docs" ]; then
        cp -Rf "$PROJECT_ROOT/website/versioned_docs" "$TMP_OUTPUT_DIR/website/versioned_docs"
    fi
    if [ -f "$PROJECT_ROOT/website/versions.json" ]; then
        cp -f "$PROJECT_ROOT/website/versions.json" "$TMP_OUTPUT_DIR/website/versions.json"
    fi

    generate_all "$TMP_OUTPUT_DIR"

    if ! diff -qr \
        "$PROJECT_ROOT/docs/CLI_REFERENCE.md" \
        "$TMP_OUTPUT_DIR/docs/CLI_REFERENCE.md" >/dev/null; then
        echo "FAIL: docs/CLI_REFERENCE.md is out of sync with live CLI help."
        echo "Run: ./scripts/generate-cli-docs.sh ${BD_ARG:-}"
        diff -u "$PROJECT_ROOT/docs/CLI_REFERENCE.md" "$TMP_OUTPUT_DIR/docs/CLI_REFERENCE.md" | sed -n '1,120p' || true
        exit 1
    fi

    check_dirs=("website/docs/cli-reference")
    if [ -n "$VERSIONED_VERSION" ]; then
        check_dirs+=("website/versioned_docs/version-$VERSIONED_VERSION/cli-reference")
    fi

    for rel in "${check_dirs[@]}"; do
        if [ -d "$PROJECT_ROOT/$rel" ]; then
            if ! diff -qr "$PROJECT_ROOT/$rel" "$TMP_OUTPUT_DIR/$rel" >/dev/null; then
                echo "FAIL: $rel is out of sync with live CLI help."
                echo "Run: ./scripts/generate-cli-docs.sh ${BD_ARG:-}"
                diff -ur "$PROJECT_ROOT/$rel" "$TMP_OUTPUT_DIR/$rel" | sed -n '1,160p' || true
                exit 1
            fi
        fi
    done

    "$PROJECT_ROOT/scripts/generate-llms-full.sh" --check --source-root "$TMP_OUTPUT_DIR"

    echo "PASS: generated CLI docs are fresh"
else
    generate_all "$PROJECT_ROOT"
    echo "Generated CLI docs from: $($BD version 2>/dev/null | head -1 || echo "$BD")"
    if [ -n "$VERSIONED_VERSION" ]; then
        echo "Updated docs/CLI_REFERENCE.md, website CLI reference pages, and version-$VERSIONED_VERSION CLI snapshot"
    else
        echo "Updated docs/CLI_REFERENCE.md and live website CLI reference pages"
    fi
fi
