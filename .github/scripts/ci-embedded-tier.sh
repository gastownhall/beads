#!/usr/bin/env bash
# Decide whether CI should run the full embedded Dolt build/storage/cmd matrix.

set -euo pipefail

event_name="${GITHUB_EVENT_NAME:-}"
base_sha="${PR_BASE_SHA:-}"
head_sha="${PR_HEAD_SHA:-}"

full_embedded=false
reason=""
changed_files=""

write_outputs() {
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
      echo "full_embedded=$full_embedded"
      echo "reason=$reason"
    } >> "$GITHUB_OUTPUT"
  fi
}

case "$event_name" in
  push)
    full_embedded=true
    reason="push to main runs full embedded Dolt coverage"
    write_outputs
    echo "$reason"
    exit 0
    ;;
  merge_group)
    full_embedded=true
    reason="merge queue runs full embedded Dolt coverage"
    write_outputs
    echo "$reason"
    exit 0
    ;;
esac

if [ "$event_name" != "pull_request" ]; then
  full_embedded=true
  reason="non-PR event runs full embedded Dolt coverage"
  write_outputs
  echo "$reason"
  exit 0
fi

if [ -z "$base_sha" ] || [ -z "$head_sha" ]; then
  full_embedded=true
  reason="PR diff bounds unavailable; defaulting to full embedded Dolt coverage"
  write_outputs
  echo "$reason"
  exit 0
fi

if ! changed_files="$(git diff --name-only "$base_sha" "$head_sha")"; then
  full_embedded=true
  reason="PR diff failed; defaulting to full embedded Dolt coverage"
  write_outputs
  echo "$reason"
  exit 0
fi

if [ -z "$changed_files" ]; then
  reason="no changed files detected; skipping embedded Dolt matrix"
  write_outputs
  echo "$reason"
  exit 0
fi

while IFS= read -r path; do
  case "$path" in
    cmd/*|internal/*|tests/*|scripts/*|.github/scripts/*|.github/workflows/*)
      full_embedded=true
      reason="changed $path"
      break
      ;;
    *.go|go.mod|go.sum|Makefile|default.nix|flake.nix|flake.lock|packages.nix)
      full_embedded=true
      reason="changed $path"
      break
      ;;
    AGENTS.md|AGENT_INSTRUCTIONS.md|CONTRIBUTING.md|RELEASING.md)
      full_embedded=true
      reason="changed $path"
      break
      ;;
  esac
done <<< "$changed_files"

if [ "$full_embedded" = true ]; then
  reason="risk path detected: $reason; running full embedded Dolt coverage"
else
  reason="docs/metadata-only PR; skipping embedded Dolt storage/cmd matrix"
fi

write_outputs

echo "$reason"
echo "Changed files:"
while IFS= read -r path; do
  printf '  %s\n' "$path"
done <<< "$changed_files"
