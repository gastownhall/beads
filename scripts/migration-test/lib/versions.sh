#!/bin/bash
# Storage era definitions and upgrade path matrix.

# The explicit export/import bridges for reviewed classic SQLite layouts. They
# are not in-place schema migrations: each SQLite source remains a rollback copy.
readonly CLASSIC_SQLITE_VERSION="v0.49.6"
readonly CONFIGURED_SQLITE_VERSION="v0.50.3"
readonly PRE_CANONICAL_SQLITE_VERSION="v0.17.0"
# This source-tag-qualified lane deliberately has no release asset.
readonly SOURCE_TAG_SQLITE_VERSION="v0.9.1"
readonly SOURCE_TAG_SQLITE_MODULE="github.com/steveyegge/beads"
readonly SOURCE_TAG_SQLITE_MODULE_SUM="h1:2KRCRCEtHvfMzmhYrpZzWl2rk2Y1Wq9p2awTdQIK9yY="
readonly SOURCE_TAG_SQLITE_GOMOD_SUM="h1:CNV215xONrxBs18j3BXx/xvl1qyCO2SeQRqiTN213Vg="
readonly SOURCE_TAG_SQLITE_COMMIT="e3c8554fa2c3e4b9caf7e296e9c8abbe24211a72"
readonly SOURCE_TAG_SQLITE_REF="refs/tags/v0.9.1"
readonly SOURCE_TAG_SQLITE_GO_TOOLCHAIN="go1.25.0"

# Representative, public release binaries for the historical-Dolt E2E test.
# These are deliberately a small, reviewed compatibility corpus rather than
# a discovery mechanism or an exhaustive tag matrix.
declare -ar HISTORICAL_DOLT_VERSIONS=(
    "v0.55.4"
    "v0.56.1"
    "v0.57.0"
    "v0.62.0"
)

# Reviewed direct embedded-Dolt sources. Unlike the server corpus, their own
# embedded data is opened and migrated in place by the candidate.
declare -ar EMBEDDED_DOLT_VERSIONS=(
    "v0.63.3"
    "v1.0.0"
    "v1.0.1"
    "v1.1.0"
    "v1.1.2"
)

# The test server is an external compatibility fixture, not the runner's
# ambient Dolt installation. CI downloads this exact linux/amd64 archive.
readonly DOLT_TEST_RUNTIME_VERSION="v2.1.8"
readonly DOLT_TEST_RUNTIME_SHA256="f66318f08ed66e409fc39363ae0fff8ce6fbf6dba9f5bac632b91527b9632a74"

# Classic SQLite files that must be retained byte-for-byte for rollback before
# the manual bridge removes the active copies.
declare -ar CLASSIC_SQLITE_ROLLBACK_FILES=(
    "beads.db"
    "beads.db-wal"
    "beads.db-shm"
    "metadata.json"
    "config.json"
    "config.yaml"
    "issues.jsonl"
)

# Legacy Dolt-server metadata that must be retained with the dolt/ directory
# before the manual bridge clears the active source.
declare -ar LEGACY_DOLT_ROLLBACK_FILES=(
    "metadata.json"
    "config.json"
    "config.yaml"
    "issues.jsonl"
)

# Release artifacts and expected qualification outcomes for strict CI lanes.
# Strict entries are deliberately explicit: adding a historical lane requires
# reviewing the official asset, checksum, source capabilities, and supported
# migration outcome instead of silently discovering them at runtime.
declare -Ar STRICT_RELEASE_ASSETS=(
    ["v0.17.0|linux|amd64"]="beads_0.17.0_linux_amd64.tar.gz"
    ["v0.49.6|linux|amd64"]="beads_0.49.6_linux_amd64.tar.gz"
    ["v0.50.3|linux|amd64"]="beads_0.50.3_linux_amd64.tar.gz"
    ["v0.55.4|linux|amd64"]="beads_0.55.4_linux_amd64.tar.gz"
    ["v0.56.1|linux|amd64"]="beads_0.56.1_linux_amd64.tar.gz"
    ["v0.57.0|linux|amd64"]="beads_0.57.0_linux_amd64.tar.gz"
    ["v0.62.0|linux|amd64"]="beads_0.62.0_linux_amd64.tar.gz"
    ["v0.63.3|linux|amd64"]="beads_0.63.3_linux_amd64.tar.gz"
    ["v1.0.0|linux|amd64"]="beads_1.0.0_linux_amd64.tar.gz"
    ["v1.0.1|linux|amd64"]="beads_1.0.1_linux_amd64.tar.gz"
    ["v1.1.0|linux|amd64"]="beads_1.1.0_linux_amd64.tar.gz"
    ["v1.1.2|linux|amd64"]="beads_1.1.2_linux_amd64.tar.gz"
)
declare -Ar STRICT_RELEASE_SHA256=(
    ["v0.17.0|linux|amd64"]="d4d08617a324c85b45c9628bc519d659a9ff9c7c37da67aa48727e0af7f19a75"
    ["v0.49.6|linux|amd64"]="8546dc9a47e11dc31ac2bc9a0224a9c690975e91850932cbb62623053fbb7db8"
    ["v0.50.3|linux|amd64"]="e94b09e0b6a9324bbc0e81ea36bccaaa42172a926bfedfb389e9a26dedb63184"
    ["v0.55.4|linux|amd64"]="e0fa25456dd82890230eef17653448a0bf995104c78864be91c5ed84426a5f49"
    ["v0.56.1|linux|amd64"]="4f9f6cc44465a11613ff529009901eaaf841c6b1f91c15e002b0ecda2015a15c"
    ["v0.57.0|linux|amd64"]="f8629d5627bed7d25f06f92334addc171d679f9aed9d08c5d42a9684205dc04b"
    ["v0.62.0|linux|amd64"]="4cca7265b22e5c3ca8d62ab0b9752bec31f68b7f5fa636282a4c7e5454c35535"
    ["v0.63.3|linux|amd64"]="5f4efd2e010209b3f381dbcd783b2a3a652f50ea72f40ef04c8ba434d408bf9e"
    ["v1.0.0|linux|amd64"]="7057db1e92428fcf5c08d5dc6b07ead57e588b262cba78b9a26893d55bd29fdb"
    ["v1.0.1|linux|amd64"]="1d2364d5d7083a4634a9e734ca87822fb79c2b6625988f9f791e3376313b1b77"
    ["v1.1.0|linux|amd64"]="b0f3dd607c3fb989ee08d0a6854fba80d0402971eb108f9af6170bc14d491a34"
    ["v1.1.2|linux|amd64"]="a72d71ed374955dc9f83a0f90b54bd7b6a0016709dd1676ae2e368651ed401c2"
)

strict_release_asset() {
    local value="${STRICT_RELEASE_ASSETS["$1|$2|$3"]:-}"
    [ -n "$value" ] || return 1
    printf '%s\n' "$value"
}

strict_release_sha256() {
    local value="${STRICT_RELEASE_SHA256["$1|$2|$3"]:-}"
    [ -n "$value" ] || return 1
    printf '%s\n' "$value"
}
