#!/usr/bin/env bash
# Installs PACKAGEs into the image's preinstalled MSYS2 (C:\msys64) for
# windows-make-shell's MSYS2 leg (pr.yml), and seeds the package cache of
# main.yml's windows-make-shell-toolchains with exactly the files that install
# needs. Run it under setup-msys2's `msys2` shell:
#
#   MSYS2_PACKAGE_CACHE='C:\...\make-shell\msys2' msys2-packages.sh PACKAGE...
#
# MSYS2_PACKAGE_CACHE is the package cache directory (a Windows path; pr.yml
# restores it from main's cache first). MSYS2_PACKAGE_CACHE_HIT=true makes a
# download a warning: an exact cache hit should leave nothing to fetch.
#
# pacman resolves the packages against the image's own sync database. Nothing
# here refreshes that database or upgrades the base system: either would mix
# packages newer than the image (and than the cache) into its old base, a
# partial upgrade. Then
#   1. pacman -Sw downloads only what the cache directory lacks: up to
#      $attempts attempts of at most $attempt_seconds each, backing off
#      between them (nothing to do on a full cache hit);
#   2. pacman -S installs from the cache directory alone.
# pacman checks each package's signature against the image's keyring before
# it installs it. The cache directory may come from actions/cache, so the
# guard below fails unless pacman.conf makes that check mandatory.
set -euo pipefail

attempts=3
attempt_seconds=120
backoff_seconds=(15 30)

if (($# == 0)); then
    echo "usage: MSYS2_PACKAGE_CACHE=<dir> $0 PACKAGE..." >&2
    exit 2
fi
: "${MSYS2_PACKAGE_CACHE:?MSYS2_PACKAGE_CACHE must name the package cache directory}"
cache_dir="$(cygpath -u "$MSYS2_PACKAGE_CACHE")"

# The [msys] repository's effective SigLevel: its own line, else the global
# one (pacman-conf prints nothing for a repository that inherits it).
siglevel="$(pacman-conf --repo=msys SigLevel)"
if [[ -z "$siglevel" ]]; then
    siglevel="$(pacman-conf SigLevel)"
fi
if ! grep -qx PackageRequired <<<"$siglevel" || ! grep -qx PackageTrustedOnly <<<"$siglevel"; then
    echo "::error title=MSYS2 packages::pacman would install unsigned or untrusted msys packages (SigLevel: $(tr '\n' ' ' <<<"$siglevel")); refusing to install from a restored cache."
    exit 1
fi

db_path="$(pacman-conf DBPath)"
db_path="${db_path%/}/"
if [[ ! -s "${db_path}sync/msys.db" ]]; then
    echo "::error title=MSYS2 packages::The image's MSYS2 has no msys sync database (${db_path}sync/msys.db); this script never refreshes it."
    exit 1
fi

mkdir -p "$cache_dir"
count_packages() {
    find "$cache_dir" -maxdepth 1 -type f -name '*.pkg.tar.*' ! -name '*.sig' ! -name '*.part' | wc -l
}
cached="$(count_packages)"

for ((attempt = 1; ; attempt++)); do
    if timeout --kill-after=10 "$attempt_seconds" \
        pacman --noconfirm --noprogressbar -Sw --needed --cachedir "$cache_dir" "$@"; then
        break
    fi
    # A killed pacman leaves its lock behind; no other pacman runs in this job.
    rm -f "${db_path}db.lck"
    if ((attempt == attempts)); then
        echo "::error title=MSYS2 packages::Downloading $* failed $attempts times."
        exit 1
    fi
    delay="${backoff_seconds[attempt - 1]}"
    echo "::warning title=MSYS2 packages::Downloading $*, attempt $attempt of $attempts, failed; retrying in ${delay}s."
    sleep "$delay"
done

# A killed attempt can leave a partial download; never install or save one.
find "$cache_dir" -maxdepth 1 -type f -name '*.part' -delete
downloaded=$(($(count_packages) - cached))
echo "MSYS2 packages: $cached in the cache before, $downloaded downloaded."
if [[ "${MSYS2_PACKAGE_CACHE_HIT:-}" == true ]] && ((downloaded > 0)); then
    echo "::warning title=MSYS2 packages::The main-saved cache matched this image's sync database exactly but lacked $downloaded package file(s)."
fi

pacman --noconfirm --noprogressbar -S --needed --overwrite '*' --cachedir "$cache_dir" "$@"
for package in "$@"; do
    pacman -Q "$package"
done
