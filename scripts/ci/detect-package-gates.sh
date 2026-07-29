#!/usr/bin/env bash
# Decide which package gates apply to the current CI event.

set -euo pipefail

event_name="${GITHUB_EVENT_NAME:-}"
pr_base_sha="${PR_BASE_SHA:-}"
pr_head_sha="${PR_HEAD_SHA:-}"
push_before_sha="${PUSH_BEFORE_SHA:-}"
push_after_sha="${PUSH_AFTER_SHA:-${GITHUB_SHA:-HEAD}}"

mcp_package=false
npm_package=false
reason=""
changed_files=""

write_outputs() {
    if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
        {
            echo "mcp_package=$mcp_package"
            echo "npm_package=$npm_package"
            echo "reason=$reason"
        } >>"$GITHUB_OUTPUT"
    fi
}

run_all() {
    reason="$1"
    mcp_package=true
    npm_package=true
    write_outputs
    echo "$reason"
    exit 0
}

if [[ "${CI_PACKAGE_GATES_FORCE:-}" == "all" ]]; then
    run_all "CI_PACKAGE_GATES_FORCE=all"
fi

case "$event_name" in
    pull_request)
        if [[ -z "$pr_base_sha" || -z "$pr_head_sha" ]]; then
            run_all "PR diff bounds unavailable; running all package gates"
        fi
        if ! changed_files="$(git diff --name-only "$pr_base_sha" "$pr_head_sha")"; then
            run_all "PR diff failed; running all package gates"
        fi
        ;;
    push)
        if [[ -z "$push_before_sha" || "$push_before_sha" =~ ^0+$ ]]; then
            if ! changed_files="$(git diff-tree --no-commit-id --name-only -r "$push_after_sha")"; then
                run_all "push diff unavailable; running all package gates"
            fi
        elif ! changed_files="$(git diff --name-only "$push_before_sha" "$push_after_sha")"; then
            run_all "push diff failed; running all package gates"
        fi
        ;;
    merge_group)
        if git rev-parse --verify --quiet HEAD^ >/dev/null; then
            if ! changed_files="$(git diff --name-only HEAD^ HEAD)"; then
                run_all "merge-group diff failed; running all package gates"
            fi
        else
            run_all "merge-group parent unavailable; running all package gates"
        fi
        ;;
    *)
        run_all "non-PR/push/merge-group event; running all package gates"
        ;;
esac

while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    case "$path" in
        integrations/beads-mcp/*|scripts/ci/package-mcp.sh|scripts/ci/detect-package-gates.sh|.github/workflows/pr.yml|.github/workflows/main.yml|.github/workflows/pr-risk.yml|.github/workflows/ci-measurements.yml|.github/workflows/release.yml|Makefile)
            mcp_package=true
            ;;
    esac
    case "$path" in
        npm-package/*|scripts/ci/package-npm.sh|scripts/ci/detect-package-gates.sh|.github/workflows/pr.yml|.github/workflows/main.yml|.github/workflows/pr-risk.yml|.github/workflows/ci-measurements.yml|.github/workflows/release.yml|Makefile)
            npm_package=true
            ;;
    esac
    case "$path" in
        scripts/ci/detect-package-gates.sh|.github/workflows/pr.yml|.github/workflows/main.yml|.github/workflows/pr-risk.yml|.github/workflows/ci-measurements.yml|Makefile)
            mcp_package=true
            npm_package=true
            ;;
    esac
done <<<"$changed_files"

if [[ "$mcp_package" == "true" || "$npm_package" == "true" ]]; then
    reason="package paths changed"
else
    reason="no package-gate paths changed"
fi

write_outputs

echo "$reason"
echo "Changed files:"
if [[ -n "$changed_files" ]]; then
    while IFS= read -r path; do
        printf '  %s\n' "$path"
    done <<<"$changed_files"
else
    echo "  <none>"
fi
echo "Package gates:"
echo "  mcp_package=$mcp_package"
echo "  npm_package=$npm_package"
