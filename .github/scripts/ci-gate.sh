#!/usr/bin/env bash
# Evaluate an aggregate GitHub Actions gate from needs.*.result values.

set -euo pipefail

gate_name="${CI_GATE_NAME:-CI Gate}"
required_vars="${CI_GATE_REQUIRED:-}"
skipped_ok_vars=" ${CI_GATE_SKIPPED_OK:-} "
failed=0

is_skipped_ok() {
    local var="$1"
    [[ "$skipped_ok_vars" == *" $var "* ]]
}

report_failure() {
    local message="$1"
    echo "::error::$message"
    failed=1
}

if [[ -z "$required_vars" ]]; then
    report_failure "CI_GATE_REQUIRED is empty for $gate_name"
fi

for var in $required_vars; do
    result="${!var:-}"
    case "$result" in
        success)
            echo "ok: $var=$result"
            ;;
        skipped)
            if is_skipped_ok "$var"; then
                echo "ok: $var=$result (allowed)"
            else
                report_failure "$var was skipped"
            fi
            ;;
        failure|cancelled)
            report_failure "$var=$result"
            ;;
        "")
            report_failure "$var is unset"
            ;;
        *)
            report_failure "$var has unexpected result: $result"
            ;;
    esac
done

if [[ "$failed" -ne 0 ]]; then
    echo "$gate_name failed"
    exit 1
fi

echo "$gate_name passed"
