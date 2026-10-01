//go:build cgo

package doctor

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
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
	// The check resolves the shared server when the ambient environment
	// enables shared mode; this test inspects the per-project layout.
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
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
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

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

// TestCheckFederationRemotesAPI_DoesNotStartServer pins that the remotesapi
// check diagnoses the server that is running and never starts one, even where
// doltServerConfig's CLI policy (used by the other federation checks) would
// auto-start an owned standalone server.
func TestCheckFederationRemotesAPI_DoesNotStartServer(t *testing.T) {
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltDatabase: "beads_test"}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Precondition: the shared config path would auto-start here.
	if !doltServerConfig(beadsDir, doltDir).AutoStart {
		t.Fatal("precondition: doltServerConfig should enable CLI auto-start for this workspace")
	}
	target, err := resolveFederationRemotesAPITarget(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if sqlCfg := federationTargetSQLConfig(beadsDir, target); sqlCfg.AutoStart || !sqlCfg.DisableAutoStart {
		t.Fatalf("remotesapi target config = AutoStart %v DisableAutoStart %v, want no auto-start", sqlCfg.AutoStart, sqlCfg.DisableAutoStart)
	}

	check := CheckFederationRemotesAPI(tmpDir)
	if check.Status != StatusOK || !strings.Contains(check.Message, "server not running") {
		t.Fatalf("got %s %q, want OK for a stopped server", check.Status, check.Message)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, doltserver.PIDFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("the check started a server: pidfile stat = %v", statErr)
	}
	if state, runErr := doltserver.IsRunning(beadsDir); runErr != nil || (state != nil && state.Running) {
		t.Fatalf("server state after check = %+v (err %v), want not running", state, runErr)
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
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

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

// remotesAPITarget isolates the inputs resolveFederationRemotesAPITarget reads:
// the server mode, the user-global dolt.remotesapi-port ("" leaves it unset)
// and BEADS_DOLT_REMOTESAPI_PORT ("" leaves it unset).
func remotesAPITarget(t *testing.T, shared bool, userGlobalPort, envPort string) federationRemotesAPITarget {
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
	target, err := resolveFederationRemotesAPITarget(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	return target
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
		if check.Status != StatusError || !strings.Contains(check.Fix, "bd dolt restart") || strings.Contains(check.Fix, "bd dolt stop") {
			t.Fatalf("got %s, Fix %q; want StatusError with the fenced restart remedy", check.Status, check.Fix)
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
		if !strings.Contains(check.Fix, "--remotesapi-port "+strconv.Itoa(port)) || strings.Contains(check.Fix, "restart") {
			t.Fatalf("Fix %q must name --remotesapi-port %d and must not prescribe a restart", check.Fix, port)
		}
	})

	t.Run("shared server without a port is disabled", func(t *testing.T) {
		check := checkRemotesAPIListener(remotesAPITarget(t, true, "", ""), 4242)
		if check.Status != StatusOK || check.Message != "N/A (remotesapi listener disabled)" || !strings.Contains(check.Detail, "shared dolt sql-server") {
			t.Fatalf("got %s %q, Detail %q; want the shared server's disabled listener", check.Status, check.Message, check.Detail)
		}
	})

	t.Run("per-project port zero is disabled without naming a shared server", func(t *testing.T) {
		check := checkRemotesAPIListener(remotesAPITarget(t, false, "", "0"), 4242)
		if check.Status != StatusOK || check.Message != "N/A (remotesapi listener disabled)" || strings.Contains(check.Detail, "shared") {
			t.Fatalf("got %s %q, Detail %q; want a disabled listener that does not claim a shared server", check.Status, check.Message, check.Detail)
		}
	})
}

// TestResolveFederationRemotesAPITargetUsesTargetPaths pins that a shared-mode
// workspace is diagnosed against the shared server's data directory, pidfile
// directory, SQL port and user-global remotesapi port, while a per-project
// workspace keeps its own paths and configfile port.
func TestResolveFederationRemotesAPITargetUsesTargetPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_REMOTESAPI_PORT", "")
	sharedRoot := filepath.Join(home, ".beads", "shared-server")
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedRoot)
	if err := os.MkdirAll(filepath.Join(sharedRoot, "dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	activeDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(activeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeDir, "config.yaml"),
		[]byte("dolt:\n  shared-server: false\n  port: 16666\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", activeDir)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.SetUserYamlConfig("dolt.remotesapi-port", "8123"); err != nil {
		t.Fatal(err)
	}
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}

	nonSharedDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(nonSharedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonSharedDir, "config.yaml"), []byte("dolt:\n  shared-server: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonSharedCfg := configfile.DefaultConfig()
	nonSharedCfg.DoltMode = configfile.DoltModeServer
	nonSharedCfg.DoltServerPort = 15555
	nonSharedCfg.DoltRemotesAPIPort = 7001
	if err := nonSharedCfg.Save(nonSharedDir); err != nil {
		t.Fatal(err)
	}
	nonShared, err := resolveFederationRemotesAPITarget(nonSharedDir)
	if err != nil {
		t.Fatal(err)
	}
	wantNonSharedData := filepath.Join(nonSharedDir, "dolt")
	nonSharedSQL := federationTargetSQLConfig(nonSharedDir, nonShared)
	if nonShared.SharedMode ||
		nonShared.DoltPath != wantNonSharedData ||
		nonShared.ServerDir != nonSharedDir ||
		nonSharedSQL.ServerPort != 15555 ||
		nonSharedSQL.ServerPortSource != doltserver.PortSourceMetadataJSON ||
		nonSharedSQL.ServerPortSharedServer ||
		nonShared.RemotesAPIPort != 7001 {
		t.Fatalf("non-shared target = %+v sql=%+v, want data:%q state:%q sql:15555 shared-provenance:false rapi:7001", nonShared, nonSharedSQL, wantNonSharedData, nonSharedDir)
	}

	sharedDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(sharedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared, err := resolveFederationRemotesAPITarget(sharedDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sharedDir, "dolt")); !os.IsNotExist(err) {
		t.Fatalf("precondition: shared project unexpectedly has local dolt dir: %v", err)
	}
	sharedSQL := federationTargetSQLConfig(sharedDir, shared)
	if !shared.SharedMode ||
		shared.DoltPath != filepath.Join(sharedRoot, "dolt") ||
		shared.ServerDir != sharedRoot ||
		sharedSQL.ServerHost != "127.0.0.1" ||
		sharedSQL.ServerPort != doltserver.DefaultSharedServerPort ||
		sharedSQL.ServerPortSource != doltserver.PortSourceSharedServerDefault ||
		!sharedSQL.ServerPortSharedServer ||
		shared.RemotesAPIPort != 8123 {
		t.Fatalf("shared target = %+v sql=%+v, want data:%q state:%q sql:127.0.0.1:%d shared-provenance:true rapi:8123", shared, sharedSQL, filepath.Join(sharedRoot, "dolt"), sharedRoot, doltserver.DefaultSharedServerPort)
	}
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
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

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
