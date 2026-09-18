#!/bin/bash
# Guard: winget installer manifests must set PortableCommandAlias (GH#4908).
# Parses with yq rather than grep so a corrupt/unparseable manifest (e.g. a
# multi-line InstallerSha256 from a checksum-matching bug) fails loudly
# instead of a bare substring match reporting OK.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
shopt -s nullglob
files=("$ROOT"/winget/*.installer.yaml)
if [ "${#files[@]}" -eq 0 ]; then
  echo "FAIL: no winget/*.installer.yaml files found"
  exit 1
fi
fail=0
for f in "${files[@]}"; do
  if yq -e '.NestedInstallerFiles[] | select(.PortableCommandAlias == "bd")' "$f" >/dev/null 2>&1; then
    echo "OK: $f has PortableCommandAlias"
  else
    echo "FAIL: $f missing valid PortableCommandAlias: bd (GH#4908)"
    fail=1
  fi
done
exit "$fail"
