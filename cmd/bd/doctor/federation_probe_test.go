package doctor

// CheckFederationRemotesAPI decides whether the server is up from the
// workspace PID file. A server bd did not start (launchd, systemd, a
// container) writes no PID file there, so the check used to open the database
// on that very server and then report "Server not running". These tests cover
// the endpoint probe it now falls back to and the decision around it.
//
// Not cgo-gated (unlike federation_test.go): they exercise pure helpers with
// injected state and a plain TCP listener, so they run under CGO_ENABLED=0 too.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// clearDoltServerEnv removes every environment override that would change
// which endpoint the probe dials or how the workspace's server is classified,
// and points the user-global config at an empty home.
func clearDoltServerEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"BEADS_DOLT_SERVER_PORT",
		"BEADS_DOLT_PORT",
		"BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_SERVER_MODE",
		"BEADS_DOLT_PASSWORD",
		"BEADS_DOLT_REMOTESAPI_PORT",
		"BEADS_DOLT_DATA_DIR",
		"GT_ROOT",
	} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

// probeTestListener returns a loopback port, still listening when open is
// true and already closed (so a dial is refused) otherwise.
func probeTestListener(t *testing.T, open bool) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if open {
		t.Cleanup(func() { _ = ln.Close() })
	} else {
		_ = ln.Close()
	}
	return port
}

// externalServerWorkspace builds a Dolt workspace whose server port is known
// only from the server's own config.yaml (listener.port): the shape of a
// server started by launchd or systemd from that file, with no PID file and no
// port file under .beads/. A port of 0 leaves the server unconfigured.
func externalServerWorkspace(t *testing.T, port int) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(&configfile.Config{Backend: configfile.BackendDolt})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	if port > 0 {
		yaml := "listener:\n  host: 127.0.0.1\n  port: " + strconv.Itoa(port) + "\n"
		if err := os.WriteFile(filepath.Join(doltDir, "config.yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return beadsDir
}

func TestServerRunningOrProbeable(t *testing.T) {
	cases := []struct {
		name      string
		state     *doltserver.State
		probeSays bool
		want      bool
	}{
		{
			name:  "PID-tracked running server: probe not consulted",
			state: &doltserver.State{Running: true, PID: 4242, Port: 3308},
			want:  true,
		},
		{
			name:      "no PID file, endpoint serving: probe decides",
			state:     &doltserver.State{Running: false},
			probeSays: true,
			want:      true,
		},
		{
			name:      "nil state (PID file unreadable), endpoint serving: probe decides",
			state:     nil,
			probeSays: true,
			want:      true,
		},
		{
			name:      "no PID file and endpoint down: not running",
			state:     &doltserver.State{Running: false},
			probeSays: false,
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			probe := func() bool { calls++; return tc.probeSays }
			got := serverRunningOrProbeable(tc.state, probe)
			if got != tc.want {
				t.Errorf("serverRunningOrProbeable() = %v, want %v", got, tc.want)
			}
			// The PID-tracked case must short-circuit without dialing.
			if tc.state != nil && tc.state.Running && calls != 0 {
				t.Errorf("probe called %d times for a PID-tracked running server; want 0", calls)
			}
		})
	}
}

func TestFederationServerProbe(t *testing.T) {
	clearDoltServerEnv(t)

	t.Run("dials the endpoint the remotes check opens", func(t *testing.T) {
		port := probeTestListener(t, true)
		beadsDir := externalServerWorkspace(t, port)

		// The probe must ask about the same server doltServerConfig would
		// open, or it could confirm one server and then list another's
		// remotes.
		if got := doltServerConfig(beadsDir, filepath.Join(beadsDir, "dolt")).ServerPort; got != port {
			t.Fatalf("doltServerConfig resolved port %d, want the config.yaml listener port %d", got, port)
		}
		if !federationServerProbe(beadsDir) {
			t.Fatalf("federationServerProbe = false with a listener on 127.0.0.1:%d", port)
		}
	})

	t.Run("refused dial reports the server down", func(t *testing.T) {
		beadsDir := externalServerWorkspace(t, probeTestListener(t, false))
		if federationServerProbe(beadsDir) {
			t.Fatal("federationServerProbe = true with nothing listening")
		}
	})

	t.Run("unconfigured port never dials", func(t *testing.T) {
		beadsDir := externalServerWorkspace(t, 0)
		if federationServerProbe(beadsDir) {
			t.Fatal("federationServerProbe = true with no port configured")
		}
	})
}

// TestCheckRemotesAPIListener_ProbeFoundServer pins the detail line for a
// server the probe found: there is no PID to print, and "PID 0" would read
// as a bd-managed server with a corrupt PID file.
func TestCheckRemotesAPIListener_ProbeFoundServer(t *testing.T) {
	clearDoltServerEnv(t)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "false")
	t.Setenv("BEADS_DOLT_REMOTESAPI_PORT", strconv.Itoa(probeTestListener(t, false)))
	beadsDir := externalServerWorkspace(t, 0)

	check := checkRemotesAPIListener(beadsDir, 0)
	if check.Status != StatusError {
		t.Fatalf("got %s %q, want StatusError for a refused remotesapi dial", check.Status, check.Message)
	}
	if !strings.Contains(check.Detail, "no PID file") || strings.Contains(check.Detail, "PID 0") {
		t.Fatalf("Detail %q must say the server has no PID file and must not print PID 0", check.Detail)
	}

	tracked := checkRemotesAPIListener(beadsDir, 4242)
	if !strings.Contains(tracked.Detail, "PID 4242") {
		t.Fatalf("Detail %q must still name the PID of a PID-tracked server", tracked.Detail)
	}
}
