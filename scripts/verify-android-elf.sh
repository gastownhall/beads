#!/usr/bin/env bash
#
# verify-android-elf.sh - Fail unless a built binary is an Android (Bionic)
# ELF that 16 KB-page devices can load.
# Usage: ./scripts/verify-android-elf.sh <path-to-binary>
#
# verify-cgo.sh only rejects CGO_ENABLED=0 and libicu, so it would accept an
# android/arm64 binary linked by the host's glibc toolchain. This checks what
# makes the bd-android-arm64 release run on Termux: the program interpreter is
# Bionic's /system/bin/linker64, and every PT_LOAD segment is aligned to at
# least 16 KB (0x4000), which Android 15+ devices with 16 KB pages require.
#
# Unlike verify-cgo.sh, a missing readelf is an error rather than a skip: a
# skip would pass an unchecked binary (a macOS host without binutils) as a
# verified Android ELF.

set -euo pipefail

readonly want_interpreter="/system/bin/linker64"
readonly min_load_alignment=$((0x4000))

if [[ $# -ne 1 ]]; then
    echo "Usage: $0 <path-to-binary>" >&2
    exit 2
fi

binary_path="$1"

if ! command -v readelf >/dev/null 2>&1; then
    echo "ERROR: 'readelf' is required to verify the Android ELF headers of $binary_path" >&2
    exit 1
fi

if [[ ! -f "$binary_path" ]]; then
    echo "ERROR: binary not found: $binary_path" >&2
    exit 1
fi

status=0
headers="$(readelf -lW "$binary_path")" || status=$?
if ((status != 0)); then
    echo "ERROR: readelf -lW could not read $binary_path (exit $status)" >&2
    exit 1
fi

interpreter=""
loads=0
while read -r type rest; do
    if [[ "$type" == "[Requesting" && "$rest" =~ ^program\ interpreter:\ (.*)\]$ ]]; then
        interpreter="${BASH_REMATCH[1]}"
        continue
    fi
    [[ "$type" == "LOAD" ]] || continue
    loads=$((loads + 1))
    # Align is the last column; Flg can hold spaces ("R E").
    align="${rest##* }"
    if [[ ! "$align" =~ ^0x[0-9a-fA-F]+$ ]]; then
        echo "ERROR: $binary_path has a PT_LOAD segment with unreadable alignment '$align'" >&2
        exit 1
    fi
    if ((align < min_load_alignment)); then
        echo "ERROR: $binary_path has a PT_LOAD segment aligned to $align, want at least 0x4000 (16 KB pages)" >&2
        exit 1
    fi
done <<<"$headers"

if [[ "$interpreter" != "$want_interpreter" ]]; then
    echo "ERROR: $binary_path has program interpreter '${interpreter:-none}', want $want_interpreter (Android Bionic)" >&2
    exit 1
fi

if ((loads == 0)); then
    echo "ERROR: $binary_path has no PT_LOAD segment alignment to check" >&2
    exit 1
fi

echo "OK: $binary_path is an Android ELF (interpreter $want_interpreter, PT_LOAD alignment >= 0x4000)"
