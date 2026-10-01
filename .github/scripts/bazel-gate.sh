#!/usr/bin/env bash
# pr.yml's ci-gate view of its bazel.yml call (the `bazel` job). bazel.yml's
# rbe job decides the execution mode once and exports it as the call's
# rbe-mode / rbe-enabled outputs; this script reads those outputs and never
# re-derives the decision (repo variable, fork, secrets) itself.
#
#   bazel-gate.sh skips      the Bazel gate ids whose job is skipped by design
#                            in this mode, for CI_GATE_SKIPPED_OK:
#                              skip:   every lane and the aggregate (BAZEL)
#                              local:  the remote-only BAZEL_EMBEDDED,
#                                      BAZEL_INTEGRATION, BAZEL_PROXIED,
#                                      BAZEL_SERVER_STORAGE
#                              remote: none
#   bazel-gate.sh aggregate  the value to gate on for BAZEL: the call's
#                            aggregate result, or, when the mode is missing
#                            or invalid (the rbe job failed, the call never
#                            started), an unexpected value that ci-gate.sh
#                            rejects.
#
# Any other skip, a failure, a cancellation, or a lane that should run but
# reported nothing (pr.yml maps an empty output to skipped) fails the gate.
#
# Inputs (environment): BAZEL_RBE_MODE, BAZEL_RBE_ENABLED (the call's
# rbe-mode / rbe-enabled outputs), BAZEL_CALL (needs.bazel.result).

set -euo pipefail

mode="${BAZEL_RBE_MODE:-}"
enabled="${BAZEL_RBE_ENABLED:-}"
valid=false
case "$mode/$enabled" in
    remote/true | local/false | skip/false) valid=true ;;
esac

case "${1:-}" in
    skips)
        skips=()
        if [[ "$valid" == true ]]; then
            case "$mode" in
                skip) skips+=(BAZEL BAZEL_TEST BAZEL_PURE BAZEL_EMBEDDED BAZEL_INTEGRATION BAZEL_DOLTSERVER BAZEL_PROXIED BAZEL_SERVER_STORAGE) ;;
                local) skips+=(BAZEL_EMBEDDED BAZEL_INTEGRATION BAZEL_PROXIED BAZEL_SERVER_STORAGE) ;;
            esac
        fi
        echo "${skips[*]-}"
        ;;
    aggregate)
        if [[ "$valid" != true ]]; then
            echo "invalid-rbe-mode:$(printf '%s/%s' "$mode" "$enabled" | tr -cd 'a-z/')"
        else
            echo "${BAZEL_CALL:-}"
        fi
        ;;
    *)
        echo "usage: $0 skips|aggregate" >&2
        exit 2
        ;;
esac
