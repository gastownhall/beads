package testutil

import (
	"os"
	"testing"
)

// gm-2g3g5r: EnsureDoltContainerForTestMain returns early when Docker is
// unavailable (testdoltserver.go:236-238) WITHOUT reaching
// ensureSharedContainer, which is the only code that overwrites the ambient
// BEADS_DOLT_SERVER_PORT / BEADS_DOLT_PORT. Every TestMain then prints a WARN
// and runs the suite anyway, so the ambient port -- in a gc-managed city, the
// PRODUCTION dolt port -- stays in force for the whole run.
//
// "No container" must mean "no server", not "whatever the environment says".
func TestEnsureDoltContainerForTestMain_NeutralizesAmbientPortOnFailure(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")
	t.Setenv("BEADS_DOLT_PORT", "59999")

	if err := EnsureDoltContainerForTestMain(); err == nil {
		t.Skip("Docker is available here; this test covers the no-Docker path")
	}

	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		t.Errorf("FAIL-OPEN: BEADS_DOLT_SERVER_PORT still %q after container setup "+
			"failed; test-mode stores will resolve to it", p)
	}
	if p := os.Getenv("BEADS_DOLT_PORT"); p != "" {
		t.Errorf("FAIL-OPEN: BEADS_DOLT_PORT still %q after container setup failed", p)
	}
}
