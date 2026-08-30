#!/usr/bin/env bash
set -euo pipefail

# messgo is intentionally scoped to production Go in the change.  The
# repository's baseline contains legacy findings, so a whole-tree required
# check would make unrelated changes impossible to merge.  A file touched by
# this change is scanned in full; tests are excluded by design.

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
cd "$repo_root"

# Keep the tool's Go subprocesses on the repository's pure-Go build path.
# shellcheck disable=SC1091
source ./.buildflags

diff_base="${MESSGO_DIFF_BASE:-}"
if [[ -n "$diff_base" ]]; then
  if ! git rev-parse --verify --quiet "$diff_base^{commit}" >/dev/null; then
    printf 'messgo: merge-base %q is not available locally\n' "$diff_base" >&2
    exit 1
  fi
  changed_files="$(git diff --name-only "$diff_base"...HEAD -- '*.go')"
else
  changed_files="$(git ls-files -- '*.go')"
fi

paths=()
while IFS= read -r path; do
  [[ -n "$path" ]] || continue
  [[ "$path" != *_test.go ]] || continue
  [[ -f "$path" ]] || continue
  paths+=("$path")
done <<<"$changed_files"

if ((${#paths[@]} == 0)); then
  echo 'messgo: no production Go files in scope; skipping'
  exit 0
fi

path_list=""
for path in "${paths[@]}"; do
  path_list+="${path_list:+,}$path"
done

if [[ -n "${MESSGO_BIN:-}" ]]; then
  messgo=("$MESSGO_BIN")
elif command -v messgo >/dev/null 2>&1; then
  messgo=("$(command -v messgo)")
else
  messgo=(go run github.com/quality-gates/messgo/cmd/messgo@v0.2.2)
fi

printf 'messgo: scanning %d production Go file(s)\n' "${#paths[@]}"
"${messgo[@]}" "$path_list" github 'unusedcode,design,codesize' --ignore-tests
