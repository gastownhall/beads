#!/usr/bin/env bash
set -euo pipefail

# Install the pinned Android NDK that the bd-android-arm64 release build
# (.goreleaser.yml) cross-compiles cgo with (#3538), and print its root as one
# stdout line, BEADS_ANDROID_NDK_HOME=<root>; progress goes to stderr. When
# $GITHUB_ENV is set the same line is appended to it, on success only.
#
# Usage: install-android-ndk.sh [dest-dir]   (default: $RUNNER_TEMP)
#
# scripts/ci/android-arm64-cc and -cxx run this when BEADS_ANDROID_NDK_HOME is
# unset, so it is called once per cgo compile and possibly concurrently: an
# NDK already in dest-dir is reused, and a download is extracted beside it and
# renamed into place, so a racing call never sees a half-extracted tree.
# Locally: export "$(scripts/ci/install-android-ndk.sh "$HOME/.cache/beads-ndk")"
#
# The runner image's ANDROID_NDK_HOME (NDK 27.3) is never read: it links 4 KB
# PT_LOAD alignment, which 16 KB-page Android devices refuse. An NDK of exactly
# the pinned revision under $ANDROID_HOME/ndk (ubuntu-24.04 ships one) is used
# instead of downloading. The zip and its toolchain are linux-x86_64, so the
# android cgo build is Linux x86_64 only.
readonly revision="30.0.16248370"
readonly release="r30"
readonly url="https://dl.google.com/android/repository/android-ndk-${release}-linux.zip"
readonly sha256="753611f410d002cfcd3f3dc2ef49aad532089d3180b436c060a90bf0fcb64df2"
readonly clang_rel="toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android21-clang"
readonly max_attempts=3
readonly retry_delay_seconds=5

# ndk_ok ROOT: ROOT is an NDK of exactly the pinned revision with an
# executable API-21 clang. Exact match: 30.0.162483701 or 130.0.16248370 is
# some other NDK.
ndk_ok() {
  local root="$1" key value found=""
  [[ -f "$root/source.properties" && -x "$root/$clang_rel" && -x "$root/$clang_rel++" ]] || return 1
  while IFS='=' read -r key value; do
    key="${key//[[:space:]]/}"
    value="${value//[[:space:]]/}"
    if [[ "$key" == "Pkg.Revision" ]]; then
      found="$value"
    fi
  done <"$root/source.properties"
  [[ "$found" == "$revision" ]]
}

succeed() {
  local line="BEADS_ANDROID_NDK_HOME=$1"
  if [[ -n "${GITHUB_ENV:-}" ]]; then
    printf '%s\n' "$line" >>"$GITHUB_ENV"
  fi
  printf '%s\n' "$line"
  exit 0
}

host="$(uname -s)/$(uname -m)"
if [[ "$host" != "Linux/x86_64" ]]; then
  printf 'The android CGO build (goreleaser id bd-android-arm64) is Linux x86_64 only: NDK %s is installed from its linux-x86_64 zip, and this host is %s. Omit --id bd-android-arm64 here and build it on Linux.\n' \
    "$revision" "$host" >&2
  exit 1
fi

dest="${1:-${RUNNER_TEMP:-}}"
if [[ $# -gt 1 || -z "$dest" ]]; then
  printf 'Usage: %s [dest-dir]   (dest-dir defaults to $RUNNER_TEMP, which is unset)\n' "$0" >&2
  exit 2
fi

if [[ -n "${ANDROID_HOME:-}" ]] && ndk_ok "$ANDROID_HOME/ndk/$revision"; then
  printf 'Using preinstalled Android NDK %s at %s\n' "$revision" "$ANDROID_HOME/ndk/$revision" >&2
  succeed "$ANDROID_HOME/ndk/$revision"
fi

mkdir -p "$dest"
dest="$(cd "$dest" && pwd)"
root="$dest/android-ndk-$release"
if ndk_ok "$root"; then
  printf 'Using Android NDK %s already installed at %s\n' "$revision" "$root" >&2
  succeed "$root"
fi

workdir="$(mktemp -d "$dest/.android-ndk-$release.XXXXXX")"
trap 'rm -rf "$workdir"' EXIT
zip="$workdir/android-ndk-$release-linux.zip"

for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  # Capture curl's status directly (see install-dolt.sh): reading $? after an
  # if-compound reports 0 when every attempt failed.
  status=0
  curl -fsSL --connect-timeout 30 --max-time 240 -o "$zip" "$url" || status=$?
  if ((status == 0)); then
    break
  fi
  if ((attempt == max_attempts)); then
    printf 'Failed to download %s after %d attempts (curl exit %d).\n' "$url" "$max_attempts" "$status" >&2
    exit "$status"
  fi
  printf 'Failed to download %s (attempt %d/%d); retrying in %d seconds.\n' \
    "$url" "$attempt" "$max_attempts" "$retry_delay_seconds" >&2
  sleep "$retry_delay_seconds"
done

actual="$(sha256sum "$zip")"
actual="${actual%% *}"
if [[ "$actual" != "$sha256" ]]; then
  printf '%s has SHA-256 %s, want %s.\n' "$url" "$actual" "$sha256" >&2
  exit 1
fi

unzip -q "$zip" -d "$workdir"
if ! ndk_ok "$workdir/android-ndk-$release"; then
  printf '%s did not extract an NDK of revision %s with an executable %s.\n' "$url" "$revision" "$clang_rel" >&2
  exit 1
fi

# -T: never move into an existing directory. If a concurrent call won the
# rename, use its tree.
if ! mv -T "$workdir/android-ndk-$release" "$root" 2>/dev/null && ! ndk_ok "$root"; then
  printf 'Could not install Android NDK %s at %s.\n' "$revision" "$root" >&2
  exit 1
fi
printf 'Installed Android NDK %s at %s\n' "$revision" "$root" >&2
succeed "$root"
