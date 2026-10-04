//go:build !windows

package testutil

import (
	"errors"
	"os"
	"reflect"
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
			// The port must read as ambient whatever the surrounding run
			// exports. ./scripts/test.sh with BEADS_TEST_SHARED_SERVER=1 sets
			// the harness marker process-wide to the port it allocated, and
			// the gate would invert on a run where that happened to be this
			// fixture's port.
			// TestEnsureDoltContainerForTestMain_KeepsHarnessProvisionedPort
			// covers the marked cases on purpose.
			t.Setenv(EnvSharedDoltServer, "")

			forceTestMainFailure(t, state, nil)

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

// TestEnsureDoltContainerForTestMain_KeepsHarnessProvisionedPort is the other
// half of the invariant: "no container" must not erase a server the running
// harness started on purpose. ./scripts/test.sh with
// BEADS_TEST_SHARED_SERVER=1 starts one dockerless `dolt sql-server`, exports
// its port as BEADS_DOLT_PORT, and sets EnvSharedDoltServer to that same port;
// on a Docker-less host every one of these TestMains reaches a failure path,
// so clearing the port there would take the harness's own server down with it.
//
// The marker vouches for one port VALUE, never for the process, so the table
// is per variable: only a variable holding exactly the marked port survives.
// marked_but_divergent_var is the case that matters most. An unrelated
// BEADS_DOLT_SERVER_PORT beside the harness's port outranks it in
// applyConfigDefaults, so keeping it would carry the suite onto whatever
// server it names (gm-2g3g5r) with the marker set.
//
// Both failure paths run the whole table. They share one clear, and running
// the table through the second -- a container that would not start -- is what
// pins that it keeps sharing it.
func TestEnsureDoltContainerForTestMain_KeepsHarnessProvisionedPort(t *testing.T) {
	const (
		harnessPort = "59998" // the port the harness allocated and marked
		ambientPort = "59999" // reproProdPort in internal/storage/dolt: a production server
	)
	failures := []struct {
		name     string
		probe    doltReadiness
		startErr error
	}{
		{name: "not_ready", probe: doltNoDocker},
		{name: "container_start_failed", probe: doltReady, startErr: errors.New("dolt container would not start")},
	}
	cases := []struct {
		name                 string
		marker               string
		serverPort, port     string // "" leaves the variable unset
		wantServer, wantPort string // "" wants the variable unset
	}{
		{name: "unmarked", serverPort: ambientPort, port: harnessPort},
		{name: "harness_port_kept", marker: harnessPort, port: harnessPort, wantPort: harnessPort},
		{name: "marked_but_divergent_var", marker: harnessPort, serverPort: ambientPort, port: harnessPort, wantPort: harnessPort},
		{name: "divergent_legacy_var", marker: harnessPort, serverPort: harnessPort, port: ambientPort, wantServer: harnessPort},
		{name: "both_vars_name_the_harness_port", marker: harnessPort, serverPort: harnessPort, port: harnessPort, wantServer: harnessPort, wantPort: harnessPort},
		{name: "marker_names_neither_var", marker: "59997", serverPort: ambientPort, port: harnessPort},
		// The boolean form an earlier revision of this change exported. It now
		// names port 1, which neither variable holds, so a stale export of it
		// fails closed.
		{name: "boolean_marker", marker: "1", serverPort: ambientPort, port: harnessPort},
	}

	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					setOrUnsetEnvForTest(t, "BEADS_DOLT_SERVER_PORT", tc.serverPort)
					setOrUnsetEnvForTest(t, "BEADS_DOLT_PORT", tc.port)
					t.Setenv(EnvSharedDoltServer, tc.marker)
					forceTestMainFailure(t, f.probe, f.startErr)

					if err := EnsureDoltContainerForTestMain(); err == nil {
						t.Fatal("EnsureDoltContainerForTestMain() = nil; want an error, the marker does not start a container")
					}

					for name, want := range map[string]string{
						"BEADS_DOLT_SERVER_PORT": tc.wantServer,
						"BEADS_DOLT_PORT":        tc.wantPort,
					} {
						got, ok := os.LookupEnv(name)
						switch {
						case want == "" && ok:
							t.Errorf("FAIL-OPEN: %s still %q with %s=%q; only a variable holding "+
								"exactly the marked port survives", name, got, EnvSharedDoltServer, tc.marker)
						case want != "" && (!ok || got != want):
							t.Errorf("%s = %q (set=%v) with %s=%q; want %q -- the harness's own "+
								"shared server was erased", name, got, ok, EnvSharedDoltServer, tc.marker, want)
						}
					}
				})
			}
		})
	}
}

// TestCheckDoltFn_DefaultsToRealProbe pins the seam shut: the variable exists
// for the tests above and must not be left pointing at a stub in shipped code.
// Function identity is the assertion that states that -- a non-nil check
// passes for any leaked stub, including one that reports doltReady and would
// send a suite at whatever server the environment names.
func TestCheckDoltFn_DefaultsToRealProbe(t *testing.T) {
	if checkDoltFn == nil {
		t.Fatal("checkDoltFn is nil; EnsureDoltContainerForTestMain would panic")
	}
	if got, want := reflect.ValueOf(checkDoltFn).Pointer(), reflect.ValueOf(checkDolt).Pointer(); got != want {
		t.Fatalf("checkDoltFn does not point at checkDolt (%#x != %#x); the readiness "+
			"probe has been left seamed in shipped code", got, want)
	}
}

// TestStartSharedContainerFn_DefaultsToRealStart is the same pin for the
// container-start seam: a leaked stub that reports success would send every
// TestMain on to m.Run() with no container and nothing cleared.
func TestStartSharedContainerFn_DefaultsToRealStart(t *testing.T) {
	if startSharedContainerFn == nil {
		t.Fatal("startSharedContainerFn is nil; EnsureDoltContainerForTestMain would panic")
	}
	if got, want := reflect.ValueOf(startSharedContainerFn).Pointer(), reflect.ValueOf(startSharedContainer).Pointer(); got != want {
		t.Fatalf("startSharedContainerFn does not point at startSharedContainer (%#x != %#x); "+
			"the container start has been left seamed in shipped code", got, want)
	}
}

// forceTestMainFailure sends EnsureDoltContainerForTestMain down one of its
// failure paths without Docker: the probe reports probe, and when that is
// doltReady the container start returns startErr. The real start never runs,
// so doltServerOnce stays unconsumed for this package's real-container test.
// A start attempted after a not-ready probe fails the test; that path has no
// business starting anything.
func forceTestMainFailure(t *testing.T, probe doltReadiness, startErr error) {
	t.Helper()
	restoreProbe, restoreStart := checkDoltFn, startSharedContainerFn
	t.Cleanup(func() { checkDoltFn, startSharedContainerFn = restoreProbe, restoreStart })
	checkDoltFn = func() doltReadiness { return probe }
	startSharedContainerFn = func() error {
		if probe != doltReady {
			t.Errorf("container start attempted after the probe reported %q", probe)
		}
		return startErr
	}
}

// setOrUnsetEnvForTest sets key to value for the duration of the test, or
// removes it when value is empty; the ambient value is restored on cleanup.
func setOrUnsetEnvForTest(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
	if value != "" {
		return
	}
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}
