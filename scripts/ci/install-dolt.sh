#!/usr/bin/env bash
set -euo pipefail

# Install a pinned Dolt CLI. Every CI job that needs `dolt` on PATH must go
# through this script instead of piping the upstream install.sh from
# releases/latest, because "latest" silently changes the binary under test.
#
# Keep this version in sync with internal/testutil/testdoltcommon.go:
# DoltDockerImage, scripts/ci/pull-dolt-image.sh and tools/bazel/dolt.bzl
# (DOLT_VERSION plus its per-platform sha256). Tests run the CLI (the
# per-test sql-server that doltserver.Start launches) and the container
# side by side against the same databases; letting the two drift means the
# suite is exercising a Dolt pair no release ever shipped.
#
# Why pinned rather than latest: Dolt 2.2.4 through 2.3.1
# can break CALL DOLT_RESET('--hard') (dolthub/dolt#11581) so that some
# freshly created databases come up with the procedure permanently broken --
# "Error 1105 (HY000): context canceled" on every connection, from any
# session, for the life of the server process. Measured 2026-08-20 by
# creating fresh databases and immediately calling the procedure: 2.1.8 0/40
# broken, 2.2.0 0/60, 2.3.0 3/60, 2.3.1 3/100. That is what made
# TestFreshBootstrapHealIncarnation fail on a coin flip the day
# releases/latest moved to 2.3.x, and it also puts bd dolt compact/flatten
# and the #4566 fresh-bootstrap heal at risk on 2.2.4 through 2.3.1.
# Upstream fixed it in 2.3.2 (dolthub/dolt#11652); 2.3.4 also fixes a
# related journal-writer race (dolthub/dolt#11796). Re-measured 2026-09-29
# with one client and with 8 parallel ones, each statement on a new
# connection (method in docs/architecture/dolt.md): 2.2.0 0/400 broken,
# 2.3.1 110/400, 2.3.5 0/400, plus 0/640 with 16 clients; with 8 clients
# only, 2.2.1 0/200, 2.2.3 0/200, 2.2.4 74/200, 2.3.4 0/200. Raise this pin
# only to 2.3.4 or newer, and only to a release confirmed clean by that
# same measurement.
readonly version="2.2.0"
readonly max_attempts=3
readonly retry_delay_seconds=5

arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *)
    printf 'Unsupported architecture for pinned dolt install: %s\n' "$arch" >&2
    exit 1
    ;;
esac

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
readonly asset="dolt-${os}-${arch}.tar.gz"
readonly url="https://github.com/dolthub/dolt/releases/download/v${version}/${asset}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  # Capture curl's status directly. `status=$?` after an `if curl ...; then
  # break; fi` reads the status of the *if compound*, which is 0 when the
  # condition fails and there is no else branch -- so a download that never
  # succeeded exited 0 and the job carried on with whatever dolt was already
  # on PATH, which is exactly the unpinned binary this script exists to
  # prevent.
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

tar -xzf "$workdir/$asset" -C "$workdir"
sudo install -m 0755 "$workdir/dolt-${os}-${arch}/bin/dolt" /usr/local/bin/dolt

# Fail loudly if PATH resolves to some other dolt: a stale runner-image copy
# earlier on PATH would silently put the suite back on an unpinned binary.
# No `| head` here: pipefail turns dolt's SIGPIPE into a failed install.
# Compare the version token exactly rather than as a substring: `*"2.2.0"*`
# also matches 12.2.0 and 2.2.0-rc1, so a substring test would wave through
# releases the pin exists to keep out.
installed="$(dolt version)"
installed="${installed%%$'\n'*}"
if [[ "${installed##* }" != "$version" ]]; then
  printf 'dolt on PATH reports "%s", want version %s (which dolt: %s)\n' \
    "$installed" "$version" "$(command -v dolt)" >&2
  exit 1
fi
printf 'Installed pinned %s\n' "$installed"
