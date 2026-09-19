#!/usr/bin/env bash
set -euo pipefail

# Keep this tag in sync with internal/testutil/testdoltcommon.go:DoltDockerImage.
readonly image="dolthub/dolt-sql-server:2.2.0"
readonly max_attempts=3
readonly retry_delay_seconds=5

for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  if docker pull "$image"; then
    exit 0
  else
    status=$?
  fi

  if ((attempt == max_attempts)); then
    printf 'Failed to pull %s after %d attempts.\n' "$image" "$max_attempts" >&2
    exit "$status"
  fi

  printf 'Failed to pull %s (attempt %d/%d); retrying in %d seconds.\n' \
    "$image" "$attempt" "$max_attempts" "$retry_delay_seconds" >&2
  sleep "$retry_delay_seconds"
done
