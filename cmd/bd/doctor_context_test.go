package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/utils"
)

func savePersistentPreRunState(t *testing.T) {
	t.Helper()

	oldServerMode := serverMode
	oldCmdCtx := cmdCtx
	oldDBPath := dbPath
	oldActor := actor
	oldJSONOutput := jsonOutput
	oldReadonlyMode := readonlyMode
	oldDoltAutoCommit := doltAutoCommit
	flagState := snapshotRootFlagState()
	t.Cleanup(func() {
		serverMode = oldServerMode
		cmdCtx = oldCmdCtx
		dbPath = oldDBPath
		actor = oldActor
		jsonOutput = oldJSONOutput
		readonlyMode = oldReadonlyMode
		doltAutoCommit = oldDoltAutoCommit
		restoreRootFlagState(t, flagState)
	})

	serverMode = false
	cmdCtx = nil
	dbPath = ""
	actor = ""
	jsonOutput = false
	readonlyMode = false
	doltAutoCommit = ""
}

func writeMetadataConfig(t *testing.T, beadsDir string, doltMode string, database string) {
	t.Helper()

	if err := (&configfile.Config{
		Backend:      configfile.BackendDolt,
		DoltMode:     doltMode,
		DoltDatabase: database,
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
}

func TestPersistentPreRunUsesTrustedPluginForPluginBackend(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "doltlite"), 0o755); err != nil {
		t.Fatalf("mkdir beadsDir: %v", err)
	}
	if err := (&configfile.Config{
		Backend:      "doltlite",
		Database:     "doltlite",
		DoltDatabase: "plugin_ctx_test",
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	capture := filepath.Join(t.TempDir(), "plugin-open.jsonl")
	plugin := writeStoreFactoryPluginHelper(t, capture)
	if _, err := saveBackendPluginTrust(beadsDir, "doltlite", plugin, []string{"serve"}); err != nil {
		t.Fatalf("saveBackendPluginTrust: %v", err)
	}

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}
	if err := rootCmd.PersistentPreRunE(createCmd, []string{"plugin-backed create"}); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
			store = nil
		}
	})

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"method":"open"`) {
		t.Fatalf("plugin did not receive open request:\n%s", text)
	}
	if !strings.Contains(text, `"database":"plugin_ctx_test"`) {
		t.Fatalf("plugin open did not use configured database:\n%s", text)
	}
	if store == nil {
		t.Fatal("PersistentPreRunE did not install active store")
	}
}

func TestDoctorPersistentPreRunLoadsServerModeForNoDBCommand(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	writeMetadataConfig(t, beadsDir, configfile.DoltModeServer, "doctor_ctx_test")

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}
	if err := rootCmd.PersistentPreRunE(doctorCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}

	if !serverMode {
		t.Fatal("doctor should load server mode before the no-store early return")
	}
}

func TestDoctorPersistentPreRunUsesExplicitDBTarget(t *testing.T) {
	callerRepo := filepath.Join(t.TempDir(), "caller")
	callerBeadsDir := filepath.Join(callerRepo, ".beads")
	writeTestConfigYAML(t, callerBeadsDir, "")
	writeMetadataConfig(t, callerBeadsDir, configfile.DoltModeEmbedded, "caller_ctx_test")

	targetRepo := filepath.Join(t.TempDir(), "target")
	targetBeadsDir := filepath.Join(targetRepo, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")
	writeMetadataConfig(t, targetBeadsDir, configfile.DoltModeServer, "target_ctx_test")

	t.Chdir(callerRepo)
	t.Setenv("BEADS_DIR", callerBeadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	targetDBPath := filepath.Join(targetBeadsDir, "dolt")
	dbPath = targetDBPath
	if flag := rootCmd.PersistentFlags().Lookup("db"); flag != nil {
		flag.Changed = true
	}

	if err := rootCmd.PersistentPreRunE(doctorCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}

	if got := os.Getenv("BEADS_DIR"); got != targetBeadsDir {
		t.Fatalf("BEADS_DIR = %q, want %q", got, targetBeadsDir)
	}
	if !serverMode {
		t.Fatal("doctor should use the explicit target repo's server mode")
	}
}

func TestBootstrapPersistentPreRunUsesExplicitDBTarget(t *testing.T) {
	callerRepo := filepath.Join(t.TempDir(), "caller")
	callerBeadsDir := filepath.Join(callerRepo, ".beads")
	writeTestConfigYAML(t, callerBeadsDir, "")
	writeMetadataConfig(t, callerBeadsDir, configfile.DoltModeEmbedded, "caller_bootstrap_test")

	targetRepo := filepath.Join(t.TempDir(), "target")
	targetBeadsDir := filepath.Join(targetRepo, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")
	writeMetadataConfig(t, targetBeadsDir, configfile.DoltModeServer, "target_bootstrap_test")

	t.Chdir(callerRepo)
	t.Setenv("BEADS_DIR", callerBeadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	targetDBPath := filepath.Join(targetBeadsDir, "dolt")
	dbPath = targetDBPath
	if flag := rootCmd.PersistentFlags().Lookup("db"); flag != nil {
		flag.Changed = true
	}

	if err := rootCmd.PersistentPreRunE(bootstrapCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}

	if got := os.Getenv("BEADS_DIR"); got != targetBeadsDir {
		t.Fatalf("BEADS_DIR = %q, want %q", got, targetBeadsDir)
	}
}

func TestLoadSelectionEnvironmentUsesAmbientEnvFileForBEADSDB(t *testing.T) {
	callerRepo := filepath.Join(t.TempDir(), "caller")
	callerBeadsDir := filepath.Join(callerRepo, ".beads")
	writeTestConfigYAML(t, callerBeadsDir, "")

	targetRepo := filepath.Join(t.TempDir(), "target")
	targetBeadsDir := filepath.Join(targetRepo, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")
	targetDBPath := filepath.Join(targetBeadsDir, "dolt")
	if err := os.MkdirAll(targetDBPath, 0o700); err != nil {
		t.Fatalf("mkdir target db dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(callerBeadsDir, ".env"), []byte("BEADS_DB="+targetDBPath+"\n"), 0o600); err != nil {
		t.Fatalf("write caller .env: %v", err)
	}

	t.Chdir(callerRepo)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")

	loadSelectionEnvironment()

	if got := os.Getenv("BEADS_DB"); utils.CanonicalizePath(got) != utils.CanonicalizePath(targetDBPath) {
		t.Fatalf("BEADS_DB = %q, want %q", got, targetDBPath)
	}
	if got := beads.FindDatabasePath(); utils.CanonicalizePath(got) != utils.CanonicalizePath(targetDBPath) {
		t.Fatalf("FindDatabasePath() = %q, want %q", got, targetDBPath)
	}
}
func TestSelectedDoltBeadsDirUsesReboundBEADSDir(t *testing.T) {
	callerRepo := filepath.Join(t.TempDir(), "caller")
	callerBeadsDir := filepath.Join(callerRepo, ".beads")
	writeTestConfigYAML(t, callerBeadsDir, "")

	targetRepo := filepath.Join(t.TempDir(), "target")
	targetBeadsDir := filepath.Join(targetRepo, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")
	targetDBPath := filepath.Join(targetBeadsDir, "dolt")
	if err := os.MkdirAll(targetDBPath, 0o700); err != nil {
		t.Fatalf("mkdir target db dir: %v", err)
	}

	t.Chdir(callerRepo)
	t.Setenv("BEADS_DIR", targetBeadsDir)
	t.Setenv("BEADS_DB", filepath.Join(callerBeadsDir, "dolt"))

	if got := selectedDoltBeadsDir(); utils.CanonicalizePath(got) != utils.CanonicalizePath(targetBeadsDir) {
		t.Fatalf("selectedDoltBeadsDir() = %q, want %q", got, targetBeadsDir)
	}
}
