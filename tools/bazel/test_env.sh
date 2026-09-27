#!/usr/bin/env bash
# Hermetic environment for every `bazel test` action (wired in .bazelrc with
# `test --run_under=//tools/bazel:test_env`). It mirrors what
# scripts/ci/lib/test-env.sh does for `go test` in pr-core: a private HOME,
# XDG config, Dolt root and empty global gitconfig, so no test reads or writes
# the developer's or the worker's real configuration.
#
# Everything lives in one fresh mktemp directory under /tmp, unique per test
# process: HOME, config and TMPDIR must be short (t.TempDir() paths hold unix
# sockets, whose sun_path limit is 104 bytes, and Bazel's TEST_TMPDIR is deep
# under the output base) and must not be shared between concurrent runs of the
# same target. The directory is removed when the test exits. Only the fixed
# wrapper text enters the action key; no host path or client env does.
set -euo pipefail

root="$(mktemp -d /tmp/bbt.XXXXXX)"
# Cleanup must never change the test's exit status: a child the test left
# running (a detached bd or a Dolt server shutting down) can still be writing
# when rm runs, and Bazel only reaps it after this wrapper exits.
trap 'chmod -R u+w "$root" 2>/dev/null || true; rm -rf "$root" 2>/dev/null || true' EXIT

mkdir -p "$root/home" "$root/xdg-config" "$root/dolt-root" "$root/tmp"
: >"$root/gitconfig"
# Dolt identity, as beads_test_env_enter sets with `dolt config --global`:
# tests that shell out to dolt commit need an author. Written directly so the
# wrapper does not depend on a dolt binary.
mkdir -p "$root/dolt-root/.dolt"
printf '%s\n' '{"user.email":"test@beads.local","user.name":"beads-test"}' \
	>"$root/dolt-root/.dolt/config_global.json"

export HOME="$root/home"
export USERPROFILE="$root/home"
export XDG_CONFIG_HOME="$root/xdg-config"
export DOLT_ROOT_PATH="$root/dolt-root"
export GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL="$root/gitconfig"
export TMPDIR="$root/tmp"
export BEADS_TEST_IGNORE_REPO_CONFIG=1

# Same scrub as beads_test_env_enter; `--test_env=NAME` on a command line must
# not be able to point a test at a live workspace or Dolt server.
unset BEADS_DIR BEADS_DB BD_DB BD_JSON BD_NO_DB BD_NO_DAEMON BD_ACTOR \
	BEADS_ACTOR GT_ROOT BEADS_DOLT_SHARED_SERVER BEADS_DOLT_SERVER_MODE \
	BEADS_DOLT_AUTO_START BEADS_DOLT_SERVER_HOST BEADS_DOLT_SERVER_PORT \
	BEADS_DOLT_PORT BEADS_DOLT_SERVER_DATABASE BEADS_DOLT_SERVER_SOCKET \
	BEADS_DOLT_PASSWORD BEADS_TEST_REPO_ROOT BEADS_DOLT_BIN

# Hermetic host tools: the pinned Dolt CLI (tools/bazel/dolt.bzl) goes first on
# PATH, so no test runs whatever dolt the executor happens to have, or skips
# because it has none. The directory comes from this wrapper's own runfiles,
# which Bazel merges into every test's runfiles, so it is always declared;
# missing it means broken wiring, and every test fails rather than skipping.
hermetic_bin=""
runfiles="${RUNFILES_DIR:-${TEST_SRCDIR:-}}"
rel="${TEST_WORKSPACE:-_main}/tools/bazel/hermetic_bin"
if [[ -n "$runfiles" && -x "$runfiles/$rel/dolt" ]]; then
	hermetic_bin="$runfiles/$rel"
elif [[ -n "${RUNFILES_MANIFEST_FILE:-}" && -f "$RUNFILES_MANIFEST_FILE" ]]; then
	dolt_path="$(awk -v k="$rel/dolt" '$1 == k { print $2; exit }' "$RUNFILES_MANIFEST_FILE")"
	if [[ -n "$dolt_path" && -x "$dolt_path" ]]; then
		hermetic_bin="${dolt_path%/*}"
	fi
fi
if [[ -z "$hermetic_bin" ]]; then
	printf 'test_env: hermetic dolt not found at %s in the runfiles of this test (//tools/bazel:hermetic_bin)\n' "$rel/dolt" >&2
	exit 1
fi
export PATH="$hermetic_bin:${PATH:-/bin:/usr/bin}"
export BEADS_TEST_DOLT_BINARY="$hermetic_bin/dolt"

# Not exec: the trap must run to remove $root. The exit status is the test's.
status=0
"$@" || status=$?
exit "$status"
