#!/usr/bin/env bash
#
# verify-android-elf.sh - Fail unless a binary is a Bionic-linked Android
# ELF: the PT_INTERP must request /system/bin/linker64 and every PT_LOAD
# must be aligned for 16 KB pages (>= 0x4000), the Android 15 requirement.
# Runs after verify-cgo.sh on the bd-android-arm64 build; verify-cgo.sh
# only rejects CGO_ENABLED=0 and libicu, so it would wave through a
# host-gcc glibc binary or an NDK-27 4 KB-aligned link. This script
# would not.
# Usage: ./scripts/verify-android-elf.sh <path-to-binary>

set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "Usage: $0 <path-to-binary>" >&2
    exit 2
fi

binary_path="$1"

if [[ ! -f "$binary_path" ]]; then
    echo "ERROR: binary not found: $binary_path" >&2
    exit 1
fi

# Fail closed on a missing readelf. verify-cgo.sh silently skips its
# readelf half when binutils is absent; here that would turn a macOS
# snapshot with no binutils into a "verified" Android ELF.
if ! command -v readelf >/dev/null 2>&1; then
    echo "ERROR: 'readelf' is required to verify the Android ELF layout (install binutils)" >&2
    exit 1
fi

headers="$(readelf -lW "$binary_path" 2>/dev/null || true)"

# readelf prints the interpreter on its own bracketed line beneath the
# INTERP row, wide mode included:
#   [Requesting program interpreter: /system/bin/linker64]
interp="$(printf '%s\n' "$headers" | sed -n 's/^.*Requesting program interpreter: \(.*\)]$/\1/p')"
if [[ "$interp" != "/system/bin/linker64" ]]; then
    echo "ERROR: $binary_path interpreter is \"${interp:-<none>}\", want /system/bin/linker64 (Bionic); a glibc toolchain or CGO-less build produced this binary" >&2
    exit 1
fi

# Align is the last field of each LOAD row; the flags column before it can
# itself hold a space ("R E"), so the row's final field is the only safe
# position. Every PT_LOAD must clear 0x4000 for 16 KB-page devices.
bad_alignment=""
load_rows=0
while read -r align; do
    load_rows=$((load_rows + 1))
    if (( align < 0x4000 )); then
        bad_alignment="$align"
        break
    fi
done < <(printf '%s\n' "$headers" | awk '$1 == "LOAD" { print $NF }')
if (( load_rows == 0 )); then
    echo "ERROR: $binary_path has no PT_LOAD segments; cannot verify 16 KB alignment" >&2
    exit 1
fi
if [[ -n "$bad_alignment" ]]; then
    echo "ERROR: $binary_path PT_LOAD alignment $bad_alignment is below 0x4000 (16 KB pages); an NDK older than r28 linked this binary" >&2
    exit 1
fi

echo "OK: $binary_path is a Bionic Android ELF (linker64, >= 0x4000 PT_LOAD alignment)"
