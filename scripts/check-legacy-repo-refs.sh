#!/usr/bin/env bash
# Guard against stale pre-move repository references (GH#3059).
#
# The repository moved from steveyegge/beads to gastownhall/beads, but go.mod
# still declares `module github.com/steveyegge/beads` on purpose (released
# tags declare that path; see docs/getting-started/installation.md). Every
# import therefore contains "steveyegge", and stale repository URLs hide in
# that noise. This guard tells the two apart:
#
#   allowed  the go.mod module path (and its regex-escaped form) when it is
#            not part of a URL (imports, go install, require/replace,
#            depguard, gazelle prefix, the Go Report Card badge);
#            the author profile https://github.com/steveyegge; @steveyegge
#            and steveyegge's (attribution); and the history/fixture paths
#            and lines allowlisted below.
#   refused  any other "steveyegge" (case-sensitive, so the legacy winget id
#            SteveYegge.beads is not matched): github.com/steveyegge/beads/...
#            URLs, raw.githubusercontent.com/steveyegge/..., steveyegge.github.io,
#            `marketplace add steveyegge/beads`, steveyegge/beads#N, ...
#
# The allowance is keyed on go.mod, so if the module path is ever renamed this
# guard becomes the completeness check for that rename with no edit here.

set -euo pipefail

module=""
if [[ -f go.mod ]]; then
    module="$(awk '$1 == "module" { print $2; exit }' go.mod)"
fi
case "$module" in
    *steveyegge*) ;;
    *) module="" ;;
esac
escaped_module="${module//./\\.}"

# History and fixtures that must keep the old path forever.
excludes=(
    ':(exclude)CHANGELOG.md'
    ':(exclude)release-gates/'
    ':(exclude)tests/migration/source_tag_modules.json'
    ':(exclude)tests/migration/gen_source_tag_lock.py'
    ':(exclude)tests/migration/BUILD.bazel'
    ':(exclude)scripts/migration_corpus_bazel_test.go'
    ':(exclude)scripts/migration-test/lib/versions.sh'
    ':(exclude)scripts/migration-test/release-catalog.json'
    ':(exclude)scripts/migration-test/catalog/main.go'
    ':(exclude)scripts/check-go-install-guidance.sh'
    ':(exclude)scripts/check-legacy-repo-refs.sh'
    ':(exclude)scripts/check_legacy_repo_refs_test.go'
)

hits="$(git grep -n -I -e steveyegge -- . "${excludes[@]}" || true)"
[[ -n "$hits" ]] || exit 0

out="$(
    # ENVIRON, not awk -v: -v would process the backslash escapes in EM.
    printf '%s\n' "$hits" | LEGACY_M="$module" LEGACY_EM="$escaped_module" awk '
    # strip_module deletes every occurrence of lit not preceded by "://".
    function strip_module(s, lit,   out, i, pre) {
        if (lit == "") return s
        out = ""
        while ((i = index(s, lit)) > 0) {
            pre = substr(s, 1, i - 1)
            if (length(pre) >= 3 && substr(pre, length(pre) - 2) == "://") {
                out = out pre lit
            } else {
                out = out pre
            }
            s = substr(s, i + length(lit))
        }
        return out s
    }
    # strip_profile deletes the author-profile URL when it is not followed by
    # "/" or a word character (so .../steveyegge/beads stays refused).
    function strip_profile(s,   lit, out, i, next_ch) {
        lit = "https://github.com/steveyegge"
        out = ""
        while ((i = index(s, lit)) > 0) {
            next_ch = substr(s, i + length(lit), 1)
            if (next_ch == "/" || next_ch ~ /[A-Za-z0-9_]/) {
                out = out substr(s, 1, i - 1 + length(lit))
            } else {
                out = out substr(s, 1, i - 1)
            }
            s = substr(s, i + length(lit))
        }
        return out s
    }
    function strip_literal(s, lit,   out, i) {
        out = ""
        while ((i = index(s, lit)) > 0) {
            out = out substr(s, 1, i - 1)
            s = substr(s, i + length(lit))
        }
        return out s
    }
    BEGIN { M = ENVIRON["LEGACY_M"]; EM = ENVIRON["LEGACY_EM"] }
    {
        file = $0
        sub(/:.*/, "", file)
        rest = substr($0, length(file) + 2)
        line_no = rest
        sub(/:.*/, "", line_no)
        text = substr(rest, length(line_no) + 2)

        # Line allowlist: exact file + fixed substring.
        if (file == "cmd/bd/info.go") text = strip_literal(text, "from steveyegge/beads to gastownhall/beads")
        if (file == "docs/getting-started/upgrading.md") text = strip_literal(text, "brew untap steveyegge/beads")

        text = strip_module(text, M)
        text = strip_module(text, EM)
        text = strip_profile(text)
        text = strip_literal(text, "@steveyegge")
        text = strip_literal(text, "steveyegge'\''s")

        if (index(text, "steveyegge") > 0) {
            printf "error: %s:%s: legacy steveyegge repository reference\n", file, line_no
        }
    }'
)"

if [[ -n "$out" ]]; then
    printf '%s\n' "$out" >&2
    printf '       the repository is gastownhall/beads; only the go.mod module path (%s) keeps the old org\n' "${module:-none}" >&2
    exit 1
fi
