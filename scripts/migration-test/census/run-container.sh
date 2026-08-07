#!/usr/bin/env bash
set -euo pipefail

# Runs two independent exhaustive censuses in the pinned Dockerfile environment.
# Each census has its own persistent cache and process state; neither run seeds
# nor copies data to the other. Bind-mounted evidence is deliberately retained
# when a generator, comparison, or verification fails.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
readonly PROJECT_ROOT
readonly IMAGE_TAG="beads-census:local"
readonly CACHE_ROOT="${CENSUS_CACHE:-${TMPDIR:-/tmp}/beads-schema-census-cache}"
readonly OUTPUT_DIR="${CENSUS_OUTPUT:-${TMPDIR:-/tmp}/beads-schema-census-output}"
readonly GENERATE_TIMEOUT="${CENSUS_GENERATE_TIMEOUT:-165m}"
GENERATOR_UID="$(id -u)"
readonly GENERATOR_UID
GENERATOR_GID="$(id -g)"
readonly GENERATOR_GID
CONTROL_DIR="$(mktemp -d "${TMPDIR:-/tmp}/beads-schema-census-control.XXXXXX")"
readonly CONTROL_DIR

cleanup_control() {
    rm -rf -- "$CONTROL_DIR"
}
trap cleanup_control EXIT

usage() {
    printf 'usage: %s [--init]\n' "${0##*/}" >&2
}

mode=compare
case "${1:-}" in
    '') ;;
    --init) mode=init ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 2 ;;
esac

if [ "$#" -gt 1 ]; then
    usage
    exit 2
fi

readonly CENSUS_ONE_CACHE="$CACHE_ROOT/census-1"
readonly CENSUS_TWO_CACHE="$CACHE_ROOT/census-2"
readonly CENSUS_ONE_STATE="$OUTPUT_DIR/census-1-state"
readonly CENSUS_TWO_STATE="$OUTPUT_DIR/census-2-state"
readonly CENSUS_ONE_EVIDENCE="$OUTPUT_DIR/census-1-evidence"
readonly CENSUS_TWO_EVIDENCE="$OUTPUT_DIR/census-2-evidence"
readonly CENSUS_ONE_CACHE_SAFE_MARKER="$OUTPUT_DIR/census-1-cache-safe"
readonly CENSUS_TWO_CACHE_SAFE_MARKER="$OUTPUT_DIR/census-2-cache-safe"
readonly CENSUS_ONE_DIAGNOSTIC="$OUTPUT_DIR/census-1-diagnostic.json"
readonly CENSUS_TWO_DIAGNOSTIC="$OUTPUT_DIR/census-2-diagnostic.json"
readonly INTEGRITY_MARKER="$OUTPUT_DIR/integrity-ok"

mkdir -p \
    "$CENSUS_ONE_CACHE" "$CENSUS_TWO_CACHE" \
    "$CENSUS_ONE_STATE" "$CENSUS_TWO_STATE" \
    "$CENSUS_ONE_EVIDENCE" "$CENSUS_TWO_EVIDENCE" "$OUTPUT_DIR"
rm -f \
    "$OUTPUT_DIR/census-1.json" "$OUTPUT_DIR/census-2.json" \
    "$CENSUS_ONE_DIAGNOSTIC" "$CENSUS_TWO_DIAGNOSTIC" \
    "$CENSUS_ONE_CACHE_SAFE_MARKER" "$CENSUS_TWO_CACHE_SAFE_MARKER" \
    "$INTEGRITY_MARKER"

docker build --pull --platform linux/amd64 --tag "$IMAGE_TAG" --file "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR"

docker run --rm --platform linux/amd64 \
    --mount "type=bind,src=$PROJECT_ROOT,dst=/workspace,readonly" \
    --mount "type=bind,src=$CONTROL_DIR,dst=/control" \
    --workdir /workspace \
    "$IMAGE_TAG" \
    go build -tags gms_pure_go -o /control/census ./scripts/migration-test/census

declare -A GENERATE_STATUSES PROMOTION_STATUSES CACHE_STATUSES CLEANUP_STATUSES

clear_private_state() {
    local state_dir="$1"
    docker run --rm --platform linux/amd64 \
        --mount "type=bind,src=$state_dir,dst=/state" \
        "$IMAGE_TAG" \
        find /state -mindepth 1 -delete
}

clear_lane_evidence() {
    local evidence_dir="$1"
    docker run --rm --platform linux/amd64 \
        --mount "type=bind,src=$evidence_dir,dst=/evidence" \
        "$IMAGE_TAG" \
        find /evidence -mindepth 1 -delete
}

run_census() {
    local lane="$1"
    local cache_dir="$2"
    local state_dir="$3"
    local evidence_dir="$4"
    local cache_safe_marker="$5"
    local trusted_output_name="$6"
    local diagnostic_name="$7"
    local prepare_status
    local generate_status
    local promotion_status
    local cache_status
    local cleanup_status=0

    # Clear only this lane's prior scratch and evidence. Anything written by
    # the current attempt remains inspectable if generation or validation fails.
    if clear_private_state "$state_dir" && clear_lane_evidence "$evidence_dir"; then
        prepare_status=0
    else
        prepare_status=$?
    fi
    if [ "$prepare_status" -ne 0 ]; then
        GENERATE_STATUSES["$lane"]=$prepare_status
        PROMOTION_STATUSES["$lane"]=0
        CACHE_STATUSES["$lane"]=0
        CLEANUP_STATUSES["$lane"]=0
        printf '%s private state could not be cleared before generation (status %d)\n' \
            "$lane" "$prepare_status" >&2
        return "$prepare_status"
    fi

    printf 'Running independent %s with private cache lane %s\n' "$lane" "$cache_dir"
    # The two hardened lanes peaked at 417 PIDs and about 2.2 GiB.
    # These bounds leave build headroom while keeping untrusted work finite.
    if docker run --rm --init --platform linux/amd64 \
        --user "$GENERATOR_UID:$GENERATOR_GID" \
        --read-only \
        --cap-drop ALL \
        --security-opt no-new-privileges:true \
        --pids-limit 1024 \
        --memory 8g \
        --memory-swap 8g \
        --cpus 4 \
        --tmpfs /tmp:rw,nosuid,nodev,size=4g,mode=1777 \
        --mount "type=bind,src=$PROJECT_ROOT,dst=/workspace,readonly" \
        --mount "type=bind,src=$CONTROL_DIR,dst=/control,readonly" \
        --mount "type=bind,src=$evidence_dir,dst=/evidence" \
        --mount "type=bind,src=$cache_dir,dst=/cache" \
        --mount "type=bind,src=$state_dir,dst=/state" \
        --env HOME=/state/home \
        --env TMPDIR=/state/tmp \
        --env XDG_CACHE_HOME=/state/xdg-cache \
        --env CENSUS_GENERATE_TIMEOUT="$GENERATE_TIMEOUT" \
        --workdir /workspace \
        "$IMAGE_TAG" \
        bash -ceu '
            mkdir -p "$HOME" "$TMPDIR" "$XDG_CACHE_HOME"
            exec timeout "$CENSUS_GENERATE_TIMEOUT" /control/census generate \
                scripts/migration-test/release-catalog.json /evidence/census.json /cache
        '; then
        generate_status=0
    else
        generate_status=$?
    fi

    # The historical generator has fully exited. A trusted container now reads
    # its isolated lane through a read-only mount and atomically promotes only
    # a bounded, stable regular file into the writable trusted output mount.
    if docker run --rm --platform linux/amd64 \
        --mount "type=bind,src=$PROJECT_ROOT,dst=/workspace,readonly" \
        --mount "type=bind,src=$CONTROL_DIR,dst=/control,readonly" \
        --mount "type=bind,src=$evidence_dir,dst=/evidence,readonly" \
        --mount "type=bind,src=$OUTPUT_DIR,dst=/output" \
        --workdir /workspace \
        "$IMAGE_TAG" \
        /control/census promote-evidence scripts/migration-test/release-catalog.json /evidence/census.json \
            "/output/$trusted_output_name" "/output/$diagnostic_name"; then
        promotion_status=0
    else
        promotion_status=$?
        printf '%s raw evidence failed trusted promotion with status %d\n' \
            "$lane" "$promotion_status" >&2
    fi

    if docker run --rm --platform linux/amd64 \
        --mount "type=bind,src=$PROJECT_ROOT,dst=/workspace,readonly" \
        --mount "type=bind,src=$CONTROL_DIR,dst=/control,readonly" \
        --mount "type=bind,src=$cache_dir,dst=/cache" \
        --mount "type=bind,src=$state_dir,dst=/state" \
        --env HOME=/state/home \
        --env TMPDIR=/state/tmp \
        --env XDG_CACHE_HOME=/state/xdg-cache \
        --workdir /workspace \
        "$IMAGE_TAG" \
        bash -ceu '
            mkdir -p "$HOME" "$TMPDIR" "$XDG_CACHE_HOME"
            exec /control/census cache-validate \
                scripts/migration-test/release-catalog.json /cache
        '; then
        cache_status=0
        if touch "$cache_safe_marker"; then
            printf '%s cache lane passed validation and is safe to retain\n' "$lane"
        else
            cache_status=$?
            printf '%s cache lane passed validation but its safe marker could not be written\n' "$lane" >&2
        fi
    else
        cache_status=$?
        printf '%s cache lane failed validation and will not be retained\n' "$lane" >&2
    fi

    # Historical source builds leave executable copies under HOME/TMPDIR. They
    # are generation-private scratch state, unlike the durable cache and raw
    # evidence mounts, so reclaim them once this lane has been validated.
    if clear_private_state "$state_dir"; then
        :
    else
        cleanup_status=$?
        printf '%s private state cleanup failed with status %d\n' "$lane" "$cleanup_status" >&2
    fi

    GENERATE_STATUSES["$lane"]=$generate_status
    PROMOTION_STATUSES["$lane"]=$promotion_status
    CACHE_STATUSES["$lane"]=$cache_status
    CLEANUP_STATUSES["$lane"]=$cleanup_status
    printf '%s completed: generator=%d promotion=%d cache-validation=%d state-cleanup=%d\n' \
        "$lane" "$generate_status" "$promotion_status" "$cache_status" "$cleanup_status"

    if [ "$cache_status" -ne 0 ]; then
        return "$cache_status"
    fi
    if [ "$generate_status" -ne 0 ]; then
        return "$generate_status"
    fi
    if [ "$promotion_status" -ne 0 ]; then
        return "$promotion_status"
    fi
    return "$cleanup_status"
}

set +e
run_census census-1 "$CENSUS_ONE_CACHE" "$CENSUS_ONE_STATE" \
    "$CENSUS_ONE_EVIDENCE" "$CENSUS_ONE_CACHE_SAFE_MARKER" \
    "census-1.json" "census-1-diagnostic.json"
census_one_status=$?
run_census census-2 "$CENSUS_TWO_CACHE" "$CENSUS_TWO_STATE" \
    "$CENSUS_TWO_EVIDENCE" "$CENSUS_TWO_CACHE_SAFE_MARKER" \
    "census-2.json" "census-2-diagnostic.json"
census_two_status=$?
set -e

if [ "$census_one_status" -ne 0 ] || [ "$census_two_status" -ne 0 ]; then
    printf 'Census lanes failed: census-1=%d census-2=%d\n' \
        "$census_one_status" "$census_two_status" >&2
    # Cache-integrity failures take precedence, followed by generator,
    # promotion, and state-cleanup failures, each in stable lane order.
    for lane in census-1 census-2; do
        if [ "${CACHE_STATUSES[$lane]}" -ne 0 ]; then
            exit "${CACHE_STATUSES[$lane]}"
        fi
    done
    for lane in census-1 census-2; do
        if [ "${GENERATE_STATUSES[$lane]}" -ne 0 ]; then
            exit "${GENERATE_STATUSES[$lane]}"
        fi
    done
    for lane in census-1 census-2; do
        if [ "${PROMOTION_STATUSES[$lane]}" -ne 0 ]; then
            exit "${PROMOTION_STATUSES[$lane]}"
        fi
    done
    for lane in census-1 census-2; do
        if [ "${CLEANUP_STATUSES[$lane]}" -ne 0 ]; then
            exit "${CLEANUP_STATUSES[$lane]}"
        fi
    done
    exit 1
fi

printf 'Comparing trusted censuses promoted from independent cache lanes\n'
cmp "$OUTPUT_DIR/census-1.json" "$OUTPUT_DIR/census-2.json"
sha256sum "$OUTPUT_DIR/census-1.json" "$OUTPUT_DIR/census-2.json"

docker run --rm --platform linux/amd64 \
    --mount "type=bind,src=$PROJECT_ROOT,dst=/workspace,readonly" \
    --mount "type=bind,src=$CONTROL_DIR,dst=/control,readonly" \
    --mount "type=bind,src=$OUTPUT_DIR,dst=/output" \
    --workdir /workspace \
    "$IMAGE_TAG" \
    bash -ceu '
        /control/census seal scripts/migration-test/release-catalog.json /output/census-1.json \
            /output/runtime-schema-census.json.gz \
            /output/runtime-schema-routes.json \
            /output/runtime-schema-routes.md
        /control/census verify scripts/migration-test/release-catalog.json \
            /output/runtime-schema-census.json.gz \
            /output/runtime-schema-routes.json \
            /output/runtime-schema-routes.md
    '

if [ "$mode" = init ]; then
    printf 'Installing verified sealed census outputs into the checkout\n'
    mkdir -p "$PROJECT_ROOT/scripts/migration-test/census/testdata"
    install -m 0644 "$OUTPUT_DIR/runtime-schema-census.json.gz" \
        "$PROJECT_ROOT/scripts/migration-test/census/testdata/runtime-schema-census.json.gz"
    install -m 0644 "$OUTPUT_DIR/runtime-schema-routes.json" \
        "$PROJECT_ROOT/scripts/migration-test/census/testdata/runtime-schema-routes.json"
    install -m 0644 "$OUTPUT_DIR/runtime-schema-routes.md" \
        "$PROJECT_ROOT/scripts/migration-test/census/testdata/runtime-schema-routes.md"
else
    printf 'Comparing verified sealed census outputs with checked-in evidence\n'
    cmp "$OUTPUT_DIR/runtime-schema-census.json.gz" \
        "$PROJECT_ROOT/scripts/migration-test/census/testdata/runtime-schema-census.json.gz"
    cmp "$OUTPUT_DIR/runtime-schema-routes.json" \
        "$PROJECT_ROOT/scripts/migration-test/census/testdata/runtime-schema-routes.json"
    cmp "$OUTPUT_DIR/runtime-schema-routes.md" \
        "$PROJECT_ROOT/scripts/migration-test/census/testdata/runtime-schema-routes.md"
fi

touch "$INTEGRITY_MARKER"
printf 'Independent census integrity checks passed; cache lanes are safe to retain.\n'
