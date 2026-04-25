#!/usr/bin/env bash
# Guard user-facing docs and hints against unsupported first-party go install forms.
#
# Plain `go install github.com/.../beads/cmd/bd@latest` takes the CGO+ICU path
# on many hosts. Keep first-party guidance on one of the documented supported
# modes:
#   - CGO_ENABLED=0 ... (server-mode only)
#   - ... gms_pure_go ... (embedded-capable, no ICU)

set -euo pipefail

fail=0

while IFS= read -r hit; do
    file="${hit%%:*}"
    rest="${hit#*:}"
    line_no="${rest%%:*}"
    line="${rest#*:}"

    case "$file" in
        CHANGELOG.md|docs/GETTING_STARTED_ANALYSIS.md)
            continue
            ;;
    esac

    if [[ "$line" == *"CGO_ENABLED=0"* || "$line" == *'CGO_ENABLED="0"'* || "$line" == *"gms_pure_go"* ]]; then
        continue
    fi

    printf 'error: %s:%s: unsupported first-party bare go install guidance\n' "$file" "$line_no" >&2
    printf '       use CGO_ENABLED=0 for server mode or GOFLAGS=-tags=gms_pure_go for embedded mode\n' >&2
    fail=1
done < <(
    git grep -n -E 'go install github\.com/(steveyegge|gastownhall)/beads/cmd/bd@latest' -- . || true
)

exit "$fail"
