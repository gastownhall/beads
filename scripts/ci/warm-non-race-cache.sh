#!/usr/bin/env bash
# F4.3/F7b: populate the non-race GOCACHE (GOCACHE must already point at the
# restored/to-be-saved cache dir when this runs) with the compiled test
# binaries pr-preflight-platforms' and check-doc-freshness-platforms' legs
# need, without running any of the tests themselves (-o /dev/null, no
# Dolt/test-env setup required).
#
# Shared by every non-race seeder so they can never drift apart (F7b spec 4.2):
#   - main.yml's build-artifacts (ubuntu-latest / fork-PR path non-race save)
#   - main.yml's blacksmith-go-build-cache (Blacksmith / same-repo-PR path
#     non-race save)
#   - main.yml's test-windows (Windows non-race save)
#   - main.yml's test macOS leg (macOS non-race save; pr.yml's macOS legs
#     run on GitHub-hosted macos-latest for every PR)
#
# Package list mirrors exactly what pr.yml's check-doc-freshness-platforms and
# pr-preflight-platforms legs compile on every OS:
#   - ./cmd/bd (TestGeneratedHookTimeoutProcessBoundary)
#   - ./scripts, untagged (TestPRPreflight*, TestTestScriptPrebuiltBinaryContract)
#   - ./scripts, integration+gms_pure_go (TestDocFreshness*, TestRequiredSuiteContract)
#   - ./scripts/gitattributespolicy, integration+gms_pure_go
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

cd "$REPO_ROOT"

go test -tags gms_pure_go -c -o /dev/null ./cmd/bd
go test -tags gms_pure_go -c -o /dev/null ./scripts
go test '-tags=integration,gms_pure_go' -c -o /dev/null ./scripts
go test '-tags=integration,gms_pure_go' -c -o /dev/null ./scripts/gitattributespolicy
