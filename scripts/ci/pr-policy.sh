#!/usr/bin/env bash
# Required PR repository policy checks.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"

cd "$REPO_ROOT"

tmpdir=""
cleanup() {
    if [[ -n "$tmpdir" ]]; then
        rm -rf "$tmpdir"
    fi
}
trap cleanup EXIT

check_no_beads_jsonl_changes() {
    local base_ref=""

    if [[ "${CI_SKIP_BEADS_CHANGE_CHECK:-0}" == "1" ]]; then
        printf 'Skipping .beads/issues.jsonl guard because CI_SKIP_BEADS_CHANGE_CHECK=1\n'
        return 0
    fi

    if [[ -n "${CI_BEADS_DIFF_BASE:-}" ]]; then
        base_ref="$CI_BEADS_DIFF_BASE"
    elif [[ -n "${GITHUB_BASE_REF:-}" ]]; then
        base_ref="origin/${GITHUB_BASE_REF}"
    elif git rev-parse --verify --quiet origin/main >/dev/null; then
        base_ref="origin/main"
        printf 'No PR base ref found; checking .beads/issues.jsonl against origin/main.\n'
    else
        printf 'No diff base available; skipping .beads/issues.jsonl guard.\n'
        return 0
    fi

    if ! git rev-parse --verify --quiet "$base_ref" >/dev/null; then
        printf 'Diff base %s is unavailable; skipping .beads/issues.jsonl guard.\n' "$base_ref"
        return 0
    fi

    if git diff --name-only "$base_ref"...HEAD | grep -q '^\.beads/issues\.jsonl$'; then
        cat >&2 <<'EOF'
This change includes .beads/issues.jsonl.

That file is the project's issue database and should not be modified in PRs.

To fix:
  git checkout origin/main -- .beads/issues.jsonl
  git commit --amend
EOF
        return 1
    fi

    printf 'No .beads/issues.jsonl changes detected.\n'
}

check_workapi_frontend_boundary() {
    # The import half of this boundary is the workapi-frontend-boundary
    # depguard rule in .golangci.yml. This is the symbol half depguard cannot
    # express: internal/workapi legitimately imports internal/config (for
    # GetCustomTypesFromYAML, a workspace-scoped read), but process-local
    # reads derived from a client's cwd or environment are meaningless in a
    # long-lived server and must not leak into the shared contract.
    local banned='config\.GetDirectoryLabels|os\.Getenv'

    if [[ ! -d internal/workapi ]]; then
        printf 'internal/workapi does not exist; skipping frontend-boundary check.\n'
        return 0
    fi

    if grep -rnE "$banned" internal/workapi --include='*.go'; then
        cat >&2 <<'EOF'

internal/workapi must not read cwd- or environment-derived state.

Those reads describe the client's process, which a server process does not
share. Resolve them in the frontend and pass the result in as a parameter.
See internal/workapi/doc.go.
EOF
        return 1
    fi

    printf 'No banned cwd/env accessors in internal/workapi.\n'
}

build_docs_binary() {
    tmpdir="$(mktemp -d)"
    local build
    build="$(git rev-parse --short HEAD)"

    env CGO_ENABLED=0 go build \
        -ldflags="-X main.Build=${build}" \
        -o "$tmpdir/bd" \
        ./cmd/bd
}

ci_time "check build-tag policy" -- ./scripts/check-build-tags.sh
ci_time "check go install guidance" -- ./scripts/check-go-install-guidance.sh
ci_time "check version consistency" -- ./scripts/check-versions.sh
ci_time "build bd for docs checks" -- build_docs_binary
ci_time "check doc flags" -- ./scripts/check-doc-flags.sh "$tmpdir/bd"
ci_time "check doc freshness" -- ./scripts/check-doc-freshness.sh
ci_time "check testing.Short boundaries" -- ./scripts/check-testing-short.sh
ci_time "check workapi frontend boundary" -- check_workapi_frontend_boundary
ci_time "check no .beads/issues.jsonl changes" -- check_no_beads_jsonl_changes
# The OpenAPI drift gate belongs to a REQUIRED PR job. The Main workflow is
# perpetually concurrency-cancelled, so a gate that only ran there would be
# unenforced in practice.
ci_time "check openapi spec gate" -- make api-check
