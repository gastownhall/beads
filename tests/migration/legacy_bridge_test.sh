#!/usr/bin/env bash
# The public SQLite bridge's fast safety checks (fake binaries only), which
# scripts/migration-test/run.sh runs before the corpus. Runs the harness in
# place, so it finds its siblings (lib/, ../migrate-legacy-to-current.sh);
# arguments are ignored.
set -euo pipefail
exec scripts/migration-test/legacy-bridge-test.sh
