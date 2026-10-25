#!/usr/bin/env bash
# The scope of a PR or PR Risk run: which of the workflow's job sets this
# event runs (engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md, "Merge Queue"). Both
# workflows' scope jobs run `decide`, so they decide identically; every other
# job reads the scope job's outputs, and both gates read them back through
# `result` and `skips`. Nothing else looks at the event.
#
#   ci-scope.sh decide   GITHUB_EVENT_NAME -> the scope job's outputs on
#                        $GITHUB_OUTPUT:
#                                        platforms  dolt-tiers  linux  advisory
#                          pull_request  true       on          true   true
#                          merge_group   false      off         true   false
#                          push          true       on          false  false
#                        A pull request runs every job. A merge group runs
#                        the fast set: the Linux jobs and every Bazel lane but
#                        the Dolt tiers (no Windows, macOS or Nix job). A push
#                        to main runs the complement: the Windows, macOS and
#                        Nix jobs, while bazel.yml's own push run is every
#                        Bazel lane, so pr.yml calls none. Every job the gates
#                        read thus runs on every push head on main: the queue
#                        lands a batch as one push of its last entry, whose
#                        merge group ran the fast set on the same tree (which
#                        contains every entry of the batch), and the push
#                        runs run the rest; advisory-reports, read by
#                        neither gate, runs on pull requests only, and a
#                        direct push has no merge group, as before. Any other
#                        event fails the job, and so both gates. Rollback:
#                        give merge_group pull_request's row (advisory
#                        aside).
#   ci-scope.sh result   the gate's SCOPE: the scope job's result
#                        (SCOPE_RESULT) when its outputs (SCOPE_PLATFORMS,
#                        SCOPE_DOLT_TIERS, SCOPE_LINUX, SCOPE_ADVISORY) are
#                        exactly one event's row above, else
#                        invalid-scope:<outputs>, which ci-gate.sh rejects.
#   ci-scope.sh skips    the gate ids the scope turned off, for
#                        CI_GATE_SKIPPED_OK: the gate's CI_SCOPE_PLATFORMS_IDS
#                        when platforms is false and CI_SCOPE_LINUX_IDS when
#                        linux is false; none unless the outputs are a row.
#                        The Dolt-tier lanes are bazel-gate.sh's to excuse:
#                        that also needs the Bazel call to report it ran
#                        without them.

set -euo pipefail

events=(pull_request merge_group push)

# row EVENT: the scope outputs for EVENT; nothing for any other event.
row() {
    case "$1" in
        pull_request) echo "platforms=true dolt-tiers=on linux=true advisory=true" ;;
        merge_group) echo "platforms=false dolt-tiers=off linux=true advisory=false" ;;
        push) echo "platforms=true dolt-tiers=on linux=false advisory=false" ;;
    esac
}

# given: the scope job's outputs as the gate received them, in row form.
given() {
    printf 'platforms=%s dolt-tiers=%s linux=%s advisory=%s' \
        "${SCOPE_PLATFORMS:-}" "${SCOPE_DOLT_TIERS:-}" "${SCOPE_LINUX:-}" "${SCOPE_ADVISORY:-}"
}

# is_row: the outputs are exactly one event's row.
is_row() {
    local event
    for event in "${events[@]}"; do
        if [[ "$(given)" == "$(row "$event")" ]]; then
            return 0
        fi
    done
    return 1
}

case "${1:-}" in
    decide)
        event="${GITHUB_EVENT_NAME:-}"
        scope="$(row "$event")"
        if [[ -z "$scope" ]]; then
            echo "::error::ci-scope.sh: no scope for event '$event'; PR and PR Risk run on pull_request, merge_group and push to main only"
            exit 1
        fi
        tr ' ' '\n' <<<"$scope" >>"$GITHUB_OUTPUT"
        echo "Scope for $event: $scope" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
        ;;
    result)
        if [[ "${SCOPE_RESULT:-}" != success ]]; then
            echo "${SCOPE_RESULT:-}"
        elif is_row; then
            echo success
        else
            echo "invalid-scope:$(given | tr ' ' ',' | tr -cd 'A-Za-z0-9=,-')"
        fi
        ;;
    skips)
        skips=()
        if is_row; then
            if [[ "$SCOPE_PLATFORMS" == false ]]; then
                read -ra ids <<<"${CI_SCOPE_PLATFORMS_IDS:-}"
                skips+=("${ids[@]}")
            fi
            if [[ "$SCOPE_LINUX" == false ]]; then
                read -ra ids <<<"${CI_SCOPE_LINUX_IDS:-}"
                skips+=("${ids[@]}")
            fi
        fi
        echo "${skips[*]-}"
        ;;
    *)
        echo "usage: $0 decide|result|skips" >&2
        exit 2
        ;;
esac
