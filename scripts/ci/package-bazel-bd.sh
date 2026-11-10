#!/usr/bin/env bash
# Packages the Bazel-built bd in pr.yml's ci-build-artifacts layout
# (bd-linux-gms-pure, SHA256SUMS, build-manifest.txt) for bazel.yml.
#
# The binary is //cmd/bd:bd_for_tests, the non-race gms_pure_go cgo bd that
# Bazel's own tests exec (//cmd/bd:bd is race built under --config=ci); run
# after `bazel test //... --config=ci` (whose test:ci downloads it) or after
# `bazel build //cmd/bd:bd_for_tests` directly (bazel.yml's package gates:
# only test:ci is defined in .bazelrc, so `bazel build --config=ci` fails).
# Either way the same bd_for_tests output path is what this script packages.
# Unlike `go build`, it carries no vcs.* or CGO_ENABLED build settings, so
# `bd version` shows no commit and scripts/verify-cgo.sh would pass
# vacuously; no consumer of the artifact reads either.
#
# Usage: scripts/ci/package-bazel-bd.sh OUT_DIR
set -euo pipefail

out="${1:?usage: $0 OUT_DIR}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"
# For BEADS_BUILD_TAGS (the manifest's build_tags, as in pr.yml).
# shellcheck source=/dev/null
source ./.buildflags

mapfile -t found < <(find -H bazel-out -path '*/bin/cmd/bd/bd_for_tests/bd' -type f)
if [ "${#found[@]}" -ne 1 ]; then
  echo "::error::want exactly one //cmd/bd:bd_for_tests output, found ${#found[@]}: ${found[*]}"
  exit 1
fi

mkdir -p "$out"
install -m 755 "${found[0]}" "$out/bd-linux-gms-pure"
cd "$out"
./bd-linux-gms-pure version
sha256sum bd-linux-gms-pure > SHA256SUMS
{
  echo "commit=$(git -C "$repo_root" rev-parse HEAD)"
  echo "go_version=$(go version bd-linux-gms-pure | sed 's/^[^:]*: //')"
  echo "build_tags=${BEADS_BUILD_TAGS}"
  echo "artifact=bd-linux-gms-pure"
  echo "builder=bazel //cmd/bd:bd_for_tests"
} > build-manifest.txt
