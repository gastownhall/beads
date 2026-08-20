package testutil

import (
	"os"
	"strconv"
	"testing"
)

// TestDoltContainerStartSites_SetBeadsServerPortEnv guards against dolt test
// suites silently opening stores on an ambient shared Dolt server instead of
// their own testcontainer. applyConfigDefaults (internal/storage/dolt/store.go)
// resolves the connection port from BEADS_DOLT_SERVER_PORT first, falling
// back to the legacy BEADS_DOLT_PORT only when BEADS_DOLT_SERVER_PORT is
// unset. Both container-start sites must therefore set BEADS_DOLT_SERVER_PORT
// (not just BEADS_DOLT_PORT), or an ambiently-set BEADS_DOLT_SERVER_PORT
// (e.g. a rig's shared dev server) silently wins over the fresh
// testcontainer's port.
func TestDoltContainerStartSites_SetBeadsServerPortEnv(t *testing.T) {
	if state := checkDolt(); state != doltReady {
		t.Skipf("skipping test: %s", state)
	}

	t.Run("StartIsolatedDoltContainer", func(t *testing.T) {
		portStr := StartIsolatedDoltContainer(t)

		got := os.Getenv("BEADS_DOLT_SERVER_PORT")
		if got != portStr {
			t.Errorf("BEADS_DOLT_SERVER_PORT = %q, want %q (the isolated container's port) — testdoltserver.go:212 must also set BEADS_DOLT_SERVER_PORT, not just BEADS_DOLT_PORT", got, portStr)
		}
	})

	t.Run("EnsureDoltContainerForTestMain", func(t *testing.T) {
		if err := EnsureDoltContainerForTestMain(); err != nil {
			t.Fatalf("EnsureDoltContainerForTestMain: %v", err)
		}
		t.Cleanup(TerminateDoltContainer)

		want := strconv.Itoa(DoltContainerPortInt())
		got := os.Getenv("BEADS_DOLT_SERVER_PORT")
		if got != want {
			t.Errorf("BEADS_DOLT_SERVER_PORT = %q, want %q (the shared container's port) — testdoltserver.go:221 must also set BEADS_DOLT_SERVER_PORT, not just BEADS_DOLT_PORT", got, want)
		}
	})
}
