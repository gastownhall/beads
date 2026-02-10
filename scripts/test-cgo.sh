#!/usr/bin/env bash
set -euo pipefail

# Run full CGO-enabled tests with platform-specific prerequisites.
# Use this instead of raw `CGO_ENABLED=1 go test ...` on macOS.

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

echo "Running CGO tests: go test $*" >&2
go test "$@"
