#!/usr/bin/env bash
# bd-pfe2c4 / GH#6323: run the beads_test binary as a non-root account whose
# passwd home is /var/lib/flow-estate (a docker container with a fake
# /etc/passwd), since the host user's home cannot be relocated in-process.
#
# Adapted from the triage repro: the approved design's carve-out admits the
# account-database home only when it is an existing directory owned by the
# account — ownership is the check a forged $HOME cannot pass — so the
# container first creates /var/lib/flow-estate as that account's own
# directory, which is also the reporter's real layout.
set -euo pipefail
bazel build //internal/beads:beads_test >&2
bin=$(bazel cquery //internal/beads:beads_test --output=files 2>/dev/null | head -1)
bin="$(bazel info execution_root 2>/dev/null)/$bin"
[ -x "$bin" ] || { echo "test binary not found: $bin" >&2; exit 2; }
d=$(mktemp -d)
trap 'rm -rf "$d"' EXIT
printf 'flow-estate:x:4242:4242::/var/lib/flow-estate:/bin/sh\n' >"$d/passwd"
printf 'flow-estate:x:4242:\n' >"$d/group"
# The account must be able to read its own passwd/group entries; a restrictive
# host umask would otherwise leave them 0640.
chmod 0644 "$d/passwd" "$d/group"
cp "$bin" "$d/beads_test"
chmod 0755 "$d" "$d/beads_test"
docker run --rm \
  -v "$d/passwd:/etc/passwd:ro" -v "$d/group:/etc/group:ro" \
  -v "$d/beads_test:/beads_test:ro" -w /tmp \
  golang:1.26-bookworm \
  sh -c 'mkdir -p /var/lib/flow-estate && chown 4242:4242 /var/lib/flow-estate && setpriv --reuid=4242 --regid=4242 --clear-groups env HOME=/var/lib/flow-estate /beads_test -test.run "^TestIsPathInSafeBoundaryAdmitsOwnPasswdHome$" -test.v'