#!/usr/bin/env bash
# Ensure testing.Short() is only used for true runtime/stress/large-fixture skips.

set -euo pipefail

allowed=$(
  cat <<'EOF'
internal/hooks/hooks_test.go::TestRunSync_Timeout
internal/hooks/hooks_test.go::TestRunSync_KillsDescendants
internal/testutil/fixtures/fixtures_test.go::TestXLargeDolt
internal/testutil/fixtures/fixtures_test.go::TestLargeFromJSONL
internal/storage/dolt/concurrent_test.go::TestHighContentionStress
internal/storage/dolt/concurrent_test.go::TestConcurrentWorkQueueDrain
internal/storage/dolt/lease_test.go::TestConcurrentHeartbeatReclaimClose
cmd/bd/prune_bench_test.go::TestPruneLargeFixture
EOF
)

status=0

while IFS=: read -r file line _; do
  file="${file#./}"
  if [[ -z "$file" || -z "$line" ]]; then
    continue
  fi

  func=$(
    awk -v target="$line" '
      NR <= target && /^func [A-Za-z0-9_]+\(/ {
        current = $0
        sub(/^func /, "", current)
        sub(/\(.*/, "", current)
      }
      END { print current }
    ' "$file"
  )
  key="${file}::${func}"

  if ! grep -Fxq "$key" <<<"$allowed"; then
    printf 'Disallowed testing.Short() at %s:%s in %s\n' "$file" "$line" "${func:-unknown}" >&2
    status=1
  fi
done < <(find . -type f -name '*.go' -not -path './.git/*' -exec grep -n 'testing\.Short()' {} + || true)

if (( status != 0 )); then
  cat >&2 <<'EOF'

testing.Short() is reserved for true runtime, stress, or large-fixture skips.
Use build tags, environment checks, or named wrappers for integration/e2e/API
boundaries instead.
EOF
  exit "$status"
fi

printf 'testing.Short() usage is limited to approved runtime/stress/large-fixture skips.\n'
