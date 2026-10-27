#!/usr/bin/env bash
set -euo pipefail

# Install the pinned Android NDK (r30, 30.0.16248370) that the
# bd-android-arm64 goreleaser build compiles through
# (scripts/ci/android-arm64-cc / -cxx), and print the single line
#   BEADS_ANDROID_NDK_HOME=<root>
# (also appended to $GITHUB_ENV when set) for the wrappers -- or any later
# workflow step -- to consume. All progress and errors go to stderr so the
# stdout line is unambiguous to `export "$(scripts/ci/install-android-ndk.sh)"`.
#
# Pinning, retry bounds and the status-capture shape follow
# scripts/ci/install-dolt.sh: an unpinned "latest" NDK silently changes the
# toolchain under a release, and a retry loop whose last attempt fails must
# not exit 0. The pin matters specifically because NDK releases before r28
# (the runner image default is r27.3) link 4 KB-aligned ELFs, which Android
# 15 devices with 16 KB pages cannot load; scripts/verify-android-elf.sh
# fails such binaries at build time.
#
# Usage: install-android-ndk.sh [dest-dir]   (default: $RUNNER_TEMP)
readonly revision="30.0.16248370"
readonly zip_name="android-ndk-r30-linux.zip"
readonly url="https://dl.google.com/android/repository/android-ndk-r30-linux.zip"
# SHA-256 of android-ndk-r30-linux.zip (Google's SHA-1 for it is
# 5107f898313790e449e87eee2183d9a20602dee9).
readonly sha256="753611f410d002cfcd3f3dc2ef49aad532089d3180b436c060a90bf0fcb64df2"
readonly max_attempts=3
readonly retry_delay_seconds=5
readonly prebuilt_clang="toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android21-clang"

# The real NDK writes "Pkg.Revision = <rev>" (spaces included); accept the
# spaced and unspaced forms, and nothing looser: this is the exact-revision
# gate both image reuse and post-extract verification turn on.
ndk_revision() {
  sed -n 's/^Pkg.Revision *= *//p' "$1" 2>/dev/null | tr -d '\r' || true
}

print_root() {
  printf 'BEADS_ANDROID_NDK_HOME=%s\n' "$1"
}

os="$(uname -s)"
arch="$(uname -m)"
if [[ "$os" != Linux || "$arch" != x86_64 ]]; then
  # bd-android-arm64's CC wrappers run this script from goreleaser; naming
  # the build keeps a macOS snapshot failure obvious.
  printf 'bd-android-arm64: the android CGO build is Linux x86_64 only (uname reports %s %s); %s holds a linux-x86_64 NDK.\n' "$os" "$arch" "$zip_name" >&2
  exit 1
fi

dest="${1:-${RUNNER_TEMP:-}}"
if [[ -z "$dest" ]]; then
  printf 'usage: %s [dest-dir]   (or set RUNNER_TEMP)\n' "$0" >&2
  exit 1
fi
mkdir -p "$dest"

# Fast path: the ubuntu-24.04 runner image ships an NDK under
# $ANDROID_HOME/ndk. Trust it only when source.properties names the pinned
# revision exactly and the API-21 clang wrapper is executable -- any other
# revision (the image default r27.3 included) falls through to the download.
if [[ -n "${ANDROID_HOME:-}" ]]; then
  candidate="$ANDROID_HOME/ndk/$revision"
  if [[ -x "$candidate/$prebuilt_clang" ]] && [[ "$(ndk_revision "$candidate/source.properties")" == "$revision" ]]; then
    printf 'Using image NDK r%s at %s\n' "$revision" "$candidate" >&2
    print_root "$candidate"
    [[ -z "${GITHUB_ENV:-}" ]] || print_root "$candidate" >>"$GITHUB_ENV"
    exit 0
  fi
fi

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
zip_path="$workdir/$zip_name"

for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  # Capture curl's status directly: `status=$?` after `if curl ...` reads
  # the status of the if compound (0 when the condition fails with no else
  # branch), so an exhausted loop could exit 0 -- see install-dolt.sh.
  status=0
  curl -fsSL --connect-timeout 30 --max-time 240 -o "$zip_path" "$url" || status=$?
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

if ! (cd "$workdir" && printf '%s  %s\n' "$sha256" "$zip_name" | sha256sum -c -); then
  printf 'SHA-256 of %s does not match the pinned %s; not installing it.\n' "$zip_name" "$sha256" >&2
  exit 1
fi

unzip -q "$zip_path" -d "$dest"

root="$dest/android-ndk-r30"
extracted="$(ndk_revision "$root/source.properties")"
if [[ "$extracted" != "$revision" ]]; then
  printf 'Extracted NDK reports Pkg.Revision %q, want exactly %s.\n' "$extracted" "$revision" >&2
  exit 1
fi
if [[ ! -x "$root/$prebuilt_clang" ]]; then
  printf 'Extracted NDK has no executable aarch64-linux-android21-clang at %s.\n' "$root/$prebuilt_clang" >&2
  exit 1
fi

printf 'Installed Android NDK r%s at %s\n' "$revision" "$root" >&2
print_root "$root"
[[ -z "${GITHUB_ENV:-}" ]] || print_root "$root" >>"$GITHUB_ENV"
