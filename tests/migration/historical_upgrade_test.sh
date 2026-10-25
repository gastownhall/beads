#!/usr/bin/env bash
# One release of the historical-upgrade corpus (formerly migration-test.yml's
# `scripts/migration-test/run.sh --version <release>` loop), as a Bazel test:
# scripts/migration-test/historical-dolt-upgrade-test.sh against the
# candidate, with every historical binary it runs pre-pinned in
# HISTORICAL_RELEASE_DIR (lib/binary.sh downloads nothing then), the
# source-built v0.9.1 as SOURCE_TAG_SQLITE_BIN for that release, and, for the
# releases that need it, the pinned external Dolt runtime as DOLT_BIN.
#
# The first argument is the release. The BUILD file passes the paths as
# runfiles-relative paths ($(rootpath ...), relative to the working
# directory Bazel starts the test in); they are made absolute here, before
# the harness changes directory. Any further arguments (a lane's
# --test_arg flags, meant for Go test binaries) are ignored.
set -euo pipefail
release="$1"
abs() { printf '%s/%s\n' "$PWD" "$1"; }
CANDIDATE_BIN="$(abs "${CANDIDATE_BIN:?}")"
HISTORICAL_RELEASE_DIR="$(abs "${HISTORICAL_RELEASE_DIR:?}")"
export CANDIDATE_BIN HISTORICAL_RELEASE_DIR
if [[ -n "${DOLT_BIN:-}" ]]; then
	DOLT_BIN="$(abs "$DOLT_BIN")"
	export DOLT_BIN
fi
if [[ -n "${SOURCE_TAG_SQLITE_BIN:-}" ]]; then
	SOURCE_TAG_SQLITE_BIN="$(abs "$SOURCE_TAG_SQLITE_BIN")"
	export SOURCE_TAG_SQLITE_BIN
fi
exec scripts/migration-test/historical-dolt-upgrade-test.sh --version "$release"
