package doltserver

import (
	"testing"
)

// TestDefaultConfig_NonzeroPortAlwaysHasSource pins the invariant that makes
// applyConfigDefaults' "nonzero ServerPort + PortSourceUnset ⇒ caller-explicit"
// inference sound: DefaultConfig must never hand back a resolved port with no
// source attached. If it does, a port DefaultConfig chose on the user's behalf
// is indistinguishable from one the caller explicitly asserted, and the storage
// layer stamps it PortSourceCallerExplicit (authoritative) — which silently
// disables the BEADS_DOLT_SERVER_PORT override and turns a benign auto-start
// port change into a hard failure (GH#4052).
func TestDefaultConfig_NonzeroPortAlwaysHasSource(t *testing.T) {
	// Neutralize ambient port env: a leaked BEADS_DOLT_SERVER_PORT from the
	// surrounding shell would resolve via PortSourceEnv and mask the gap.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("HOME", t.TempDir())

	for _, tc := range []struct {
		name   string
		shared string
	}{
		{name: "per-project mode", shared: "0"},
		{name: "shared-server mode", shared: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_SHARED_SERVER", tc.shared)
			cfg := DefaultConfig(t.TempDir())
			if cfg.Port != 0 && cfg.PortSource == PortSourceUnset {
				t.Fatalf("DefaultConfig returned Port=%d with PortSourceUnset "+
					"(PortSharedServer=%v): a port resolved on the user's behalf "+
					"is indistinguishable from a caller-explicit assertion",
					cfg.Port, cfg.PortSharedServer)
			}
		})
	}
}
