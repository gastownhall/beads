#!/bin/bash
# Guard: winget installer manifests must set PortableCommandAlias (GH#4908).
# Parses with yq rather than grep so a corrupt/unparseable manifest (e.g. a
# multi-line InstallerSha256 from a checksum-matching bug) fails loudly
# instead of a bare substring match reporting OK.
#
# yq flavour portability: these expressions stay inside the vocabulary shared
# by mikefarah yq v4 (what ubuntu-latest ships) and python-yq/jq — identity,
# `==`, `[]`, `select`, and `-e`'s exit status. Avoid builtins the two spell
# differently, e.g. jq's `any(cond)` against mikefarah's `any_c`.
set -euo pipefail

# yq is outside the host-tools inventory declared in scripts/BUILD.bazel and no
# workflow job installs it, so name the missing tool instead of reporting every
# manifest as missing its alias.
if ! command -v yq >/dev/null 2>&1; then
  echo "FAIL: yq is required by this guard but is not installed"
  exit 1
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
shopt -s nullglob
files=("$ROOT"/winget/*.installer.yaml)
if [ "${#files[@]}" -eq 0 ]; then
  echo "FAIL: no winget/*.installer.yaml files found"
  exit 1
fi

# The placeholder scripts/update-winget.sh substitutes when a release publishes
# no windows_arm64 checksum.
ZERO_SHA256="0000000000000000000000000000000000000000000000000000000000000000"

fail=0
for f in "${files[@]}"; do
  # Parse before classifying. An unparseable manifest has to fail here rather
  # than fall through the NestedInstallerType test below and get skipped as
  # "not portable". yq's own diagnostic is left on stderr deliberately: the
  # point of this guard is that a missing tool or a parse error is
  # distinguishable from a missing alias.
  if ! yq -e '.' "$f" >/dev/null; then
    echo "FAIL: $f is not parseable YAML"
    fail=1
    continue
  fi

  # PortableCommandAlias exists only for nested portable installers, so an
  # msi/exe/msix manifest added later is outside this guard's class rather than
  # in violation of it.
  if ! yq -e '.NestedInstallerType == "portable"' "$f" >/dev/null; then
    echo "SKIP: $f is not a nested portable installer"
    continue
  fi

  if yq -e '.NestedInstallerFiles[] | select(.PortableCommandAlias == "bd")' "$f" >/dev/null; then
    echo "OK: $f has PortableCommandAlias"
  else
    echo "FAIL: $f missing valid PortableCommandAlias: bd (GH#4908)"
    fail=1
  fi

  # Placeholder-hash tripwire. Advisory, not fatal: the only occurrence in the
  # tree today is the arm64 entry of the legacy SteveYegge manifest, which this
  # guard does not otherwise gate, so failing would turn a required check red
  # over pre-existing content. grep is deliberate here — this looks for a
  # known-bad literal, so unlike the alias assertion above it can only miss a
  # bad value, never report OK on a manifest it failed to understand.
  if grep -Eq "InstallerSha256:[[:space:]]*\"?${ZERO_SHA256}\"?[[:space:]]*\$" "$f"; then
    echo "WARNING: $f carries a placeholder all-zero InstallerSha256 — do not publish that entry"
  fi
done
exit "$fail"
