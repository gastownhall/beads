#!/usr/bin/env bash
# Shared candidate policy; callers own PROJECT_ROOT, CACHE_DIR and successful-output cleanup.
build_candidate() {
    if [ -n "${CANDIDATE_BIN:-}" ]; then
        if [ ! -f "$CANDIDATE_BIN" ] || [ ! -x "$CANDIDATE_BIN" ]; then
            printf 'ERROR: CANDIDATE_BIN must name an executable file: %s\n' "$CANDIDATE_BIN" >&2
            return 2
        fi
        local directory
        directory=$(cd "$(dirname "$CANDIDATE_BIN")" && pwd) || return $?
        printf '%s/%s\n' "$directory" "$(basename "$CANDIDATE_BIN")"
        return
    fi

    # Empty is automatic too: the upgrade version loop forwards an empty value.
    local candidate="$CACHE_DIR/bd-candidate-$$"
    printf '%bBuilding candidate binary...%b\n' "${YELLOW:-}" "${NC:-}" >&2
    (
        cd "$PROJECT_ROOT" || exit $?
        # Keep canonical candidate flags out of historical release builds.
        # shellcheck source=../../.buildflags
        source "$PROJECT_ROOT/.buildflags" || exit $?
        go build -o "$candidate" ./cmd/bd
    ) >&2 || {
        local status=$?
        # Remove only this attempted build's output; cleanup must not mask failure.
        rm -f -- "$candidate" || :
        return "$status"
    }
    # Command substitution may disable errexit; emit a path only after success.
    printf '%s\n' "$candidate"
}
