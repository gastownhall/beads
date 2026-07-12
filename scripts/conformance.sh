#!/usr/bin/env bash
#
# Single conformance entrypoint. CI runs this verbatim; run it locally the same way:
#
#   ./scripts/conformance.sh
#
# Two tiers, both reading the backend registry (test/conformance/profiles.go for the
# E2E tier; the per-backend conformance_test.go files for the in-process tier). Add a
# backend = add a profile + a factory; both tiers pick it up.
#
# Backends that need an external service auto-skip when their env is unset:
#   BEADS_PG_TEST_URL   postgres://user:pass@host:port/db   (enables the postgres backend)
#   BEADS_PG_PASSWORD   optional, if the password is not in the URL
#
# Optional deep gate (bts-rs 523-scenario differential oracle; needs the bts-rs
# checkout + ~50 min, so it is off by default and not part of the per-PR loop):
#   CONFORMANCE_DEEP=1  BTS_RS_DIR=/path/to/bts-rs  ./scripts/conformance.sh
#
set -euo pipefail
cd "$(dirname "$0")/.."

TAGS="gms_pure_go"

echo "==> Tier 1: in-process store conformance + wedge gates"
# The embedded-Dolt backend runs the full backend-agnostic suite (conformance.RunAll) as
# the reference oracle every SQL backend is compared against. BEADS_TEST_EMBEDDED_DOLT=1
# is REQUIRED, not optional: without it that suite self-skips
# (internal/storage/embeddeddolt/conformance_test.go), which would let this whole
# storage-parity gate report success while never running the oracle. Run it with -v and
# fail loudly if the top-level TestConformance skips or reports no pass, so the gate can
# never go green on a silent skip.
#
# The skip/pass checks MUST anchor to top-level result lines (column 0). `go test -v`
# indents subtest results, and RunAll legitimately skips backend-inapplicable subtests
# (e.g. Claim/ClaimReadyIssueConcurrentExclusivity self-skips on the single-writer
# embedded-Dolt reference). An unanchored `--- SKIP: TestConformance` grep matches those
# indented subtest lines and false-fails the gate on every run even though the top-level
# suite passed.
embedded_dolt_log="$(mktemp)"
BEADS_TEST_EMBEDDED_DOLT=1 CGO_ENABLED=1 go test -tags "$TAGS" -v \
  ./internal/storage/embeddeddolt/ -run TestConformance | tee "$embedded_dolt_log"
if grep -qE '^--- SKIP: TestConformance' "$embedded_dolt_log" || ! grep -qE '^--- PASS: TestConformance' "$embedded_dolt_log"; then
  rm -f "$embedded_dolt_log"
  echo "FATAL: embedded-Dolt reference conformance skipped or did not run; storage parity gate is not enforced (need BEADS_TEST_EMBEDDED_DOLT=1)" >&2
  exit 1
fi
rm -f "$embedded_dolt_log"
# Each SQL backend now passes the FULL in-process store conformance suite (TestConformance
# = conformance.RunAll, the same ~40 behavior subtests the Dolt reference runs) — the only
# methods it does NOT implement are the genuinely-Dolt-only ones (version-control/remote/
# sync/…), which RunAll does not exercise. Alongside RunAll: live smoke, the
# interface-completeness audit (shell == deferral allowlist — no SILENT unsupported) plus
# its behavioral complement (every allowlisted method returns typed ErrUnsupported), the
# seed-once regression, and the dialect corpus-PREPARE + password-redaction gates. All
# self-skip without BEADS_PG_TEST_URL.
CGO_ENABLED=1 go test -tags "$TAGS" ./internal/storage/postgres/ \
  -run 'TestPGSmoke|TestInterfaceCompleteness|TestUnsupportedContract|TestConformance|TestSeedOnlyOnFirstProvision'
CGO_ENABLED=1 go test -tags "$TAGS" ./internal/storage/pgdialect/
# MySQL wedge gates (self-skip without BEADS_MYSQL_TEST_URL); the dialect rewrite test
# (the is_blocked 1093 workaround) always runs.
CGO_ENABLED=1 go test -tags "$TAGS" ./internal/storage/mysql/ \
  -run 'TestInterfaceCompleteness|TestUnsupportedContract|TestConformance|TestSeedOnlyOnFirstProvision'
CGO_ENABLED=1 go test -tags "$TAGS" ./internal/storage/mysqldialect/
# SQLite is embedded (pure-Go), always runs.
CGO_ENABLED=1 go test -tags "$TAGS" ./internal/storage/sqlite/ \
  -run 'TestInterfaceCompleteness|TestUnsupportedContract|TestConformance|TestSeedOnlyOnFirstProvision'
CGO_ENABLED=1 go test -tags "$TAGS" ./internal/storage/sqlitedialect/

echo "==> Tier 2: end-to-end 'bd init' + CLI conformance (differential vs Dolt)"
CGO_ENABLED=1 go test -tags "$TAGS e2e" ./test/conformance/

if [[ "${CONFORMANCE_DEEP:-0}" == "1" ]]; then
  echo "==> Deep: bts-rs 523-scenario differential oracle"
  ./scripts/run-oracle-p.sh
fi

echo "==> conformance OK"
