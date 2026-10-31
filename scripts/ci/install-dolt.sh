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
# Why pinned rather than latest: Dolt 2.3.0 (released 2026-08-13) regressed
# CALL DOLT_RESET('--hard') so that roughly one freshly created database in
# twenty comes up with the procedure permanently broken --
# "Error 1105 (HY000): context canceled" on every connection, from any
# session, for the life of the server process. Measured 2026-08-20 by
# creating fresh databases and immediately calling the procedure: 2.1.8 0/40
# broken, 2.2.0 0/60, 2.3.0 3/60, 2.3.1 3/100. That is what made
# TestFreshBootstrapHealIncarnation fail on a coin flip the day
# releases/latest moved to 2.3.x, and it also puts bd dolt compact/flatten
# and the #4566 fresh-bootstrap heal at risk on 2.3.x. Raise this pin only
# once a Dolt release is confirmed clean by that same measurement.
#
# 2.6.0 is that release. Measured 2026-10-24: 0/500 on one server and 0/1000
# across 50 fresh servers of 20 databases each, and the embedded engine
# (go.mod's dolthub/dolt/go at the v2.6.0 release) 0/100, 0/400 across 20
# fresh opens. The regression hits databases created soon after a server
# starts, so measure across fresh servers: that day 2.3.1, the positive
# control, measured 0/300 on one server but 31/600 across 30 fresh servers.
# Earlier, on one server: 2026-10-21 2.3.1 4/300, 2.4.2 0/250, 2.5.0 0/500,
# 2.5.1 0/500, embedded 2.5.1 0/100; 2026-10-10 2.4.1 0/250. The pin tracks
# that embedded engine, so the CLI, the container and embedded mode run the
# same Dolt, and doltversion.RequiredMin (2.6.0), the oldest dolt bd runs
# against outside embedded mode, must stay at or below this pin
# (TestPinnedDoltCLIAtLeastRequiredMin). Older releases also have known
# defects: 2.4.0 through 2.4.2
# return no row for an ungrouped MAX/MIN over an unmatched primary-key prefix
# (dolthub/dolt#12093); releases before 2.4.0 drop a join condition when an
# ON clause has an OR over indexed columns (dolthub/dolt#11886); releases
# before 2.3.3 cannot write to a database holding a descending index column,
# which every database bd creates in embedded mode and any database whose
# schema a dolt sql-server 2.3.3 or newer created holds (an UPDATE or DELETE
# on issues fails on fk_comp_snap_issue, other writes with "table has unknown
# fields"). docs/architecture/dolt.md, "Which Dolt version to install", has
# the dated measurements.
readonly version="2.6.0"
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
# Compare the version token exactly rather than as a substring: `*"2.6.0"*`
# also matches 12.6.0 and 2.6.0-rc1, so a substring test would wave through
# releases the pin exists to keep out.
installed="$(dolt version)"
installed="${installed%%$'\n'*}"
if [[ "${installed##* }" != "$version" ]]; then
  printf 'dolt on PATH reports "%s", want version %s (which dolt: %s)\n' \
    "$installed" "$version" "$(command -v dolt)" >&2
  exit 1
fi
printf 'Installed pinned %s\n' "$installed"
