#!/usr/bin/env bash
set -euo pipefail

# mutago measures only changed production lines.  This keeps the required
# covered-MSI gate focused on the code a change actually owns while retaining
# full test coverage and per-test mutation attribution for those lines.

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
cd "$repo_root"

# shellcheck disable=SC1091
source ./.buildflags

diff_base="${MUTAGO_DIFF_BASE:-}"
if [[ -z "$diff_base" && -n "${GITHUB_BASE_REF:-}" ]]; then
  diff_base="origin/$GITHUB_BASE_REF"
fi
if [[ -z "$diff_base" ]] && git rev-parse --verify --quiet "origin/main^{commit}" >/dev/null; then
  diff_base=origin/main
fi
if [[ -z "$diff_base" ]]; then
  diff_base="$(git merge-base HEAD origin/main 2>/dev/null || true)"
fi
if [[ -z "$diff_base" ]] || ! git rev-parse --verify --quiet "$diff_base^{commit}" >/dev/null; then
  printf 'mutago: unable to resolve a merge-base (set MUTAGO_DIFF_BASE)\n' >&2
  exit 1
fi

changed_files="$(git diff --name-only "$diff_base"...HEAD -- '*.go')"
paths=()
while IFS= read -r path; do
  [[ -n "$path" ]] || continue
  [[ "$path" != *_test.go ]] || continue
  [[ -f "$path" ]] || continue
  paths+=("$path")
done <<<"$changed_files"

if ((${#paths[@]} == 0)); then
  echo 'mutago: no changed production Go files; skipping'
  exit 0
fi

if [[ -n "${MUTAGO_BIN:-}" ]]; then
  mutago=("$MUTAGO_BIN")
elif command -v mutago >/dev/null 2>&1; then
  mutago=("$(command -v mutago)")
else
  mutago=(go run github.com/quality-gates/mutago/v2/cmd/mutago@v2.9.1)
fi

printf 'mutago: scanning %d changed production Go file(s) against %s\n' "${#paths[@]}" "$diff_base"
"${mutago[@]}" \
  --coverage \
  --per-test \
  --git-diff-lines \
  --git-diff-base="$diff_base" \
  --min-covered-msi=80 \
  --ignore-msi-with-no-mutations \
  --quiet \
  --no-diffs \
  --workers="${MUTAGO_WORKERS:-2}" \
  --test-flags=-tags=gms_pure_go \
  "${paths[@]}"
