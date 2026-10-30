//go:build cgo

package doctor

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestCheckFederationRemotesAPI_NonDoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a config with sqlite backend
	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckFederationRemotesAPI(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for non-Dolt backend, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "N/A") {
		t.Errorf("expected N/A message, got %q", check.Message)
	}
	if check.Category != CategoryFederation {
		t.Errorf("expected CategoryFederation, got %q", check.Category)
	}
}

func TestCheckFederationRemotesAPI_NoDoltDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a dolt backend config but don't create the dolt directory
	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckFederationRemotesAPI(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for missing dolt database, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "no dolt database") {
		t.Errorf("expected message about no dolt database, got %q", check.Message)
	}
}

func TestCheckFederationRemotesAPI_ServerNotRunning(t *testing.T) {
	// Isolate from orchestrator daemon which would be detected as a running server
	t.Setenv("GT_ROOT", "")
	noLiveDoltEndpoint(t)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a dolt backend config
	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	// No PID file exists, server is not running, no DB to query remotes from.
	// The check should not crash and should return OK or a safe status.
	check := CheckFederationRemotesAPI(tmpDir)

	// Without a running server and no remotes queryable, should get OK
	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for server not running (no remotes), got %s: %s", check.Status, check.Message)
	}
}

// noLiveDoltEndpoint points every port the endpoint probe resolves at a
// closed one, with no socket, no host override and an empty HOME. The cgo
// TestMain exports the Dolt test server's port in BEADS_DOLT_SERVER_PORT and
// BEADS_DOLT_PORT for the whole process, so without this a "no server" test
// probes that server and takes the running path.
func noLiveDoltEndpoint(t *testing.T) {
	t.Helper()
	closed := strconv.Itoa(closedLocalPort(t))
	t.Setenv("BEADS_DOLT_SERVER_PORT", closed)
	t.Setenv("BEADS_DOLT_PORT", closed)
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func TestDoltServerConfig_SuppressesCLIAutoStartWithConfiguredPort(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltDatabase:   "beads_test",
		DoltServerPort: 12345,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	result := doltServerConfig(beadsDir, filepath.Join(beadsDir, "dolt"))
	if result.AutoStart {
		t.Fatal("doltServerConfig should suppress CLI auto-start with an external configured server port")
	}
}

func TestDoltServerConfig_EnablesCLIAutoStartWithoutConfiguredPort(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend:      configfile.BackendDolt,
		DoltDatabase: "beads_test",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	result := doltServerConfig(beadsDir, filepath.Join(beadsDir, "dolt"))
	if !result.AutoStart {
		t.Fatal("doltServerConfig should enable CLI auto-start for owned standalone configs")
	}
}

func TestDoltServerConfig_HonorsAutoStartOptOut(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltDatabase:   "beads_test",
		DoltServerPort: 12345,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	result := doltServerConfig(beadsDir, filepath.Join(beadsDir, "dolt"))
	if result.AutoStart {
		t.Fatal("doltServerConfig should honor BEADS_DOLT_AUTO_START=0")
	}
}

func TestCheckFederationRemotesAPI_PidFileInBeadsDir(t *testing.T) {
	// Isolate from orchestrator daemon which would be detected as a running server
	t.Setenv("GT_ROOT", "")
	noLiveDoltEndpoint(t)

	// Verify the fix: PID file should be looked for in beadsDir, not doltPath.
	// The old code had: filepath.Join(doltPath, "dolt-server.pid") which was wrong.
	// The fix uses doltserver.IsRunning(beadsDir) which looks in beadsDir.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a dolt backend config
	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create a PID file in the WRONG location (doltPath) - this is where the
	// old buggy code looked. The new code should NOT detect this as server running.
	wrongPidFile := filepath.Join(doltDir, "dolt-server.pid")
	if err := os.WriteFile(wrongPidFile, []byte("99999"), 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckFederationRemotesAPI(tmpDir)

	// Server should NOT be detected as running (PID file is in wrong location)
	if check.Status == StatusError && strings.Contains(check.Message, "not accessible") {
		t.Errorf("PID file in doltPath should not be detected: old bug not fixed")
	}
}

func TestCheckFederationPeerConnectivity_NonDoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckFederationPeerConnectivity(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for non-Dolt backend, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "N/A") {
		t.Errorf("expected N/A message, got %q", check.Message)
	}
}

func TestCheckFederationPeerConnectivity_NoDoltDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckFederationPeerConnectivity(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for missing dolt database, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "no dolt database") {
		t.Errorf("expected message about no dolt database, got %q", check.Message)
	}
}

func TestCheckFederationSyncStaleness_NonDoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckFederationSyncStaleness(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for non-Dolt backend, got %s", check.Status)
	}
}

func TestCheckFederationConflicts_NonDoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckFederationConflicts(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for non-Dolt backend, got %s", check.Status)
	}
}

func TestCheckFederationConflicts_NoDoltDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckFederationConflicts(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for missing dolt database, got %s", check.Status)
	}
}

func TestCheckDoltServerModeMismatch_NonDoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckDoltServerModeMismatch(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for non-Dolt backend, got %s", check.Status)
	}
}

func TestCheckDoltServerModeMismatch_NoDoltDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckDoltServerModeMismatch(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for missing dolt database, got %s", check.Status)
	}
}

func TestCheckFederationRemotesAPI_ConfiguredPort(t *testing.T) {
	// Verify the fix: remotesapi port should be read from config, not hardcoded.
	// We can't test the actual port check (needs a running server), but we can
	// verify the config reading logic.
	cfg := &configfile.Config{
		DoltRemotesAPIPort: 9090,
	}

	port := cfg.GetDoltRemotesAPIPort()
	if port != 9090 {
		t.Errorf("expected port 9090 from config, got %d", port)
	}
}

func TestCheckFederationRemotesAPI_DefaultPort(t *testing.T) {
	cfg := &configfile.Config{}

	port := cfg.GetDoltRemotesAPIPort()
	if port != configfile.DefaultDoltRemotesAPIPort {
		t.Errorf("expected default port %d, got %d", configfile.DefaultDoltRemotesAPIPort, port)
	}
}

func TestCheckFederationRemotesAPI_EnvOverridesConfig(t *testing.T) {
	t.Setenv("BEADS_DOLT_REMOTESAPI_PORT", "7777")

	cfg := &configfile.Config{
		DoltRemotesAPIPort: 9090,
	}

	port := cfg.GetDoltRemotesAPIPort()
	if port != 7777 {
		t.Errorf("expected env override port 7777, got %d", port)
	}
}

// remotesAPITarget isolates the inputs checkRemotesAPIListener resolves from:
// the server mode, the user-global dolt.remotesapi-port ("" leaves it unset)
// and BEADS_DOLT_REMOTESAPI_PORT ("" leaves it unset).
func remotesAPITarget(t *testing.T, shared bool, userGlobalPort, envPort string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if userGlobalPort != "" {
		dir := filepath.Join(home, ".config", "bd")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		yaml := "dolt:\n  remotesapi-port: " + userGlobalPort + "\n"
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BEADS_DOLT_SHARED_SERVER", strconv.FormatBool(shared))
	t.Setenv("BEADS_DOLT_REMOTESAPI_PORT", envPort)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// localTCPPort returns a loopback port, still listening when open is true and
// already closed (so a dial is refused) otherwise.
func localTCPPort(t *testing.T, open bool) int {
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

// listenerEndpoint names the Dolt server endpoint checkRemotesAPIListener
// resolves for a server only the probe found: host ("" for the default) and
// port, with every other endpoint variable cleared.
func listenerEndpoint(t *testing.T, host string, port int) {
	t.Helper()
	t.Setenv("BEADS_DOLT_SERVER_HOST", host)
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", "")
}

// ipv6OnlyLoopbackPort is a port that accepts on [::1] while 127.0.0.1
// refuses it, so whether a dial succeeds shows which of the two hosts it went
// to. It skips the test when [::1] cannot listen or 127.0.0.1 also accepts.
func ipv6OnlyLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("cannot listen on [::1]: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	if conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
		_ = conn.Close()
		t.Skipf("127.0.0.1:%d also accepts, so the dial host is not observable", port)
	}
	return port
}

// TestCheckRemotesAPIListener covers the running-server arm of
// CheckFederationRemotesAPI, which the getter tests above never reach: the
// port it resolves and the remedy it prints for each server mode.
func TestCheckRemotesAPIListener(t *testing.T) {
	t.Run("shared server resolves the user-global port", func(t *testing.T) {
		port := localTCPPort(t, true)
		check := checkRemotesAPIListener(remotesAPITarget(t, true, strconv.Itoa(port), ""), 4242)
		if check.Status != StatusOK || check.Message != "Port "+strconv.Itoa(port)+" accessible" {
			t.Fatalf("got %s %q, want the user-global port %d accessible", check.Status, check.Message, port)
		}
	})

	t.Run("environment overrides the user-global port", func(t *testing.T) {
		port := localTCPPort(t, true)
		check := checkRemotesAPIListener(remotesAPITarget(t, true, strconv.Itoa(localTCPPort(t, false)), strconv.Itoa(port)), 4242)
		if check.Status != StatusOK || check.Message != "Port "+strconv.Itoa(port)+" accessible" {
			t.Fatalf("got %s %q, want the env port %d accessible", check.Status, check.Message, port)
		}
	})

	t.Run("unreachable shared server prescribes a restart", func(t *testing.T) {
		port := localTCPPort(t, false)
		check := checkRemotesAPIListener(remotesAPITarget(t, true, strconv.Itoa(port), ""), 4242)
		if check.Status != StatusError || !strings.Contains(check.Fix, "bd dolt stop && bd dolt start") {
			t.Fatalf("got %s, Fix %q; want StatusError with the stop/start remedy", check.Status, check.Fix)
		}
	})

	t.Run("unreachable per-project server names the flag, not a restart", func(t *testing.T) {
		port := localTCPPort(t, false)
		check := checkRemotesAPIListener(remotesAPITarget(t, false, "", strconv.Itoa(port)), 4242)
		if check.Status != StatusError {
			t.Fatalf("got %s, want StatusError", check.Status)
		}
		// bd never opens a listener for a per-project server, so a restart
		// cannot clear this error.
		if !strings.Contains(check.Fix, "--remotesapi-port "+strconv.Itoa(port)) || strings.Contains(check.Fix, "bd dolt stop") {
			t.Fatalf("Fix %q must name --remotesapi-port %d and must not prescribe a restart", check.Fix, port)
		}
	})

	t.Run("shared server without a port is disabled", func(t *testing.T) {
		check := checkRemotesAPIListener(remotesAPITarget(t, true, "", ""), 4242)
		if check.Status != StatusOK || check.Message != "N/A (remotesapi listener disabled)" || !strings.Contains(check.Detail, "shared dolt sql-server") {
			t.Fatalf("got %s %q, Detail %q; want the shared server's disabled listener", check.Status, check.Message, check.Detail)
		}
	})

	t.Run("unreachable server only the probe found names its endpoint, not PID 0", func(t *testing.T) {
		serverPort := localTCPPort(t, true)
		listenerEndpoint(t, "", serverPort)
		check := checkRemotesAPIListener(remotesAPITarget(t, false, "", strconv.Itoa(localTCPPort(t, false))), 0)
		want := "Server reachable at 127.0.0.1:" + strconv.Itoa(serverPort) + " (no PID file in this workspace names it) but remotesapi port unreachable: "
		if check.Status != StatusError || !strings.HasPrefix(check.Detail, want) || strings.Contains(check.Detail, "PID 0") {
			t.Fatalf("got %s, Detail %q; want StatusError with Detail starting %q", check.Status, check.Detail, want)
		}
	})

	// The probe takes the store's transport, a live socket before TCP, so
	// beside a live socket the port that resolved is not where it reached the
	// server.
	t.Run("unreachable server the probe found over its socket names the socket", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix sockets")
		}
		listenerEndpoint(t, "", localTCPPort(t, false))
		socket := liveUnixSocket(t)
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", socket)
		remotesAPIPort := strconv.Itoa(localTCPPort(t, false))
		check := checkRemotesAPIListener(remotesAPITarget(t, false, "", remotesAPIPort), 0)
		want := "Server reachable at unix:" + socket + " (no PID file in this workspace names it) but remotesapi port unreachable: dial tcp 127.0.0.1:" + remotesAPIPort + ": "
		if check.Status != StatusError || !strings.HasPrefix(check.Detail, want) {
			t.Fatalf("got %s, Detail %q; want StatusError with Detail starting %q", check.Status, check.Detail, want)
		}
	})

	t.Run("server the probe found over its socket is dialed at 127.0.0.1, not the configured host", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix sockets")
		}
		port := ipv6OnlyLoopbackPort(t)
		listenerEndpoint(t, "::1", localTCPPort(t, false))
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", liveUnixSocket(t))
		check := checkRemotesAPIListener(remotesAPITarget(t, false, "", strconv.Itoa(port)), 0)
		if check.Status != StatusError || !strings.Contains(check.Detail, "dial tcp 127.0.0.1:"+strconv.Itoa(port)+": ") {
			t.Fatalf("got %s %q (%s), want StatusError from a dial of 127.0.0.1:%d: the probe reached the socket, not [::1]",
				check.Status, check.Message, check.Detail, port)
		}
	})

	t.Run("unreachable PID-tracked server names its PID", func(t *testing.T) {
		listenerEndpoint(t, "", localTCPPort(t, true))
		check := checkRemotesAPIListener(remotesAPITarget(t, false, "", strconv.Itoa(localTCPPort(t, false))), 4242)
		want := "Server running (PID 4242) but remotesapi port unreachable: "
		if check.Status != StatusError || !strings.HasPrefix(check.Detail, want) {
			t.Fatalf("got %s, Detail %q; want StatusError with Detail starting %q", check.Status, check.Detail, want)
		}
	})

	t.Run("server only the probe found is dialed at its configured host", func(t *testing.T) {
		port := ipv6OnlyLoopbackPort(t)
		listenerEndpoint(t, "::1", localTCPPort(t, false))
		beadsDir := remotesAPITarget(t, false, "", strconv.Itoa(port))

		check := checkRemotesAPIListener(beadsDir, 0)
		if check.Status != StatusOK || check.Message != "Port "+strconv.Itoa(port)+" accessible" {
			t.Fatalf("pid 0: got %s %q (%s), want the listener on [::1]:%d accessible", check.Status, check.Message, check.Detail, port)
		}
		// bd's own server binds 127.0.0.1, where nothing listens.
		if check := checkRemotesAPIListener(beadsDir, 4242); check.Status != StatusError {
			t.Fatalf("pid 4242: got %s %q, want StatusError from a dial of 127.0.0.1:%d", check.Status, check.Message, port)
		}
	})

	t.Run("server only the probe found keeps the shared server's restart remedy", func(t *testing.T) {
		listenerEndpoint(t, "", localTCPPort(t, true))
		check := checkRemotesAPIListener(remotesAPITarget(t, true, strconv.Itoa(localTCPPort(t, false)), ""), 0)
		if check.Status != StatusError || !strings.Contains(check.Fix, "bd dolt stop && bd dolt start") {
			t.Fatalf("got %s, Fix %q; want StatusError with the stop/start remedy", check.Status, check.Fix)
		}
	})

	t.Run("server only the probe found keeps the disabled listener", func(t *testing.T) {
		listenerEndpoint(t, "", localTCPPort(t, true))
		check := checkRemotesAPIListener(remotesAPITarget(t, true, "", ""), 0)
		if check.Status != StatusOK || check.Message != "N/A (remotesapi listener disabled)" {
			t.Fatalf("got %s %q; want the disabled listener", check.Status, check.Message)
		}
	})

	t.Run("per-project port zero is disabled without naming a shared server", func(t *testing.T) {
		check := checkRemotesAPIListener(remotesAPITarget(t, false, "", "0"), 4242)
		if check.Status != StatusOK || check.Message != "N/A (remotesapi listener disabled)" || strings.Contains(check.Detail, "shared") {
			t.Fatalf("got %s %q, Detail %q; want a disabled listener that does not claim a shared server", check.Status, check.Message, check.Detail)
		}
	})
}

func TestCheckFederationChecks_CategoryIsFederation(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a sqlite config so all checks return quickly with N/A
	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name string
		fn   func(string) DoctorCheck
	}{
		{"RemotesAPI", CheckFederationRemotesAPI},
		{"PeerConnectivity", CheckFederationPeerConnectivity},
		{"SyncStaleness", CheckFederationSyncStaleness},
		{"Conflicts", CheckFederationConflicts},
		{"LegacyCLIRemotes", CheckLegacyCLIRemotes},
		{"ServerModeMismatch", CheckDoltServerModeMismatch},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			check := tc.fn(tmpDir)
			if check.Category != CategoryFederation {
				t.Errorf("%s: expected CategoryFederation, got %q", tc.name, check.Category)
			}
		})
	}
}

func TestDoltServerConfig_PopulatesFromConfig(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltServerHost: "192.168.1.10",
		DoltServerUser: "testuser",
		DoltDatabase:   "mydb",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	result := doltServerConfig(beadsDir, doltDir)

	if result.Path != doltDir {
		t.Errorf("expected Path %q, got %q", doltDir, result.Path)
	}
	if !result.ReadOnly {
		t.Error("expected ReadOnly=true")
	}
	if result.Database != "mydb" {
		t.Errorf("expected Database 'mydb', got %q", result.Database)
	}
	if result.ServerHost != "192.168.1.10" {
		t.Errorf("expected ServerHost '192.168.1.10', got %q", result.ServerHost)
	}
	if result.ServerUser != "testuser" {
		t.Errorf("expected ServerUser 'testuser', got %q", result.ServerUser)
	}
}

func TestCheckLegacyCLIRemotesDetectsServerRootOnlyRemote(t *testing.T) {
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skipf("dolt binary not available: %v", err)
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	cliDir := filepath.Join(doltDir, testSharedDB)
	if err := os.MkdirAll(cliDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       "server",
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		DoltDatabase:   testSharedDB,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	runDoctorTestCmd(t, doltDir, "dolt", "init", "--name", "test", "--email", "test@test.com")
	runDoctorTestCmd(t, cliDir, "dolt", "init", "--name", "test", "--email", "test@test.com")
	rootOnlyURL := "file:///tmp/root-only-remote.git"
	runDoctorTestCmd(t, doltDir, "dolt", "remote", "add", "rootonly", rootOnlyURL)

	check := CheckLegacyCLIRemotes(tmpDir)
	if check.Status != StatusWarning {
		t.Fatalf("expected StatusWarning for root-only legacy remote, got %s: %s\nDetail: %s", check.Status, check.Message, check.Detail)
	}
	if !strings.Contains(check.Detail, "Dolt server root") {
		t.Fatalf("expected detail to identify Dolt server root, got: %s", check.Detail)
	}
	if !strings.Contains(check.Detail, "rootonly="+rootOnlyURL) &&
		!strings.Contains(check.Detail, "rootonly=git+"+rootOnlyURL) {
		t.Fatalf("expected root-only remote in detail, got: %s", check.Detail)
	}
}

func TestDoltDatabaseName_Default(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// No config file — should fall back to default
	name := doltDatabaseName(beadsDir)
	if name != configfile.DefaultDoltDatabase {
		t.Errorf("expected default %q, got %q", configfile.DefaultDoltDatabase, name)
	}
}

func TestDoltDatabaseName_FromConfig(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		DoltDatabase: "custom_db",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	name := doltDatabaseName(beadsDir)
	if name != "custom_db" {
		t.Errorf("expected 'custom_db', got %q", name)
	}
}

func runDoctorTestCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed in %s: %v\nOutput: %s", name, args, dir, err, out)
	}
}

// TestCheckFederationRemotesAPI_ServerRunningNoPeers verifies that when the
// Dolt server is running but no federation peers are configured, the check
// returns StatusOK instead of erroring about the remotesapi port.
// This is the bug described in GH#2273.
func TestCheckFederationRemotesAPI_ServerRunningNoPeers(t *testing.T) {
	// Isolate from orchestrator daemon — we'll simulate "server running" via
	// a standalone PID file pointing at a real dolt process on the host.
	t.Setenv("GT_ROOT", "")

	// Find a running dolt process on the host to use for the PID file.
	// This makes doltserver.IsRunning() return true via the standalone path.
	doltPIDs := findDoltPIDs(t)
	if len(doltPIDs) == 0 {
		t.Skip("no host dolt process running (needed to simulate server-running state)")
	}

	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write PID file so doltserver.IsRunning detects "server running"
	pidFile := filepath.Join(beadsDir, "dolt-server.pid")
	if err := os.WriteFile(pidFile, []byte(doltPIDs[0]), 0o600); err != nil {
		t.Fatal(err)
	}

	// Write config pointing at the testcontainers server with the shared DB.
	// BEADS_DOLT_PORT (set by TestMain) routes dolt.New() to testcontainers.
	cfg := &configfile.Config{
		Backend:      configfile.BackendDolt,
		DoltDatabase: testSharedDB,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckFederationRemotesAPI(tmpDir)

	// With a running server and no federation peers, the fix returns OK
	// instead of the old false error about remotesapi port 8080.
	if check.Status == StatusError {
		t.Errorf("GH#2273: server running with no peers should not report error, got: %s: %s\n  Detail: %s",
			check.Status, check.Message, check.Detail)
	}
	if check.Status == StatusOK && !strings.Contains(check.Message, "no federation peers") {
		t.Logf("got StatusOK with message: %s (expected 'no federation peers configured')", check.Message)
	}
}

// findDoltPIDs returns PIDs of running dolt sql-server processes on the host.
func findDoltPIDs(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", "dolt sql-server").Output()
	if err != nil {
		return nil
	}
	var pids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			pids = append(pids, line)
		}
	}
	return pids
}

// TestCheckFederationRemotesAPI_AllCheckNames verifies all federation checks
// return meaningful check names (not empty strings).
func TestCheckFederationRemotesAPI_AllCheckNames(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		fn       func(string) DoctorCheck
		wantName string
	}{
		{CheckFederationRemotesAPI, "Federation remotesapi"},
		{CheckFederationPeerConnectivity, "Peer Connectivity"},
		{CheckFederationSyncStaleness, "Sync Staleness"},
		{CheckFederationConflicts, "Federation Conflicts"},
		{CheckLegacyCLIRemotes, "Dolt Remote Migration"},
		{CheckDoltServerModeMismatch, "Dolt Mode"},
	}

	for _, tc := range checks {
		check := tc.fn(tmpDir)
		if check.Name != tc.wantName {
			t.Errorf("expected Name %q, got %q", tc.wantName, check.Name)
		}
	}
}

// probeOnlyServerWorkspace is a server-mode workspace whose database is a
// fresh one on the Dolt test server at port, holding the named remotes, with
// a .beads/dolt directory and no dolt-server.pid: the shape of a server bd
// did not start (launchd, systemd, a container). Nothing names the server's
// port yet; the caller sets the variable under test. The remotesapi port is
// closed.
func probeOnlyServerWorkspace(t *testing.T, port int, remotes ...string) (repo, beadsDir string) {
	t.Helper()
	repo, beadsDir = doctorEndpointWorkspace(t)
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DOLT_REMOTESAPI_PORT", strconv.Itoa(closedLocalPort(t)))
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	dbName := "doctest_fedprobe_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	t.Cleanup(func() { dropDoctorTestDatabase(dbName, port) })
	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            t.TempDir(),
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create %s: %v", dbName, err)
	}
	for _, name := range remotes {
		if err := store.AddRemote(ctx, name, "file:///tmp/"+dbName+"-"+name); err != nil {
			_ = store.Close()
			t.Fatalf("add remote %s: %v", name, err)
		}
	}
	_ = store.Close()

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DoltDatabase = dbName
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	return repo, beadsDir
}

// unixSocketRelay is a unix socket that relays every connection to the TCP
// server at 127.0.0.1:port, so the endpoint probe and the store reach the
// Dolt test server over a socket. Its path is short enough to bind, as
// liveUnixSocket's is.
func unixSocketRelay(t *testing.T, port int) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "dolt.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = client.Close() }()
				server, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					return
				}
				defer func() { _ = server.Close() }()
				// Either side closing ends the relay; the deferred closes
				// then unblock the other copy.
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(server, client); done <- struct{}{} }()
				go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
				<-done
			}()
		}
	}()
	return path
}

// TestCheckFederationRemotesAPI_ProbeOnlyServer pins the Federation
// remotesapi check for a live server no dolt-server.pid names. The check read
// the PID file alone, so it took its no-server branch, listed the remotes on
// that very server, and warned "Server not running (N peers configured)",
// counting origin. It now probes the endpoint the store opens
// (doltserver.ResolveEndpoint, the legacy BEADS_DOLT_PORT included) and takes
// the running path.
func TestCheckFederationRemotesAPI_ProbeOnlyServer(t *testing.T) {
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available")
	}

	// A server only the probe found, with federation peers and nothing on
	// the remotesapi port: the listener's real state, naming the endpoint the
	// probe reached. A directory at the PID path makes IsRunning return a nil
	// state.
	for _, tc := range []struct {
		name     string
		pidAsDir bool
	}{
		{name: "peers, no PID file"},
		{name: "peers, unreadable PID file", pidAsDir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, beadsDir := probeOnlyServerWorkspace(t, port, "origin", "peer1")
			t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
			if tc.pidAsDir {
				if err := os.Mkdir(filepath.Join(beadsDir, "dolt-server.pid"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			remotesAPIPort := os.Getenv("BEADS_DOLT_REMOTESAPI_PORT")
			check := CheckFederationRemotesAPI(repo)
			wantDetail := "Server reachable at 127.0.0.1:" + strconv.Itoa(port) + " (no PID file in this workspace names it)"
			if check.Status != StatusError || check.Message != "remotesapi port "+remotesAPIPort+" not accessible" ||
				!strings.Contains(check.Detail, wantDetail) || strings.Contains(check.Detail, "PID 0") {
				t.Fatalf("live server at %d with a peer: got %s %q (%s), want error %q with Detail containing %q",
					port, check.Status, check.Message, check.Detail, "remotesapi port "+remotesAPIPort+" not accessible", wantDetail)
			}
		})
	}

	// The same server reached over a live socket beside a closed port, at a
	// configured host whose remotesapi port accepts: the check reads the
	// peers through the socket, names it, and dials the listener at
	// 127.0.0.1, where nothing listens, never at the host the probe did not
	// reach.
	t.Run("peers, reached over a live socket beside a closed port", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix sockets")
		}
		repo, _ := probeOnlyServerWorkspace(t, port, "origin", "peer1")
		remotesAPIPort := strconv.Itoa(ipv6OnlyLoopbackPort(t))
		t.Setenv("BEADS_DOLT_REMOTESAPI_PORT", remotesAPIPort)
		t.Setenv("BEADS_DOLT_SERVER_HOST", "::1")
		t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(closedLocalPort(t)))
		socket := unixSocketRelay(t, port)
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", socket)
		check := CheckFederationRemotesAPI(repo)
		wantDetail := "Server reachable at unix:" + socket + " (no PID file in this workspace names it) but remotesapi port unreachable: dial tcp 127.0.0.1:" + remotesAPIPort + ": "
		if check.Status != StatusError || !strings.HasPrefix(check.Detail, wantDetail) {
			t.Fatalf("live server over %s with a peer: got %s %q (%s), want error with Detail starting %q",
				socket, check.Status, check.Message, check.Detail, wantDetail)
		}
	})

	// A server only the probe found whose store does not open. The probe
	// counts a TCP accept, which proves neither a server that takes the
	// workspace's user nor one holding its database: no peer was read, so
	// the check reports the failed open, not the closed remotesapi port.
	// TestCheckFederationRemotesAPI_AcceptOnlyListener covers a listener
	// that is not a Dolt server.
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, beadsDir string)
	}{
		{name: "workspace database missing on the server", setup: func(t *testing.T, beadsDir string) {
			cfg, err := configfile.Load(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			cfg.DoltDatabase = "doctest_fedprobe_absent_" + strconv.FormatInt(time.Now().UnixNano(), 16)
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "server refuses the workspace's user", setup: func(t *testing.T, _ string) {
			t.Setenv("BEADS_DOLT_SERVER_USER", "doctest_nobody")
		}},
	} {
		t.Run("peers, store does not open: "+tc.name, func(t *testing.T) {
			repo, beadsDir := probeOnlyServerWorkspace(t, port, "origin", "peer1")
			t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
			tc.setup(t, beadsDir)
			check := CheckFederationRemotesAPI(repo)
			if check.Status != StatusOK || check.Message != "Skipped (database unavailable)" || check.Detail == "" {
				t.Fatalf("live server at %d whose store does not open: got %s %q (%s), want ok %q with the open's error as Detail",
					port, check.Status, check.Message, check.Detail, "Skipped (database unavailable)")
			}
		})
	}

	for _, env := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		t.Run("origin only, endpoint from "+env, func(t *testing.T) {
			repo, _ := probeOnlyServerWorkspace(t, port, "origin")
			t.Setenv(env, strconv.Itoa(port))
			check := CheckFederationRemotesAPI(repo)
			if check.Status != StatusOK || check.Message != "N/A (no federation peers configured)" {
				t.Fatalf("live server at %s=%d with only origin: got %s %q (%s), want ok %q",
					env, port, check.Status, check.Message, check.Detail, "N/A (no federation peers configured)")
			}
		})
	}
}
