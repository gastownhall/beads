#!/usr/bin/env bash
# Loading and analysis of every bazel.yml lane, for the remote repo contents
# cache (bazel.yml's rrc-seed and rrc-verify jobs).
#
# Usage: rrc-run-lanes.sh [STARTUP_FLAG...] -- [COMMAND_FLAG...]
#
# Runs `bazel STARTUP_FLAG... <line> --nobuild COMMAND_FLAG...` for each
# command line in .github/scripts/rrc-lane-commands.txt (or RRC_LANE_COMMANDS),
# in order, stopping at the first failure. --nobuild: repository rules,
# loading and analysis only, never a build or test action.
#
# Bazel exits 1 after `test --nobuild` even when loading and analysis succeed
# ("Couldn't start the build. Unable to run tests"). A command passes when it
# exits 0, or exits non-zero having printed "Build completed successfully"
# and no ERROR line but that one.
set -euo pipefail

startup=()
while [ $# -gt 0 ] && [ "$1" != -- ]; do
	startup+=("$1")
	shift
done
[ $# -gt 0 ] || {
	echo "usage: rrc-run-lanes.sh [STARTUP_FLAG...] -- [COMMAND_FLAG...]" >&2
	exit 2
}
shift
flags=("$@")

file=${RRC_LANE_COMMANDS:-$(dirname "${BASH_SOURCE[0]}")/rrc-lane-commands.txt}
mapfile -t cmds < <(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$file" | grep -v '^[[:space:]]*$' || true)
if [ "${#cmds[@]}" -eq 0 ]; then
	echo "::error::no lane commands in $file"
	exit 1
fi

log=$(mktemp)
trap 'rm -f "$log"' EXIT
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
for cmd in "${cmds[@]}"; do
	read -r -a args <<<"$cmd"
	start=$(date +%s)
	rc=0
	bazel ${startup[@]+"${startup[@]}"} "${args[@]}" --nobuild ${flags[@]+"${flags[@]}"} </dev/null 2>&1 | tee "$log" || rc=$?
	if [ "$rc" -ne 0 ] && grep -q '^INFO: Build completed successfully' "$log" &&
		! grep '^ERROR:' "$log" | grep -qv "^ERROR: Couldn't start the build. Unable to run tests$"; then
		rc=0
	fi
	echo "rrc: \`bazel $cmd\`: exit $rc, $(($(date +%s) - start))s" | tee -a "$summary"
	[ "$rc" -eq 0 ] || exit "$rc"
done
