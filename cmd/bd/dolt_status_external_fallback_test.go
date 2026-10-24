package main

// Regression tests for GH#6017: `bd dolt status` reported "not running" for a
// launchd/systemd-managed local Dolt server in a dolt_mode=server workspace,
// because the PID-file path (doltserver.IsRunning) looks for a workspace-local
// PID file that an externally-managed server never writes, while CRUD against
// the same server worked. The fix probes the resolved endpoint before
// reporting the server down and, if something is serving, describes it via
// the external/SQL-probe path.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// clearDoltStatusEnv pins every ambient source DefaultConfig consults for the
// port, host and mode so the decision depends only on the planted port file.
func clearDoltStatusEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_SHARED_SERVER_DIR", "")
}

// writePortFile plants the runtime port file so DefaultConfig(dir).Port
// resolves to a known value without touching shared-server state.
func writePortFile(t *testing.T, dir string, port string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, doltserver.PortFileName), []byte(port+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalStatusNeedsExternalFallback(t *testing.T) {
	clearDoltStatusEnv(t)

	srvDir := t.TempDir()
	writePortFile(t, srvDir, "3308")

	reachable := func(host string, port int) bool { return port == 3308 }
	unreachable := func(host string, port int) bool { return false }

	cases := []struct {
		name  string
		state *doltserver.State
		probe serverProbe
		want  bool
	}{
		{
			name:  "bd-managed running server: no fallback (PID path is authoritative)",
			state: &doltserver.State{Running: true, PID: 1234, Port: 3308},
			probe: reachable,
			want:  false,
		},
		{
			name:  "no PID file but endpoint reachable: fall back to external",
			state: &doltserver.State{Running: false},
			probe: reachable,
			want:  true,
		},
		{
			name:  "nil state but endpoint reachable: fall back",
			state: nil,
			probe: reachable,
			want:  true,
		},
		{
			name:  "no PID file and endpoint unreachable: genuinely down, no fallback",
			state: &doltserver.State{Running: false},
			probe: unreachable,
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localStatusNeedsExternalFallback(tc.state, srvDir, "127.0.0.1", tc.probe)
			if got != tc.want {
				t.Errorf("localStatusNeedsExternalFallback() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLocalStatusNeedsExternalFallback_ProbeArgs pins that the probe is
// dialed with the caller's host and the port DefaultConfig resolves for the
// server directory, so the fallback and runExternalDoltStatus agree on the
// endpoint.
func TestLocalStatusNeedsExternalFallback_ProbeArgs(t *testing.T) {
	clearDoltStatusEnv(t)

	srvDir := t.TempDir()
	writePortFile(t, srvDir, "13306")

	var gotHost string
	var gotPort int
	probe := func(host string, port int) bool { gotHost, gotPort = host, port; return true }

	if !localStatusNeedsExternalFallback(&doltserver.State{Running: false}, srvDir, "localhost", probe) {
		t.Fatal("expected fallback when the probe reports the endpoint reachable")
	}
	if gotHost != "localhost" || gotPort != 13306 {
		t.Errorf("probe dialed %s:%d, want localhost:13306", gotHost, gotPort)
	}
}

// TestLocalStatusNeedsExternalFallback_NoPort ensures a workspace with no
// resolvable port never triggers a probe (avoids dialing port 0).
func TestLocalStatusNeedsExternalFallback_NoPort(t *testing.T) {
	clearDoltStatusEnv(t)

	emptyDir := t.TempDir() // no port file planted
	probeCalled := false
	probe := func(host string, port int) bool { probeCalled = true; return true }

	if got := localStatusNeedsExternalFallback(&doltserver.State{Running: false}, emptyDir, "127.0.0.1", probe); got {
		t.Error("expected no fallback when the port cannot be resolved")
	}
	if probeCalled {
		t.Error("probe must not be called when port <= 0")
	}
}
