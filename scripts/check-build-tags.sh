#!/usr/bin/env bash
# check-build-tags.sh — source-time guard for ICU regression.
#
# Scans tracked scripts, CI workflows, and git hooks. Fails when a
# `go build|test|run|generate|install` invocation neither:
#   (a) carries -tags=...gms_pure_go itself, nor
#   (b) appears in a file that sources .buildflags beforehand, nor
#   (c) is an exempt third-party tool install (go install X@version).
#
# This is the source-time companion to scripts/verify-cgo.sh (which is a
# runtime check on release binaries). See docs/ICU-POLICY.md.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

# Candidate files: shell scripts, workflows, git hooks, the Makefile.
mapfile -t candidates < <(
    git ls-files \
        '*.sh' \
        '.github/workflows/*.yml' \
        '.github/workflows/*.yaml' \
        '.github/scripts/*' \
        '.githooks/*' \
        'Makefile' 2>/dev/null || true
)

# Files that intentionally opt out of the policy.
opt_out_regex='^(scripts/test-cgo\.sh|scripts/test-icu-path\.sh|scripts/check-build-tags\.sh|examples/)'

fail=0
for f in "${candidates[@]}"; do
    [[ -f "$f" ]] || continue
    [[ "$f" =~ $opt_out_regex ]] && continue

    # Per-file opt-out marker for files that legitimately test the ICU path.
    if head -n 5 "$f" | grep -q '^# build-tags: allow-bare'; then
        continue
    fi

    # Does the file source .buildflags before any `go` invocation?
    # If so, GOFLAGS covers all bare `go` commands in the file.
    sources_buildflags=no
    if grep -Eq '(^|[[:space:]])(source|\.)[[:space:]]+[^#]*\.buildflags' "$f"; then
        sources_buildflags=yes
    fi

    # Does the file define a make/shell variable that carries the tag?
    # e.g. `BUILD_TAGS := gms_pure_go` in the Makefile. If so, references
    # like `-tags "$(BUILD_TAGS)"` count as tagged.
    declare -a tag_vars=()
    while IFS= read -r var; do
        tag_vars+=("$var")
    done < <(grep -E '^[[:space:]]*(export[[:space:]]+)?[A-Z_]+[[:space:]]*[:?+]?=[[:space:]]*["'"'"']*[^"'"'"']*gms_pure_go' "$f" \
        | sed -E 's/^[[:space:]]*(export[[:space:]]+)?([A-Z_]+).*/\2/' || true)

    while IFS= read -r hit; do
        lineno="${hit%%:*}"
        line="${hit#*:}"

        # Skip shell comments (allowing leading whitespace).
        [[ "$line" =~ ^[[:space:]]*# ]] && continue

        stripped="$line"

        # Skip string literals that happen to mention `go <verb>`, e.g.
        # log_error "go install failed" or echo "Run: go install ...".
        # Heuristics:
        #   (a) `go <verb>` immediately preceded by a quote (unlikely edge case)
        #   (b) line is a log/echo/printf call with a quoted argument. Any
        #       `go <verb>` inside such a line is message text, not a command.
        if [[ "$stripped" =~ [\"\']\ *go[[:space:]]+(build|test|run|install|generate) ]]; then
            continue
        fi
        if [[ "$stripped" =~ (log_[a-z_]+|echo|printf)[[:space:]]+[\"\'] ]]; then
            continue
        fi

        verb=""
        if [[ "$stripped" =~ (^|[^[:alnum:]_/.-])go[[:space:]]+(build|test|run|generate|install)($|[[:space:]]) ]]; then
            verb="${BASH_REMATCH[2]}"
        else
            continue
        fi

        # Allow third-party tool invocations pinned by version:
        #   `go install some/tool@version`
        #   `go run some/tool@version`
        # These build their own module, not beads, so our tags don't apply.
        if [[ "$verb" == "install" || "$verb" == "run" ]]; then
            if [[ "$stripped" =~ @(latest|main|v[0-9]) ]]; then
                continue
            fi
        fi

        # Allow if the tag is literal on this line.
        if [[ "$stripped" == *gms_pure_go* ]]; then
            continue
        fi

        # Allow if the line references a file-defined variable that holds the tag.
        matched_var=no
        for v in "${tag_vars[@]}"; do
            if [[ "$stripped" == *"\$($v)"* || \
                  "$stripped" == *"\${$v}"* || \
                  "$stripped" == *"\$$v"* ]]; then
                matched_var=yes
                break
            fi
        done
        if [[ "$matched_var" == "yes" ]]; then
            continue
        fi

        # Allow if the file sources .buildflags.
        if [[ "$sources_buildflags" == "yes" ]]; then
            continue
        fi

        printf 'error: %s:%s: bare `go %s` without -tags=gms_pure_go\n' "$f" "$lineno" "$verb" >&2
        printf '       %s\n' "$line" >&2
        fail=1
    done < <(grep -n -E '\bgo[[:space:]]+(build|test|run|generate|install)\b' "$f" 2>/dev/null || true)
done

if [[ "$fail" -ne 0 ]]; then
    cat >&2 <<'EOF'

The beads project requires every `go build|test|run|generate|install`
invocation to build with -tags=gms_pure_go (see docs/ICU-POLICY.md).

Fix by EITHER:
  1. Source .buildflags in the script (preferred, canonical):
       # shellcheck source=../.buildflags
       source "$PROJECT_ROOT/.buildflags"
  2. Pass -tags=gms_pure_go (or -tags=other,gms_pure_go) explicitly.
  3. Add a '# build-tags: allow-bare' marker in the top 5 lines of the
     file if it intentionally exercises the ICU path.
EOF
    exit 1
fi

echo "check-build-tags: ${#candidates[@]} file(s) scanned, all clear."
