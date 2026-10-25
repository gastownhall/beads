#!/usr/bin/env bash
# One previous release of the upgrade smoke tests (formerly
# cross-version-smoke.yml's `scripts/upgrade-smoke-test.sh` loop): that
# release → the candidate, with both binaries pinned inputs, so the script
# downloads and builds nothing.
#
# The first argument is the release. CANDIDATE_BIN and PREV_BIN are
# runfiles-relative paths ($(rootpath ...), relative to the working directory
# Bazel starts the test in), made absolute here before the script changes
# directory. Any further arguments (a lane's --test_arg flags, meant for Go
# test binaries) are ignored.
set -euo pipefail
release="$1"
CANDIDATE_BIN="$PWD/${CANDIDATE_BIN:?}"
PREV_BIN="$PWD/${PREV_BIN:?}"
export CANDIDATE_BIN PREV_BIN
exec scripts/upgrade-smoke-test.sh "$release"
