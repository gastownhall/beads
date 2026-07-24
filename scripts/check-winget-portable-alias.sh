#!/bin/bash
# Guard: winget installer manifests must set PortableCommandAlias (GH#4908).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
fail=0
for f in "$ROOT"/winget/*.installer.yaml; do
  [ -f "$f" ] || continue
  if ! grep -q 'PortableCommandAlias:' "$f"; then
    echo "FAIL: $f missing PortableCommandAlias (GH#4908)"
    fail=1
  else
    echo "OK: $f has PortableCommandAlias"
  fi
done
exit "$fail"
