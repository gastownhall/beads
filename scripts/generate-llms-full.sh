#!/bin/bash
# Generate llms-full.txt from website documentation
# This concatenates all docs into a single file for LLM consumption.
# Uses the latest *released* docs snapshot (versioned_docs) when present so
# llms-full matches the default site version, not unreleased "Next".

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
OUTPUT_FILE="$PROJECT_ROOT/website/static/llms-full.txt"

resolve_docs_dir() {
  local vfile="$PROJECT_ROOT/website/versions.json"
  if [ ! -f "$vfile" ]; then
    echo "$PROJECT_ROOT/website/docs"
    return
  fi
  # versions.json lists newest first; first semver-like string wins
  local ver
  ver=$(grep -oE '"[0-9][^"]*"' "$vfile" | head -1 | tr -d '"')
  if [ -n "$ver" ] && [ -d "$PROJECT_ROOT/website/versioned_docs/version-$ver" ]; then
    echo "$PROJECT_ROOT/website/versioned_docs/version-$ver"
  else
    echo "$PROJECT_ROOT/website/docs"
  fi
}

DOCS_DIR="$(resolve_docs_dir)"
DOC_VERSION_LABEL="development"
if [[ "$DOCS_DIR" == *"/versioned_docs/version-"* ]]; then
  DOC_VERSION_LABEL="${DOCS_DIR##*version-}"
fi

# Header
cat > "$OUTPUT_FILE" << EOF
# Beads Documentation (Complete)

> This file contains the complete beads documentation for LLM consumption.
> Generated automatically from the documentation source files (version: ${DOC_VERSION_LABEL}).
> For the web version, visit: https://gastownhall.github.io/beads/

---

EOF

# Function to process a markdown file
process_file() {
    local file="$1"
    local relative_path="${file#$DOCS_DIR/}"

    echo "<document path=\"docs/$relative_path\">" >> "$OUTPUT_FILE"
    echo "" >> "$OUTPUT_FILE"

    # Remove frontmatter and add content
    sed '/^---$/,/^---$/d' "$file" >> "$OUTPUT_FILE"

    echo "" >> "$OUTPUT_FILE"
    echo "</document>" >> "$OUTPUT_FILE"
    echo "" >> "$OUTPUT_FILE"
}

# Process files in order (intro first, then by category)
if [ -f "$DOCS_DIR/intro.md" ]; then
    process_file "$DOCS_DIR/intro.md"
fi

# Process directories in logical order
for dir in getting-started core-concepts architecture cli-reference workflows multi-agent integrations recovery reference; do
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
cat >> "$OUTPUT_FILE" << 'EOF'
---

# End of Documentation

For updates and contributions, visit: https://github.com/gastownhall/beads
EOF

echo "Generated: $OUTPUT_FILE"
echo "Source docs: $DOCS_DIR"
echo "Size: $(wc -c < "$OUTPUT_FILE" | tr -d ' ') bytes"
echo "Lines: $(wc -l < "$OUTPUT_FILE" | tr -d ' ')"
