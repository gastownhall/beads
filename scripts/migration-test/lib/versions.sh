#!/bin/bash
# Storage era definitions and upgrade path matrix.

# Representative versions for each storage era.
# Format: era_name|representative_version|storage_dir|description
ERAS=(
    "sqlite|v0.49.6|beads.db|SQLite era (pre-Dolt)"
    "dolt_server|v0.57.0|dolt|Dolt server mode"
    "embedded_old|v0.62.0|dolt|Old embedded Dolt (in-process)"
    "embedded_current|v0.63.3|embeddeddolt|Current embedded Dolt"
)

# Direct upgrade paths to test: source_version
DIRECT_PATHS=(
    "v0.49.6"
    "v0.57.0"
    "v0.62.0"
    "v0.63.3"
)

# Stepping-stone paths: version1,version2,...,versionN
# Each version upgrades to the next, then finally to candidate.
STEPPING_STONE_PATHS=(
    "v0.49.6,v0.57.0"
    "v0.49.6,v0.55.4,v0.63.3"
    "v0.57.0,v0.63.3"
)

# Semver comparison: returns 0 if $1 <= $2
version_lte() {
    local v1="${1#v}"
    local v2="${2#v}"
    [ "$(printf '%s\n%s\n' "$v1" "$v2" | sort -V | head -1)" = "$v1" ]
}

# Semver comparison: returns 0 if $1 < $2
version_lt() {
    local v1="${1#v}"
    local v2="${2#v}"
    [ "$v1" != "$v2" ] && version_lte "$1" "$2"
}

# Returns the era name for a given version.
get_era() {
    local version="$1"
    if version_lt "$version" "v0.50.0"; then
        echo "sqlite"
    elif version_lt "$version" "v0.59.0"; then
        echo "dolt_server"
    elif version_lt "$version" "v0.63.3"; then
        echo "embedded_old"
    else
        echo "embedded_current"
    fi
}
