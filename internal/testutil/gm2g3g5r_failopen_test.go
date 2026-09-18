package testutil

import (
	"os"
	"testing"
)

// gm-2g3g5r: EnsureDoltContainerForTestMain can return without ever reaching
// ensureSharedContainer, which is the only code that overwrites the ambient
// BEADS_DOLT_SERVER_PORT / BEADS_DOLT_PORT. Every TestMain then prints a WARN
// and runs the suite anyway, so the ambient port -- in a gc-managed city, the
// PRODUCTION dolt port -- would stay in force for the whole run.
//
// "No container" must mean "no server", not "whatever the environment says".
//
// This file holds the platform-independent half of that gate: the helper's own
// contract. The wiring -- that the failure paths actually call it -- is gated
// per-platform in gm2g3g5r_failopen_unix_test.go and
// gm2g3g5r_failopen_windows_test.go, because the two implementations of
// EnsureDoltContainerForTestMain fail for different reasons.

// TestNeutralizeAmbientDoltPort_ClearsBothVariables pins the helper contract:
// both variables are cleared, not merely one. Clearing only
// BEADS_DOLT_SERVER_PORT would leave applyConfigDefaults falling back to the
// legacy BEADS_DOLT_PORT and resolving onto the ambient server anyway.
func TestNeutralizeAmbientDoltPort_ClearsBothVariables(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")
	t.Setenv("BEADS_DOLT_PORT", "59998")

	neutralizeAmbientDoltPort()

	for _, name := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("FAIL-OPEN: %s still set to %q; test-mode stores will resolve to it", name, v)
		}
	}
}

// TestNeutralizeAmbientDoltPort_IsIdempotent guards the unset-when-already-unset
// path, which both failure branches of EnsureDoltContainerForTestMain can hit
// in sequence on a host that never had the variables set.
func TestNeutralizeAmbientDoltPort_IsIdempotent(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")
	t.Setenv("BEADS_DOLT_PORT", "59999")

	neutralizeAmbientDoltPort()
	neutralizeAmbientDoltPort()

	for _, name := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("%s reappeared as %q after a second call", name, v)
		}
	}
}
