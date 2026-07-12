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

# assert_conformance_passed LOGFILE LABEL
# Fail the gate unless the top-level TestConformance in LOGFILE ran and PASSED. The
# checks MUST anchor to column-0 result lines: `go test -v` indents subtest results, and
# RunAll legitimately skips backend-inapplicable subtests (e.g. Claim concurrency on the
# single-writer embedded-Dolt reference), so an unanchored `--- SKIP: TestConformance`
# grep would match an indented subtest and false-fail the gate even though the top-level
# suite passed. Deletes LOGFILE either way.
assert_conformance_passed() {
  local log="$1" label="$2"
  if grep -qE '^--- SKIP: TestConformance' "$log" || ! grep -qE '^--- PASS: TestConformance' "$log"; then
    rm -f "$log"
    echo "FATAL: $label conformance (top-level TestConformance) skipped or did not run; storage-parity gate is not enforced" >&2
    exit 1
  fi
  rm -f "$log"
}

# require_backend NAME -> success if NAME is listed (comma-separated) in
# BEADS_CONFORMANCE_REQUIRE. CI sets BEADS_CONFORMANCE_REQUIRE=postgres,mysql so a
# backend that self-skips because its BEADS_*_TEST_URL is missing becomes a hard
# failure instead of a silent green; unset locally, the backend self-skips as before.
require_backend() {
  local want="$1" tok
  local IFS=,
  for tok in ${BEADS_CONFORMANCE_REQUIRE:-}; do
    [ "$(printf '%s' "$tok" | tr -d '[:space:]')" = "$want" ] && return 0
  done
  return 1
}

# run_sql_conformance NAME PKG RUN URLVAR
# Run one SQL backend's in-process conformance + wedge gates. When NAME is required
# (see require_backend), run verbose and assert the TestConformance parity oracle
# actually ran and passed — mirroring the embedded-Dolt reference guard so a missing
# BEADS_*_TEST_URL can't silently narrow coverage. Otherwise run plain and let the
# suite self-skip. URLVAR names the env var to set, surfaced in the failure hint.
run_sql_conformance() {
  local name="$1" pkg="$2" run="$3" urlvar="$4"
  if require_backend "$name"; then
    local log
    log="$(mktemp)"
    CGO_ENABLED=1 go test -tags "$TAGS" -v "$pkg" -run "$run" | tee "$log"
    assert_conformance_passed "$log" "$name backend (set $urlvar)"
  else
    CGO_ENABLED=1 go test -tags "$TAGS" "$pkg" -run "$run"
  fi
}

echo "==> Tier 1: in-process store conformance + wedge gates"
# The embedded-Dolt backend runs the full backend-agnostic suite (conformance.RunAll) as
# the reference oracle every SQL backend is compared against. BEADS_TEST_EMBEDDED_DOLT=1
# is REQUIRED, not optional: without it that suite self-skips
# (internal/storage/embeddeddolt/conformance_test.go), which would let this whole
# storage-parity gate report success while never running the oracle. Run it with -v and
# fail loudly if the top-level TestConformance skips or reports no pass, so the gate can
# never go green on a silent skip.
# (assert_conformance_passed explains why the skip/pass checks anchor to column-0 lines.)
embedded_dolt_log="$(mktemp)"
BEADS_TEST_EMBEDDED_DOLT=1 CGO_ENABLED=1 go test -tags "$TAGS" -v \
  ./internal/storage/embeddeddolt/ -run TestConformance | tee "$embedded_dolt_log"
assert_conformance_passed "$embedded_dolt_log" "embedded-Dolt reference (need BEADS_TEST_EMBEDDED_DOLT=1)"
# Each SQL backend now passes the FULL in-process store conformance suite (TestConformance
# = conformance.RunAll, the same ~40 behavior subtests the Dolt reference runs) — the only
# methods it does NOT implement are the genuinely-Dolt-only ones (version-control/remote/
# sync/…), which RunAll does not exercise. Alongside RunAll: live smoke, the
# interface-completeness audit (shell == deferral allowlist — no SILENT unsupported) plus
# its behavioral complement (every allowlisted method returns typed ErrUnsupported), the
# seed-once regression, and the dialect corpus-PREPARE + password-redaction gates. All
# self-skip without BEADS_PG_TEST_URL — or hard-fail when BEADS_CONFORMANCE_REQUIRE
# lists postgres (see run_sql_conformance), so CI can't go green with the wedge absent.
run_sql_conformance postgres ./internal/storage/postgres/ \
  'TestPGSmoke|TestInterfaceCompleteness|TestUnsupportedContract|TestConformance|TestSeedOnlyOnFirstProvision' \
  BEADS_PG_TEST_URL
CGO_ENABLED=1 go test -tags "$TAGS" ./internal/storage/pgdialect/
# MySQL wedge gates (self-skip without BEADS_MYSQL_TEST_URL, or hard-fail when
# BEADS_CONFORMANCE_REQUIRE lists mysql); the dialect rewrite test (the is_blocked 1093
# workaround) always runs.
run_sql_conformance mysql ./internal/storage/mysql/ \
  'TestInterfaceCompleteness|TestUnsupportedContract|TestConformance|TestSeedOnlyOnFirstProvision' \
  BEADS_MYSQL_TEST_URL
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
