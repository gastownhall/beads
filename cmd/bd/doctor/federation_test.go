package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestCheckFederationRemotesAPI_NonDoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a config with sqlite backend
	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a dolt backend config but don't create the dolt directory
	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
	// Isolate from Gas Town daemon which would be detected as a running server
	t.Setenv("GT_ROOT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a dolt backend config
	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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

func TestCheckFederationRemotesAPI_PidFileInBeadsDir(t *testing.T) {
	// Isolate from Gas Town daemon which would be detected as a running server
	t.Setenv("GT_ROOT", "")

	// Verify the fix: PID file should be looked for in beadsDir, not doltPath.
	// The old code had: filepath.Join(doltPath, "dolt-server.pid") which was wrong.
	// The fix uses doltserver.IsRunning(beadsDir) which looks in beadsDir.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a dolt backend config
	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Create a PID file in the WRONG location (doltPath) - this is where the
	// old buggy code looked. The new code should NOT detect this as server running.
	wrongPidFile := filepath.Join(doltDir, "dolt-server.pid")
	if err := os.WriteFile(wrongPidFile, []byte("99999"), 0600); err != nil {
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: configfile.BackendDolt,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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

func TestCheckFederationChecks_CategoryIsFederation(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a sqlite config so all checks return quickly with N/A
	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltServerHost: "192.168.1.10",
		DoltServerUser: "testuser",
		DoltDatabase:   "mydb",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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

func TestDoltDatabaseName_Default(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		DoltDatabase: "custom_db",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	name := doltDatabaseName(beadsDir)
	if name != "custom_db" {
		t.Errorf("expected 'custom_db', got %q", name)
	}
}

// TestCheckFederationRemotesAPI_AllCheckNames verifies all federation checks
// return meaningful check names (not empty strings).
func TestCheckFederationRemotesAPI_AllCheckNames(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Backend: "sqlite",
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
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
		{CheckDoltServerModeMismatch, "Dolt Mode"},
	}

	for _, tc := range checks {
		check := tc.fn(tmpDir)
		if check.Name != tc.wantName {
			t.Errorf("expected Name %q, got %q", tc.wantName, check.Name)
		}
	}
}
