#!/usr/bin/env bash
# Cross-compile the Windows test/bd binaries listed in
# scripts/ci/windows-test-binaries.txt on a Linux runner, so the native
# Windows CI jobs that consume them (today: the "-prebuilt" advisory twins of
# test-windows-liveness and worktree-remove-windows) no longer pay for a CGO
# compile inside windows-latest itself.
#
# Usage: build-windows-test-binaries.sh OUT_DIR
#
# For every non-comment, non-blank line "name cgo tags package" in the
# manifest:
#   - name ending in ".test.exe" -> `go test -c` (a standalone test binary)
#   - anything else              -> `go build` (a regular executable)
#
# cgo=1 entries cross-compile with the same mingw-w64 toolchain release.yml's
# goreleaser job uses for the shipped bd-windows-amd64 binary
# (CC=x86_64-w64-mingw32-gcc, CXX=x86_64-w64-mingw32-g++; see .goreleaser.yml).
# cgo=0 entries build with CGO_ENABLED=0 and need no C toolchain.
#
# Writes OUT_DIR/SHA256SUMS so the Windows consumer jobs can verify the
# artifact after download (`sha256sum -c`).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MANIFEST="$REPO_ROOT/scripts/ci/windows-test-binaries.txt"

if [[ $# -ne 1 ]]; then
    echo "usage: $0 OUT_DIR" >&2
    exit 2
fi
OUT_DIR="$1"
mkdir -p "$OUT_DIR"

if [[ ! -f "$MANIFEST" ]]; then
    echo "manifest not found: $MANIFEST" >&2
    exit 1
fi

echo "== host go toolchain ==" >&2
go env GOVERSION GOHOSTOS GOHOSTARCH >&2

if command -v x86_64-w64-mingw32-gcc >/dev/null 2>&1; then
    echo "== mingw-w64 cross-compiler ==" >&2
    x86_64-w64-mingw32-gcc --version | head -n1 >&2
fi

built=0
while IFS= read -r line; do
    # Strip comments and blank lines.
    line="${line%%#*}"
    line="$(echo "$line" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
    [[ -z "$line" ]] && continue

    read -r name cgo tags package <<<"$line"
    if [[ -z "$name" || -z "$cgo" || -z "$tags" || -z "$package" ]]; then
        echo "malformed manifest line: $line" >&2
        exit 1
    fi

    out="$OUT_DIR/$name"
    echo "== building $name (cgo=$cgo tags=$tags package=$package) ==" >&2

    env_args=(GOOS=windows GOARCH=amd64 CGO_ENABLED="$cgo")
    if [[ "$cgo" == "1" ]]; then
        env_args+=(CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++)
    fi

    # Print the resolved toolchain for this entry so the advisory-phase log
    # confirms native-vs-cross CGO parity (F4.8 item 3), without failing the
    # build if `go env` ever rejects one of the override vars up front.
    env "${env_args[@]}" go env CGO_ENABLED CC >&2 || true

    start=$(date +%s)
    if [[ "$name" == *.test.exe ]]; then
        env "${env_args[@]}" go test -c -tags "$tags" -o "$out" "$package"
    else
        env "${env_args[@]}" go build -tags "$tags" -o "$out" "$package"
    fi
    elapsed=$(( $(date +%s) - start ))
    size=$(stat -c%s "$out" 2>/dev/null || stat -f%z "$out")
    echo "== built $name in ${elapsed}s (${size} bytes) ==" >&2

    built=$((built + 1))
done <"$MANIFEST"

if [[ "$built" -eq 0 ]]; then
    echo "manifest $MANIFEST produced zero binaries" >&2
    exit 1
fi

(
    cd "$OUT_DIR"
    sha256sum -- * >SHA256SUMS
)
echo "wrote $OUT_DIR/SHA256SUMS ($built binaries)" >&2
