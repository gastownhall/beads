#!/usr/bin/env bash
# bazel-sync-patch.sh - capture the drift `make bazel-sync` left in the
# checkout as an apply-ready patch for the bazel-autofix workflow.
#
# Usage: scripts/ci/bazel-sync-patch.sh <out-dir>
#
# Writes <out-dir>/bazel-sync.patch (`git diff --binary`, new files included)
# restricted to the paths the privileged consumer accepts, plus
# <out-dir>/bazel-sync-meta.txt (PR number and head SHA from PR_NUMBER and
# PR_HEAD_SHA when set). Drift outside the allowlist (third_party/patches,
# go.mod, ...) is listed but never patched: a human has to look at it.
#
# The allowlist mirrors path_allowed() in scripts/bazel-autofix-push.sh, which
# re-validates everything; that copy runs from the base branch and is the one
# that counts (scripts/ci_workflow_test.go keeps the two regexes identical).
# The working tree and the real index are left as they are.

set -euo pipefail
export LC_ALL=C

out="${1:?usage: bazel-sync-patch.sh <out-dir>}"
mkdir -p "$out"
patch="$out/bazel-sync.patch"
meta="$out/bazel-sync-meta.txt"

BUILD_FILE_RE='^([A-Za-z0-9_+-][A-Za-z0-9_.+-]*/)*BUILD\.bazel$'

path_allowed() {
    case "$1" in
        MODULE.bazel | MODULE.bazel.lock) return 0 ;;
        third_party/*) return 1 ;;
    esac
    [[ "$1" =~ $BUILD_FILE_RE ]]
}

# Stage everything in a throwaway index so new BUILD files (untracked) land
# in the diff without touching the checkout's own index.
index="$(mktemp)"
trap 'rm -f "$index"' EXIT
cp "$(git rev-parse --git-path index)" "$index"
GIT_INDEX_FILE="$index" git add -A -- .

allowed=()
rejected=()
while IFS= read -r path; do
    [ -n "$path" ] || continue
    if path_allowed "$path"; then
        allowed+=("$path")
    else
        rejected+=("$path")
    fi
done < <(GIT_INDEX_FILE="$index" git -c core.quotePath=true diff --cached --no-renames --name-only)

if [ "${#rejected[@]}" -gt 0 ]; then
    echo "Drift outside the autofix allowlist (fix by hand, not patched):"
    printf '  %s\n' "${rejected[@]}"
fi

if [ "${#allowed[@]}" -eq 0 ]; then
    echo "No auto-fixable BUILD/MODULE drift; no patch written."
    rm -f "$patch" "$meta"
    exit 0
fi

GIT_INDEX_FILE="$index" git diff --cached --binary --no-renames -- "${allowed[@]}" > "$patch"
{
    echo "pr=${PR_NUMBER:-}"
    echo "head_sha=${PR_HEAD_SHA:-}"
    echo "checkout_sha=$(git rev-parse HEAD)"
} > "$meta"

echo "Wrote $patch (${#allowed[@]} file(s)):"
printf '  %s\n' "${allowed[@]}"
