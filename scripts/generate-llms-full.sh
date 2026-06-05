#!/bin/bash
# Generate llms-full.txt from website documentation
# This concatenates all docs into a single file for LLM consumption.
# Uses the latest *released* docs snapshot (versioned_docs) when present so
# llms-full matches the default site version, not unreleased "Next".

set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
SOURCE_ROOT="$PROJECT_ROOT"
OUTPUT_FILE="$PROJECT_ROOT/website/static/llms-full.txt"
CHECK_MODE=0
TMP_OUTPUT_FILE=""

usage() {
  echo "Usage: $0 [--check] [--source-root DIR] [--output FILE]" >&2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --check)
      CHECK_MODE=1
      shift
      ;;
    --source-root)
      if [ "$#" -lt 2 ]; then
        usage
        exit 1
      fi
      SOURCE_ROOT="$2"
      shift 2
      ;;
    --output)
      if [ "$#" -lt 2 ]; then
        usage
        exit 1
      fi
      OUTPUT_FILE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 1
      ;;
  esac
done

cleanup() {
  if [ -n "$TMP_OUTPUT_FILE" ]; then
    rm -f "$TMP_OUTPUT_FILE"
  fi
}
trap cleanup EXIT

resolve_docs_dir() {
  local vfile="$SOURCE_ROOT/website/versions.json"
  if [ ! -f "$vfile" ]; then
    echo "$SOURCE_ROOT/website/docs"
    return
  fi
  # versions.json lists newest first; first semver-like string wins
  local ver
  ver=$(grep -oE '"[0-9][^"]*"' "$vfile" | head -1 | tr -d '"')
  if [ -n "$ver" ] && [ -d "$SOURCE_ROOT/website/versioned_docs/version-$ver" ]; then
    echo "$SOURCE_ROOT/website/versioned_docs/version-$ver"
  else
    echo "$SOURCE_ROOT/website/docs"
  fi
}

DOCS_DIR="$(resolve_docs_dir)"
DOC_VERSION_LABEL="Latest"
if [[ "$DOCS_DIR" == *"/versioned_docs/version-"* ]]; then
  DOC_VERSION_LABEL="latest released"
fi

if [ "$CHECK_MODE" -eq 1 ]; then
  TMP_OUTPUT_FILE="$(mktemp)"
  GENERATED_FILE="$TMP_OUTPUT_FILE"
else
  mkdir -p "$(dirname "$OUTPUT_FILE")"
  GENERATED_FILE="$OUTPUT_FILE"
fi

# Header
cat > "$GENERATED_FILE" << EOF
# Beads Documentation (Complete)

> This file contains the complete beads documentation for LLM consumption.
> Generated automatically from the documentation source files (version: ${DOC_VERSION_LABEL}).
> For the web version, visit: https://gastownhall.github.io/beads/

---

EOF

normalize_content() {
    local relative_path="$1"

    if [ "$relative_path" = "cli-reference/index.md" ]; then
        sed -E 's/^Reference for bd v[0-9][A-Za-z0-9._-]*\. Generated/Reference for bd Latest. Generated/'
    else
        cat
    fi
}

# Function to process a markdown file
process_file() {
    local file="$1"
    local relative_path="${file#$DOCS_DIR/}"

    echo "<document path=\"docs/$relative_path\">" >> "$GENERATED_FILE"
    echo "" >> "$GENERATED_FILE"

    # Remove frontmatter and add content
    sed '/^---$/,/^---$/d' "$file" | normalize_content "$relative_path" >> "$GENERATED_FILE"

    echo "" >> "$GENERATED_FILE"
    echo "</document>" >> "$GENERATED_FILE"
    echo "" >> "$GENERATED_FILE"
}

# Process files in order (intro first, then by category)
if [ -f "$DOCS_DIR/intro.md" ]; then
    process_file "$DOCS_DIR/intro.md"
fi

# Process directories in logical order
for dir in getting-started core-concepts architecture cli-reference workflows multi-agent integrations; do
    if [ -d "$DOCS_DIR/$dir" ]; then
        # Process index first if exists
        if [ -f "$DOCS_DIR/$dir/index.md" ]; then
            process_file "$DOCS_DIR/$dir/index.md"
        fi

        # Process other files
        for file in "$DOCS_DIR/$dir"/*.md; do
            if [ -f "$file" ] && [ "$(basename "$file")" != "index.md" ]; then
                process_file "$file"
            fi
        done
    fi
done

for doc in community-tools; do
    if [ -f "$DOCS_DIR/$doc.md" ]; then
        process_file "$DOCS_DIR/$doc.md"
    fi
done

for dir in recovery reference; do
    if [ -d "$DOCS_DIR/$dir" ]; then
        # Process index first if exists
        if [ -f "$DOCS_DIR/$dir/index.md" ]; then
            process_file "$DOCS_DIR/$dir/index.md"
        fi

        # Process other files
        for file in "$DOCS_DIR/$dir"/*.md; do
            if [ -f "$file" ] && [ "$(basename "$file")" != "index.md" ]; then
                process_file "$file"
            fi
        done
    fi
done

# Add footer
cat >> "$GENERATED_FILE" << 'EOF'
---

# End of Documentation

For updates and contributions, visit: https://github.com/gastownhall/beads
EOF

if [ "$CHECK_MODE" -eq 1 ]; then
  if ! diff -q "$OUTPUT_FILE" "$GENERATED_FILE" >/dev/null; then
    echo "FAIL: website/static/llms-full.txt is out of sync with generated website docs."
    echo "Run: ./scripts/generate-llms-full.sh"
    diff -u "$OUTPUT_FILE" "$GENERATED_FILE" | sed -n '1,160p' || true
    exit 1
  fi
  echo "PASS: website/static/llms-full.txt is fresh"
else
  echo "Generated: $OUTPUT_FILE"
  echo "Source docs: $DOCS_DIR"
  echo "Size: $(wc -c < "$OUTPUT_FILE" | tr -d ' ') bytes"
  echo "Lines: $(wc -l < "$OUTPUT_FILE" | tr -d ' ')"
fi
