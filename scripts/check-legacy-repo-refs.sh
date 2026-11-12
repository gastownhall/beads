#!/usr/bin/env bash
# check-legacy-repo-refs.sh: reject stale pre-move repository references.
#
# The repository moved from steveyegge/beads to gastownhall/beads (GH#3059).
# The Go module path deliberately stays github.com/steveyegge/beads until
# released tags move (docs/getting-started/installation.md), so module-path
# shapes stay legal: import paths, go install guidance, require/replace
# directives, the gazelle prefix, depguard regexes, the Go Report Card badge.
# Repository URL shapes do not: they resolve only through GitHub's transfer
# redirect, which dies the day anyone recreates a steveyegge/beads repository.
#
# If the module path is ever renamed, this same guard becomes the
# completeness check for that rename: every leftover old-path string outside
# the history allowlist then fails with no edit to the guard.
set -euo pipefail

module_path="$(sed -n 's/^module[[:space:]][[:space:]]*//p' go.mod | head -n 1)"

# Paths that keep legacy references on purpose: release history, the frozen
# v0.9.1 source-tag fixtures, and the guards' own vocabularies.
export GUARD_ALLOW_PATHS='CHANGELOG.md
release-gates/
tests/migration/source_tag_modules.json
tests/migration/gen_source_tag_lock.py
tests/migration/BUILD.bazel
scripts/migration_corpus_bazel_test.go
scripts/migration-test/lib/versions.sh
scripts/migration-test/release-catalog.json
scripts/migration-test/catalog/main.go
scripts/check-go-install-guidance.sh
scripts/check-legacy-repo-refs.sh
scripts/check_legacy_repo_refs_test.go
scripts/repro/bd-49w8x2-old-repo-urls.sh'

# Lines that are correct as-is: exact file plus a fixed substring, so an
# exemption cannot widen to the rest of the file.
export GUARD_ALLOW_LINES='cmd/bd/info.go|from steveyegge/beads to gastownhall/beads
docs/getting-started/upgrading.md|brew untap steveyegge/beads'

export GUARD_MODULE_PATH="$module_path"

fail=0
while IFS= read -r hit; do
	printf 'error: %s: legacy steveyegge repository reference\n' "$hit" >&2
	printf '       point repository URLs at gastownhall/beads; the %s module path is legal until go.mod moves\n' "$module_path" >&2
	fail=1
done < <(
	git grep -n -I -e steveyegge -- . | awk '
		function rep(s, needle, r,  i, off) {
			off = 1
			while ((i = index(substr(s, off), needle)) > 0) {
				i = off + i - 1
				s = substr(s, 1, i - 1) r substr(s, i + length(needle))
				off = i + length(r)
			}
			return s
		}
		function allowed_path(file,  n, paths, i, p) {
			n = split(ENVIRON["GUARD_ALLOW_PATHS"], paths, "\n")
			for (i = 1; i <= n; i++) {
				p = paths[i]
				if (p == "") continue
				if (substr(p, length(p)) == "/") {
					if (index(file, p) == 1) return 1
				} else if (file == p) return 1
			}
			return 0
		}
		function allowed_line(file, line,  n, pairs, i, parts) {
			n = split(ENVIRON["GUARD_ALLOW_LINES"], pairs, "\n")
			for (i = 1; i <= n; i++) {
				if (pairs[i] == "") continue
				split(pairs[i], parts, "|")
				if (file == parts[1] && index(line, parts[2]) > 0) return 1
			}
			return 0
		}
		{
			file = $0
			sub(/:.*/, "", file)
			c1 = index($0, ":")
			c2 = c1 + index(substr($0, c1 + 1), ":")
			line_no = substr($0, c1 + 1, c2 - c1 - 1)
			content = substr($0, c2 + 1)

			if (allowed_path(file)) next
			if (allowed_line(file, content)) next

			work = content
			module = ENVIRON["GUARD_MODULE_PATH"]
			if (module != "" && index(module, "steveyegge") > 0) {
				# regex-escaped module path, as .golangci.yml depguard uses it
				esc = rep(module, ".", sprintf("%c", 92) ".")
				sent = sprintf("%c", 1)
				# Module-path references are legal unless preceded by "://",
				# where they name the repository, not the module.
				work = rep(work, "://" module, sent)
				work = rep(work, "://" esc, sent)
				work = rep(work, module, "")
				work = rep(work, esc, "")
				work = rep(work, sent, "://" module)
			}

			# Author-profile URL, legal when it is the bare profile:
			# not followed by "/" (a repository) or a word character.
			prof = "https://github.com/steveyegge"
			s = work
			out = ""
			while ((i = index(s, prof)) > 0) {
				nxt = substr(s, i + length(prof), 1)
				out = out substr(s, 1, i - 1)
				# "/" or a word character continues the path into a
				# repository reference; anything else ends the profile URL.
				if (nxt == "/" || nxt ~ /[A-Za-z0-9_]/)
					out = out prof
				s = substr(s, i + length(prof))
			}
			work = out s

			work = rep(work, "@steveyegge", "")
			work = rep(work, "steveyegge'"'"'s", "")

			if (index(work, "steveyegge") > 0)
				printf "%s:%s\n", file, line_no
		}
	' || true
)

exit "$fail"
