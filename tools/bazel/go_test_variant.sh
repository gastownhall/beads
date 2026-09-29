#!/usr/bin/env bash
# Runs an existing go_test binary as another test target, so a package can be
# tested a second way (different env, tags, or lane) without compiling it
# again. The first argument is the binary's runfiles path ($(rootpath ...),
# relative to the workspace runfiles directory, which is the test's working
# directory); the rest, plus any --test_arg, go to the binary unchanged.
#
# Sharding, XML output and timeouts pass through (the binary reads Bazel's
# TEST_* environment). What does NOT pass through is the go_test's own
# RunEnvironmentInfo: rules_go sets GO_TEST_RUN_FROM_BAZEL=1, which this script
# exports so the binary still changes into its package directory, and the
# go_test's `env` attribute, which the variant target must repeat in its own
# `env` (with the referenced targets in its `data`).
set -euo pipefail
bin="$1"
shift
export GO_TEST_RUN_FROM_BAZEL=1
exec "./$bin" "$@"
