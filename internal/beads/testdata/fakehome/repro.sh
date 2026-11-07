#!/usr/bin/env bash
# bd-77hbfy repro: run TestIsPathInSafeBoundaryAdmitsVarLibServiceHome with
# the account home faked to /var/lib/flow-estate via an LD_PRELOAD shim.
# Run from the repository root.
set -euo pipefail
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
gcc -shared -fPIC -o "$out/fakehome.so" internal/beads/testdata/fakehome/fakehome.c -ldl
bazel build //internal/beads:beads_test >/dev/null 2>&1
bin="$(bazel cquery --output=files //internal/beads:beads_test 2>/dev/null | head -1)"
cd "$(dirname "$bin")"
LD_PRELOAD="$out/fakehome.so" BD_REPRO_FAKE_HOME=/var/lib/flow-estate \
	"$OLDPWD/$bin" -test.run '^TestIsPathInSafeBoundaryAdmitsVarLibServiceHome$' -test.v
