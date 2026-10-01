#!/usr/bin/env bash
# check-build-tags.sh — source-time guard for ICU regression.
#
# Scans tracked shell scripts, git hooks, and the Makefile. Fails when a
# `go build|test|run|generate|install` invocation neither:
#   (a) carries -tags=...gms_pure_go itself, nor
#   (b) appears in a file that sources .buildflags beforehand, nor
#   (c) is an exempt third-party tool install (go install X@version).
#
# Bazel invocations (`bazel build|test|run|...`) are not `go` invocations:
# their Go build tags come from the committed .bazelrc, not GOFLAGS. When any
# scanned file invokes Bazel, the script instead requires .bazelrc to set
# `--@rules_go//go/config:tags=...gms_pure_go` for build (and so test/run).
#
# GitHub Actions `run` steps are no longer scanned here; they are checked
# structurally by scripts/checkworkflowtags.
#
# This is the source-time companion to scripts/verify-cgo.sh (which is a
# runtime check on release binaries). See engdocs/ICU-POLICY.md.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

# Candidate files: shell scripts, git hooks, and the Makefile. Workflow `run`
# steps are checked structurally by scripts/checkworkflowtags, so they are no
# longer scanned for bare `go` commands here.
mapfile -t candidates < <(
    git ls-files \
        '*.sh' \
        '.github/scripts/*' \
        '.githooks/*' \
        'Makefile' 2>/dev/null || true
)

# ...but the Bazel census below is a separate question, and workflows are where
# most `bazel` invocations live. Moving workflows to the Go checker dropped them
# from `bazel_users` as a side effect, which would let .bazelrc lose its tag with
# this gate still green — and scripts/checkworkflowtags does no Bazel handling at
# all. So workflows keep feeding the census through their own list. Kept separate
# from `candidates` on purpose: this list must NOT re-enter the `go`-verb scan.
mapfile -t bazel_census_only < <(
    git ls-files \
        '.github/workflows/*.yml' \
        '.github/workflows/*.yaml' 2>/dev/null || true
)

# Files that intentionally opt out of the policy.
opt_out_regex='^(scripts/test-cgo\.sh|scripts/test-icu-path\.sh|scripts/check-build-tags\.sh|examples/)'

fail=0
bazel_users=()
bazel_invocation_regex='(^|[^[:alnum:]_/.-])(bazel|bazelisk|\$\(BAZEL\)|\$\{?BAZEL\}?)[[:space:]]+(build|test|run|coverage|query|cquery|aquery|mod)($|[[:space:]])'
for f in "${candidates[@]}"; do
    [[ -f "$f" ]] || continue
    [[ "$f" =~ $opt_out_regex ]] && continue

    # Per-file opt-out marker for files that legitimately test the ICU path.
    if head -n 5 "$f" | grep -q '^# build-tags: allow-bare'; then
        continue
    fi

    # Record files that drive Bazel; .bazelrc must then carry the tag.
    if grep -Eq "$bazel_invocation_regex" "$f" 2>/dev/null; then
        bazel_users+=("$f")
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

        # Bazel invocations are governed by .bazelrc (checked below), even
        # when their arguments mention a go verb (e.g. `bazel run @rules_go//go -- test`).
        # Only the Bazel command itself is exempt: split the line into shell
        # command segments (on &&, ||, ;, |, $( and backticks) and drop the
        # segments that invoke Bazel, so `bazel build x && go test ./...`
        # still has its `go test` checked. Make variable references like
        # $(BAZEL) are rewritten to ${BAZEL} first so they are not split.
        # Lines without a Bazel invocation are checked unchanged.
        # Pure bash (no sed): BSD sed does not expand \n in replacements.
        segmented="$stripped"
        while [[ "$segmented" =~ \$\(([A-Za-z_][A-Za-z0-9_]*)\) ]]; do
            segmented="${segmented/"${BASH_REMATCH[0]}"/\$\{${BASH_REMATCH[1]}\}}"
        done
        nl=$'\n'
        segmented="${segmented//&&/$nl}"
        segmented="${segmented//||/$nl}"
        segmented="${segmented//;/$nl}"
        segmented="${segmented//|/$nl}"
        segmented="${segmented//\$\(/$nl}"
        segmented="${segmented//\`/$nl}"
        if [[ "$segmented" =~ $bazel_invocation_regex ]]; then
            stripped=""
            while IFS= read -r segment; do
                [[ "$segment" =~ $bazel_invocation_regex ]] && continue
                stripped+="$segment"$'\n'
            done <<< "$segmented"
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

# Workflows contribute to the Bazel census only. Deliberately a separate loop
# rather than another arm in the one above: nothing here may reach the `go`-verb
# scan, which scripts/checkworkflowtags now owns for these files.
for f in "${bazel_census_only[@]}"; do
    [[ -f "$f" ]] || continue
    if grep -Eq "$bazel_invocation_regex" "$f" 2>/dev/null; then
        bazel_users+=("$f")
    fi
done

# Bazel: every `bazel` invocation reads the committed .bazelrc, so the tag
# must be set there once. Accept `build` or `common` (test/run inherit build).
bazelrc_tag_regex='^[[:space:]]*(build|common)[[:space:]]+(.*[[:space:]])?--@(@)?rules_go//go/config:tags=([^[:space:]]*,)?gms_pure_go(,[^[:space:]]*)?([[:space:]]|$)'
if [[ ${#bazel_users[@]} -gt 0 || -f .bazelrc ]]; then
    if [[ ! -f .bazelrc ]]; then
        printf 'error: %s invoke(s) Bazel but .bazelrc is missing\n' "${bazel_users[*]}" >&2
        fail=1
    elif ! grep -Eq "$bazelrc_tag_regex" .bazelrc; then
        printf 'error: .bazelrc does not set --@rules_go//go/config:tags=gms_pure_go for build\n' >&2
        if [[ ${#bazel_users[@]} -gt 0 ]]; then
            printf '       Bazel is invoked from: %s\n' "${bazel_users[*]}" >&2
        fi
        fail=1
    fi
fi

if [[ "$fail" -ne 0 ]]; then
    cat >&2 <<'EOF'

The beads project requires every `go build|test|run|generate|install`
invocation to build with -tags=gms_pure_go (see engdocs/ICU-POLICY.md).

Fix by EITHER:
  1. Source .buildflags in the script (preferred, canonical):
       # shellcheck source=../.buildflags
       source "$PROJECT_ROOT/.buildflags"
  2. Pass -tags=gms_pure_go (or -tags=other,gms_pure_go) explicitly.
  3. Add a '# build-tags: allow-bare' marker in the top 5 lines of the
     file if it intentionally exercises the ICU path.

Bazel invocations take their tags from .bazelrc, which must contain:
  build --@rules_go//go/config:tags=gms_pure_go
EOF
    exit 1
fi

echo "check-build-tags: ${#candidates[@]} file(s) scanned, all clear."
