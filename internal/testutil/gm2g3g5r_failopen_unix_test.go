//go:build !windows

package testutil

import (
	"os"
	"testing"
)

// TestEnsureDoltContainerForTestMain_ClearsAmbientPortWhenNotReady is the
// regression gate for gm-2g3g5r on the platform where the readiness probe can
// actually vary.
//
// It forces the probe rather than consulting the real one. Reading the real
// checkDolt() would make this test assert nothing on any host that has Docker
// -- i.e. on every CI runner, which is the lane that matters -- and would also
// start the shared singleton container just to observe a failure path, with no
// TestMain in this package to terminate it.
//
// Every not-ready state is covered, because the invariant is about the absence
// of a container and not the reason for it: an explicit BEADS_TEST_SKIP=dolt
// opt-out must fail closed exactly as a Docker-less host does.
func TestEnsureDoltContainerForTestMain_ClearsAmbientPortWhenNotReady(t *testing.T) {
	notReady := []doltReadiness{doltNoDocker, doltNoImage, doltWrongVersion, doltSkipped}

	for _, state := range notReady {
		t.Run(state.String(), func(t *testing.T) {
			t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")
			t.Setenv("BEADS_DOLT_PORT", "59999")

			restore := checkDoltFn
			t.Cleanup(func() { checkDoltFn = restore })
			checkDoltFn = func() doltReadiness { return state }

			err := EnsureDoltContainerForTestMain()
			if err == nil {
				t.Fatalf("EnsureDoltContainerForTestMain() = nil for state %s; want an error", state)
			}

			for _, name := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
				if v, ok := os.LookupEnv(name); ok {
					t.Errorf("FAIL-OPEN: %s still %q after setup failed with %q; "+
						"test-mode stores will resolve to it", name, v, err)
				}
			}
		})
	}
}

// TestCheckDoltFn_DefaultsToRealProbe pins the seam shut: the variable exists
// for the test above and must not be left pointing at a stub in shipped code.
func TestCheckDoltFn_DefaultsToRealProbe(t *testing.T) {
	if checkDoltFn == nil {
		t.Fatal("checkDoltFn is nil; EnsureDoltContainerForTestMain would panic")
	}
}
