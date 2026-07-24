#!/usr/bin/env bash
#
# Single conformance entrypoint. CI runs this verbatim; run it locally the same way:
#
#   ./scripts/conformance.sh
#
# Two tiers exercise the storage conformance contract: the in-process storage
# corpus against the embedded-Dolt oracle, then the real-binary CLI corpus.
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

echo "==> Tier 1: in-process store conformance + wedge gates"
# The embedded-Dolt backend runs the full backend-agnostic suite (conformance.RunAll) as
# the reference oracle any future backend is compared against. BEADS_TEST_EMBEDDED_DOLT=1
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
echo "==> Tier 2: end-to-end 'bd init' + CLI conformance (reference round-trip)"
CGO_ENABLED=1 go test -tags "$TAGS e2e" ./test/conformance/

echo "==> conformance OK"
