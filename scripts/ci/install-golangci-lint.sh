#!/usr/bin/env bash
set -euo pipefail

# Install the pinned golangci-lint release binary instead of `go install`
# (F5.2): the release tarball drops the install step from about 58s to about
# 3s, because it is a straight download-and-verify rather than a compile.
# CI-only: this script requires $RUNNER_TEMP and $GITHUB_PATH, so the local
# `.githooks/pre-commit` hook keeps using
# `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.1`,
# which needs no change here. `make ci-pr-lint` (scripts/pr-lint) looks up
# whatever `golangci-lint` binary is on PATH; it does not use `go run`.
#
# Keep this version and the sha256 values in sync with CONTRIBUTING.md,
# engdocs/LINTING.md, .github/workflows/pr.yml, .github/workflows/main.yml,
# .github/workflows/ci-measurements.yml and .githooks/pre-commit. Verified
# locally against the release's own golangci-lint-2.10.1-checksums.txt.
readonly version="2.10.1"
readonly max_attempts=3
readonly retry_delay_seconds=5

declare -A sha256_by_arch=(
  [amd64]="dfa775874cf0561b404a02a8f4481fc69b28091da95aa697259820d429b09c99"
  [arm64]="6652b42ae02915eb2f9cb2a2e0cac99514c8eded8388d88ae3e06e1a52c00de8"
)

: "${RUNNER_TEMP:?RUNNER_TEMP is required; this script is CI-only}"
: "${GITHUB_PATH:?GITHUB_PATH is required; this script is CI-only}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
if [[ "$os" != "linux" ]]; then
  printf 'install-golangci-lint.sh only supports Linux runners, got: %s\n' "$os" >&2
  exit 1
fi

arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *)
    printf 'Unsupported architecture for pinned golangci-lint install: %s\n' "$arch" >&2
    exit 1
    ;;
esac

expected_sha256="${sha256_by_arch[$arch]:?no pinned sha256 for arch: $arch}"
readonly asset="golangci-lint-${version}-${os}-${arch}.tar.gz"
readonly url="https://github.com/golangci/golangci-lint/releases/download/v${version}/${asset}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  # See install-dolt.sh for why the status capture can't live in the `if`.
  status=0
  curl -fsSL -o "$workdir/$asset" "$url" || status=$?
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

actual_sha256="$(sha256sum "$workdir/$asset" | awk '{print $1}')"
if [[ "$actual_sha256" != "$expected_sha256" ]]; then
  printf '%s sha256 mismatch: got %s, want %s\n' "$asset" "$actual_sha256" "$expected_sha256" >&2
  exit 1
fi

tar -xzf "$workdir/$asset" -C "$workdir"

install_dir="$RUNNER_TEMP/golangci-lint"
mkdir -p "$install_dir"
install -m 0755 "$workdir/golangci-lint-${version}-${os}-${arch}/golangci-lint" "$install_dir/golangci-lint"

echo "$install_dir" >> "$GITHUB_PATH"

# Fail loudly if the extracted binary does not report the pinned version: a
# bad tarball layout would otherwise silently put a stale or wrong binary on
# PATH. Compare the version token exactly, as install-dolt.sh does.
installed="$("$install_dir/golangci-lint" version)"
if [[ "$installed" != *"version $version"* ]]; then
  printf 'golangci-lint reports "%s", want version %s\n' "$installed" "$version" >&2
  exit 1
fi
printf 'Installed pinned golangci-lint %s to %s (sha256 %s)\n' "$version" "$install_dir" "$actual_sha256"
