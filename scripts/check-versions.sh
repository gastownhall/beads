#!/bin/bash
# Compatibility entrypoint for the authoritative Go release-version checker.
# Run this before committing version bumps.

set -euo pipefail

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

cd "$repo_root"
exec go run -tags=gms_pure_go ./scripts/check-versions "$@"
