#!/usr/bin/env bash
# Local preview for the Mintlify docs site under docs/.
# Usage: ./mint.sh [dev|broken-links|...]   (defaults to dev)
set -euo pipefail

repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
docs_dir="$repo_dir/docs"
# Pinned so local preview and CI parse the same tool (keep in sync with
# .github/scripts/docs-render-check.sh).
MINT_VERSION="${MINT_VERSION:-4.2.687}"
args=("$@")
if [[ ${#args[@]} -eq 0 ]]; then
  args=(dev)
fi

node_major() {
  node -p 'process.versions.node.split(".")[0]' 2>/dev/null || true
}

find_node22_bin() {
  local prefix
  for prefix in /opt/homebrew/opt/node@22 /usr/local/opt/node@22; do
    if [[ -x "$prefix/bin/node" ]]; then
      printf '%s/bin\n' "$prefix"
      return 0
    fi
  done

  if command -v brew >/dev/null 2>&1; then
    prefix=$(brew --prefix node@22 2>/dev/null || true)
    if [[ -n "$prefix" && -x "$prefix/bin/node" ]]; then
      printf '%s/bin\n' "$prefix"
      return 0
    fi
  fi

  return 1
}

major=$(node_major)
if [[ "$major" =~ ^[0-9]+$ ]] && (( major < 25 )); then
  cd "$docs_dir"
  exec npx --yes "mint@$MINT_VERSION" "${args[@]}"
fi

if node22_bin=$(find_node22_bin); then
  export PATH="$node22_bin:$PATH"
  cd "$docs_dir"
  exec npx --yes "mint@$MINT_VERSION" "${args[@]}"
fi

cat >&2 <<EOF
Mintlify does not support Node 25+.

Use Node 22 LTS to preview the docs. For example:
  nvm use 22
  fnm use 22
  volta install node@22

On macOS with Homebrew:
  brew install node@22
  cd "$repo_dir"
  PATH="/opt/homebrew/opt/node@22/bin:\$PATH" ./mint.sh dev
EOF
exit 1
