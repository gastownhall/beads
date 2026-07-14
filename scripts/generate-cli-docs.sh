#!/bin/bash
# generate-cli-docs.sh — Generate CLI reference docs from the live bd command tree.
#
# Two stages:
#   1. `bd help --docs-root <root>` emits vendor-neutral output only:
#      docs/CLI_REFERENCE.md plus a generic per-command tree at
#      build/cli-docs/ (uncommitted staging).
#   2. `go run ./tools/docsmint <root>` post-processes the staging tree into
#      the committed Mintlify pages at docs/cli-reference/ and splices the
#      CLI Reference pages array in docs/docs.json.
# bd never emits site-generator-specific formats; all Mintlify targeting
# lives in tools/docsmint.

set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

CHECK_MODE=0
BD_ARG=""
TMP_BUILD_DIR=""
TMP_OUTPUT_DIR=""

while [ "$#" -gt 0 ]; do
    case "$1" in
        --check)
            CHECK_MODE=1
            shift
            ;;
        -h|--help)
            cat <<'EOF'
Usage: scripts/generate-cli-docs.sh [--check] [path-to-bd]

Generate the committed CLI docs (docs/CLI_REFERENCE.md, docs/cli-reference/,
and the CLI pages array in docs/docs.json) from one bd process plus the
tools/docsmint post-processor. --check regenerates into a scratch root and
fails if the committed copies are stale.

If the resolved bd binary is CGO-enabled it emits the full `bd federation`
help tree that CI (CGO_ENABLED=0) stubs out; the script rebuilds a pinned
pure-go binary to keep committed docs in sync. Set BD_DOCS_ALLOW_CGO=1 to
bypass that rebuild and trust the supplied binary as-is.
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
elif [ "$CHECK_MODE" -eq 0 ] && [ -x "$PROJECT_ROOT/bd" ]; then
    # Convenience for regeneration only. --check never trusts a repo-root
    # ./bd it wasn't explicitly given: the guard below detects CGO-ness, not
    # staleness, so a stale pure-go ./bd would produce a false "fresh".
    BD="$PROJECT_ROOT/bd"
else
    TMP_BUILD_DIR="$(mktemp -d)"
    BD="$TMP_BUILD_DIR/bd"
    echo "Building temporary bd for docs generation..."
    (cd "$PROJECT_ROOT" && CGO_ENABLED=0 go build -tags gms_pure_go -o "$BD" ./cmd/bd/)
fi

# Guard against a CGO-enabled bd: it exposes the full `bd federation` subcommand tree
# that CI never produces (scripts/ci/pr-policy.sh build_docs_binary uses env
# CGO_ENABLED=0 go build). A CGO build therefore emits ~hundreds of lines of federation
# help that the committed, CI-built docs stub out ("built without CGO support"), so a
# naive regen on a machine with a C compiler produces spurious federation churn.
#
# The pure-go federation stub prints "Federation commands require CGO" (see
# cmd/bd/federation_nocgo.go); a CGO build does not. If the resolved binary is missing
# that stub marker, warn and rebuild a pinned CGO_ENABLED=0 -tags gms_pure_go binary so
# the committed docs always match CI. Set BD_DOCS_ALLOW_CGO=1 to bypass the rebuild and
# trust the supplied binary as-is (e.g. to deliberately regenerate the full federation
# tree); you then own any federation churn the diff introduces.
if [ -x "$BD" ] && ! "$BD" federation --help 2>&1 | grep -q "Federation commands require CGO"; then
    if [ "${BD_DOCS_ALLOW_CGO:-0}" = "1" ]; then
        echo "WARNING: $BD looks CGO-enabled and emits the full 'bd federation' help tree," >&2
        echo "         which CI's pure-go docs build stubs out. BD_DOCS_ALLOW_CGO=1 is set," >&2
        echo "         using it anyway; expect federation doc churn unless that is intended." >&2
    else
        echo "WARNING: $BD looks CGO-enabled; its 'bd federation' help differs from CI's" >&2
        echo "         pure-go build and would add spurious federation doc churn." >&2
        echo "         Rebuilding a pinned pure-go (CGO_ENABLED=0 -tags gms_pure_go) binary" >&2
        echo "         for CI-consistent docs. Set BD_DOCS_ALLOW_CGO=1 to use it as-is." >&2
        if [ -z "$TMP_BUILD_DIR" ]; then
            TMP_BUILD_DIR="$(mktemp -d)"
        fi
        BD="$TMP_BUILD_DIR/bd-pure"
        (cd "$PROJECT_ROOT" && CGO_ENABLED=0 go build -tags gms_pure_go -o "$BD" ./cmd/bd/)
    fi
fi

if [ ! -x "$BD" ]; then
    echo "Error: bd binary not found or not executable: $BD" >&2
    echo "Usage: $0 [--check] [path-to-bd]" >&2
    exit 1
fi

generate_all() {
    local root="$1"
    "$BD" help --docs-root "$root"
    (cd "$PROJECT_ROOT" && go run -tags=gms_pure_go ./tools/docsmint "$root")
}

if [ "$CHECK_MODE" -eq 1 ]; then
    TMP_OUTPUT_DIR="$(mktemp -d)"
    mkdir -p "$TMP_OUTPUT_DIR/docs"
    # Seed the committed artifacts the pipeline rewrites, then regenerate and diff.
    cp -f "$PROJECT_ROOT/docs/docs.json" "$TMP_OUTPUT_DIR/docs/docs.json"
    if [ -d "$PROJECT_ROOT/docs/cli-reference" ]; then
        cp -Rf "$PROJECT_ROOT/docs/cli-reference" "$TMP_OUTPUT_DIR/docs/cli-reference"
    fi

    generate_all "$TMP_OUTPUT_DIR"

    if ! diff -q \
        "$PROJECT_ROOT/docs/CLI_REFERENCE.md" \
        "$TMP_OUTPUT_DIR/docs/CLI_REFERENCE.md" >/dev/null; then
        echo "FAIL: docs/CLI_REFERENCE.md is out of sync with live CLI help."
        echo "Run: ./scripts/generate-cli-docs.sh ${BD_ARG:-}"
        diff -u "$PROJECT_ROOT/docs/CLI_REFERENCE.md" "$TMP_OUTPUT_DIR/docs/CLI_REFERENCE.md" | sed -n '1,120p' || true
        exit 1
    fi

    if ! diff -q "$PROJECT_ROOT/docs/docs.json" "$TMP_OUTPUT_DIR/docs/docs.json" >/dev/null; then
        echo "FAIL: docs/docs.json CLI Reference navigation is out of sync with live CLI help."
        echo "Run: ./scripts/generate-cli-docs.sh ${BD_ARG:-}"
        diff -u "$PROJECT_ROOT/docs/docs.json" "$TMP_OUTPUT_DIR/docs/docs.json" | sed -n '1,80p' || true
        exit 1
    fi

    if ! diff -qr "$PROJECT_ROOT/docs/cli-reference" "$TMP_OUTPUT_DIR/docs/cli-reference" >/dev/null; then
        echo "FAIL: docs/cli-reference is out of sync with live CLI help."
        echo "Run: ./scripts/generate-cli-docs.sh ${BD_ARG:-}"
        diff -ur "$PROJECT_ROOT/docs/cli-reference" "$TMP_OUTPUT_DIR/docs/cli-reference" | sed -n '1,160p' || true
        exit 1
    fi

    echo "PASS: generated CLI docs are fresh"
else
    generate_all "$PROJECT_ROOT"
    echo "Generated CLI docs from: $($BD version 2>/dev/null | head -1 || echo "$BD")"
    echo "Updated docs/CLI_REFERENCE.md, docs/cli-reference/, and the docs.json CLI nav"
fi
