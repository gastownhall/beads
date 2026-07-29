#!/usr/bin/env bash
set -euo pipefail

# Run the opt-in ICU regex path tests.
# This is intentionally NOT the normal validation path for beads. The shipped
# and CI-tested configuration uses -tags gms_pure_go instead.

echo "WARNING: scripts/test-icu-path.sh intentionally exercises the ICU-only regex path." >&2
echo "WARNING: This is maintainer-only and not part of normal validation; use ./scripts/test.sh or make test for the shipped path." >&2

export CGO_ENABLED=1

if [[ "$(uname)" == "Darwin" ]]; then
  ICU_PREFIX="$(brew --prefix icu4c 2>/dev/null || true)"
  if [[ -z "$ICU_PREFIX" ]]; then
    echo "ERROR: Homebrew icu4c not found." >&2
    echo "Install it with: brew install icu4c" >&2
    exit 1
  fi

  export CGO_CFLAGS="${CGO_CFLAGS:+$CGO_CFLAGS }-I${ICU_PREFIX}/include"
  export CGO_CPPFLAGS="${CGO_CPPFLAGS:+$CGO_CPPFLAGS }-I${ICU_PREFIX}/include"
  export CGO_LDFLAGS="${CGO_LDFLAGS:+$CGO_LDFLAGS }-L${ICU_PREFIX}/lib -Wl,-rpath,${ICU_PREFIX}/lib"
fi

if [[ $# -eq 0 ]]; then
  set -- ./...
fi

echo "Running ICU regex path tests: go test $*" >&2
go test "$@"
