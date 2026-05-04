#!/bin/bash
# generate-cli-docs.sh — Generate CLI reference docs from the live bd command tree.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

CHECK_MODE=0
if [ "${1:-}" = "--check" ]; then
    CHECK_MODE=1
    shift
fi

BD_ARG="${1:-}"
TMP_BUILD_DIR=""
TMP_OUTPUT_DIR=""

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

if [ ! -x "$BD" ]; then
    echo "Error: bd binary not found or not executable: $BD" >&2
    echo "Usage: $0 [--check] [path-to-bd]" >&2
    exit 1
fi

command_doc_id() {
    printf '%s' "$1" \
        | tr '[:upper:] ' '[:lower:]-' \
        | sed -E 's/[^a-z0-9-]+/-/g; s/-+/-/g; s/^-//; s/-$//'
}

trim_trailing_blank_lines() {
    perl -0pi -e 's/\n+\z/\n/' "$1"
}

generate_index() {
    local out_dir="$1"
    local commands_file="$2"
    local count
    count="$(wc -l < "$commands_file" | tr -d ' ')"

    cat > "$out_dir/index.md" << EOF
---
id: index
title: CLI Reference
sidebar_position: 0
---

# CLI Reference

<!-- AUTO-GENERATED: do not edit manually -->
Generated from \`bd help --list\` and \`bd help --doc <command>\`.

This reference covers all ${count} live top-level \`bd\` commands. Regenerate it with:

\`\`\`bash
./scripts/generate-cli-docs.sh
\`\`\`

## Commands

EOF

    while IFS= read -r cmd; do
        local doc_id
        doc_id="$(command_doc_id "$cmd")"
        printf -- "- [\`bd %s\`](./%s.md)\n" "$cmd" "$doc_id" >> "$out_dir/index.md"
    done < "$commands_file"
}

generate_cli_dir() {
    local out_dir="$1"
    local commands_file="$2"

    mkdir -p "$out_dir"
    rm -f "$out_dir"/*.md
    generate_index "$out_dir" "$commands_file"

    while IFS= read -r cmd; do
        local doc_id
        doc_id="$(command_doc_id "$cmd")"
        "$BD" help --doc "$cmd" > "$out_dir/$doc_id.md"
        trim_trailing_blank_lines "$out_dir/$doc_id.md"
    done < "$commands_file"
}

generate_all() {
    local root="$1"
    local commands_file="$root/.cli-commands.txt"

    mkdir -p "$root/docs"
    "$BD" help --list > "$commands_file"
    "$BD" help --all > "$root/docs/CLI_REFERENCE.md"
    trim_trailing_blank_lines "$root/docs/CLI_REFERENCE.md"

    generate_cli_dir "$root/website/docs/cli-reference" "$commands_file"

    local versioned_root="$PROJECT_ROOT/website/versioned_docs/version-1.0.0"
    if [ -d "$versioned_root" ]; then
        generate_cli_dir "$root/website/versioned_docs/version-1.0.0/cli-reference" "$commands_file"
    fi

    rm -f "$commands_file"
}

if [ "$CHECK_MODE" -eq 1 ]; then
    TMP_OUTPUT_DIR="$(mktemp -d)"
    generate_all "$TMP_OUTPUT_DIR"

    if ! diff -qr \
        "$PROJECT_ROOT/docs/CLI_REFERENCE.md" \
        "$TMP_OUTPUT_DIR/docs/CLI_REFERENCE.md" >/dev/null; then
        echo "FAIL: docs/CLI_REFERENCE.md is out of sync with live CLI help."
        echo "Run: ./scripts/generate-cli-docs.sh ${BD_ARG:-}"
        diff -u "$PROJECT_ROOT/docs/CLI_REFERENCE.md" "$TMP_OUTPUT_DIR/docs/CLI_REFERENCE.md" | sed -n '1,120p' || true
        exit 1
    fi

    for rel in \
        website/docs/cli-reference \
        website/versioned_docs/version-1.0.0/cli-reference; do
        if [ -d "$PROJECT_ROOT/$rel" ]; then
            if ! diff -qr "$PROJECT_ROOT/$rel" "$TMP_OUTPUT_DIR/$rel" >/dev/null; then
                echo "FAIL: $rel is out of sync with live CLI help."
                echo "Run: ./scripts/generate-cli-docs.sh ${BD_ARG:-}"
                diff -ur "$PROJECT_ROOT/$rel" "$TMP_OUTPUT_DIR/$rel" | sed -n '1,160p' || true
                exit 1
            fi
        fi
    done

    echo "PASS: generated CLI docs are fresh"
else
    generate_all "$PROJECT_ROOT"
    echo "Generated CLI docs from: $($BD version 2>/dev/null | head -1 || echo "$BD")"
    echo "Updated docs/CLI_REFERENCE.md and website CLI reference pages"
fi
