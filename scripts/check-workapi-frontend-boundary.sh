#!/usr/bin/env bash
# internal/workapi must not read cwd- or environment-derived state.
#
# The import half of this boundary is the workapi-frontend-boundary depguard
# rule in .golangci.yml. This is the symbol half depguard cannot express:
# internal/workapi legitimately imports internal/config (for
# GetCustomTypesFromYAML, a workspace-scoped read), but process-local reads
# derived from a client's cwd or environment are meaningless in a long-lived
# server and must not leak into the shared contract.
#
# Runs as //scripts/repochecks:workapi_frontend_boundary_test.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

banned='config\.GetDirectoryLabels|os\.Getenv'

if [[ ! -d internal/workapi ]]; then
    printf 'internal/workapi does not exist; skipping frontend-boundary check.\n'
    exit 0
fi

if grep -rnE "$banned" internal/workapi --include='*.go'; then
    cat >&2 <<'MSG'

internal/workapi must not read cwd- or environment-derived state.

Those reads describe the client's process, which a server process does not
share. Resolve them in the frontend and pass the result in as a parameter.
See internal/workapi/doc.go.
MSG
    exit 1
fi

printf 'No banned cwd/env accessors in internal/workapi.\n'
