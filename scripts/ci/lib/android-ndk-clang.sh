#!/usr/bin/env bash
# The body of scripts/ci/android-arm64-cc and android-arm64-cxx, the CC and
# CXX of .goreleaser.yml's bd-android-arm64 build (#3538): exec the pinned
# Android NDK's API-21 clang (or clang++) with the compiler's original
# arguments.
#
# The NDK is $BEADS_ANDROID_NDK_HOME when that tree has the compiler;
# otherwise scripts/ci/install-android-ndk.sh finds or installs it (in
# $RUNNER_TEMP, else ~/.cache/beads-ndk) and prints its root. ANDROID_NDK_HOME
# is deliberately not consulted: on the runner image it is NDK 27, whose 4 KB
# PT_LOAD alignment 16 KB-page devices refuse. The NDK's bin directory is
# never put on PATH either: it holds a bare `ld` that would shadow binutils.

android_ndk_exec_clang() {
    local suffix="$1"
    shift
    local here host root line log name
    name="$(basename "$0")"
    here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
    local rel="toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android21-clang$suffix"

    host="$(uname -s)/$(uname -m)"
    if [[ "$host" != "Linux/x86_64" ]]; then
        printf '%s: the android CGO build (goreleaser id bd-android-arm64) is Linux x86_64 only, and this host is %s. Omit --id bd-android-arm64 here and build it on Linux.\n' \
            "$name" "$host" >&2
        exit 1
    fi

    root="${BEADS_ANDROID_NDK_HOME:-}"
    if [[ -z "$root" || ! -x "$root/$rel" ]]; then
        # The installer's progress is shown only when it fails: Go reads a
        # compiler's stderr when it probes flag support. GITHUB_ENV is
        # cleared so each compile does not append another export line.
        log="$(mktemp)"
        if ! line="$(GITHUB_ENV='' "$here/install-android-ndk.sh" "${RUNNER_TEMP:-${HOME:?}/.cache/beads-ndk}" 2>"$log")"; then
            cat "$log" >&2
            rm -f "$log"
            exit 1
        fi
        rm -f "$log"
        if [[ "$line" != BEADS_ANDROID_NDK_HOME=* ]]; then
            printf '%s: install-android-ndk.sh printed %q, want BEADS_ANDROID_NDK_HOME=<root>\n' "$name" "$line" >&2
            exit 1
        fi
        root="${line#BEADS_ANDROID_NDK_HOME=}"
    fi

    exec "$root/$rel" "$@"
}
