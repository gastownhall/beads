package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/utils"
)

func savePersistentPreRunState(t *testing.T) {
	t.Helper()

	// Track env vars that PersistentPreRun may modify via prepareSelectedCommandContext
	// or applyChangeDirSelection. Without this, a call to PersistentPreRun leaves
	// BEADS_DIR set to the project's .beads dir, corrupting subsequent tests.
	for _, key := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB"} {
		t.Setenv(key, os.Getenv(key))
	}

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

// TestDoctorPersistentPreRunPreservesSourceDatabaseAcrossRedirect covers
// be-xil: bd doctor reported "Dolt Schema: wrong database" and offered a
// destructive --fix that would have repointed the rig at a different rig's
// store entirely.
//
// Root cause: selectedNoDBBeadsDir (used by doctor/bootstrap/context/dolt)
// resolves an ambient BEADS_DIR pointing at a redirect stub via
// beads.FindBeadsDir(), which always follows the redirect internally before
// returning. By the time prepareSelectedNoDBContext's own internal
// preserveRedirectSourceDatabase call sees a beadsDir, it's already the
// redirect TARGET (no redirect file of its own), so
// beads.ResolveRedirect never detects a redirect was followed and the
// source repo's configured dolt_database is silently never captured into
// BEADS_DOLT_SERVER_DATABASE. doctor's Dolt health checks resolve their own
// beadsDir independently (beads.ResolveBeadsDirForRepo, which also always
// follows the redirect) but still open the store via
// configfile.Config.GetDoltDatabase(), which checks BEADS_DOLT_SERVER_DATABASE
// before the loaded config's raw field — so this env var is the only thing
// standing between doctor and the shared target's own default database.
//
// The fix calls preserveRedirectSourceDatabase(beads.GetRedirectInfo().LocalDir)
// explicitly with the SOURCE dir before selectedNoDBBeadsDir resolves and
// loses that information; the function's own idempotency guard (early return
// once the env var is set) makes prepareSelectedNoDBContext's later internal
// call a harmless no-op.
func TestDoctorPersistentPreRunPreservesSourceDatabaseAcrossRedirect(t *testing.T) {
	sourceRepo := filepath.Join(t.TempDir(), "source")
	sourceBeadsDir := filepath.Join(sourceRepo, ".beads")
	writeTestConfigYAML(t, sourceBeadsDir, "")
	writeMetadataConfig(t, sourceBeadsDir, configfile.DoltModeServer, "source_db")

	targetBeadsDir := filepath.Join(t.TempDir(), "shared", ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")
	writeMetadataConfig(t, targetBeadsDir, configfile.DoltModeServer, "target_default_db")

	if err := os.WriteFile(filepath.Join(sourceBeadsDir, "redirect"), []byte(targetBeadsDir), 0o600); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	t.Chdir(sourceRepo)
	t.Setenv("BEADS_DIR", sourceBeadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	if err := rootCmd.PersistentPreRunE(doctorCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}

	if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "source_db" {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want %q (source database lost across redirect — be-xil regression)", got, "source_db")
	}
}

// TestDoctorPersistentPreRunExplicitDBTargetOverridesAmbientRedirect covers
// be-fyt round 1: the be-xil fix above called preserveRedirectSourceDatabase
// unconditionally in the skipsStoreInit branch (main.go ~1168), unlike its
// dbPath-guarded sibling at ~1223. beads.GetRedirectInfo() always resolves
// from the ambient CWD repo's local .beads regardless of --db/BEADS_DIR
// (bd-wayc3), so when the ambient repo itself has an active redirect AND the
// caller passes an explicit --db target, the ambient repo's redirect-source
// database silently shadowed the explicit target's own configured database —
// reopening be-xil's exact "wrong database" failure mode via a narrower
// trigger. Neither TestDoctorPersistentPreRunUsesExplicitDBTarget (no
// ambient redirect) nor TestDoctorPersistentPreRunPreservesSourceDatabaseAcrossRedirect
// (no explicit --db target) exercises the combination; this test does.
func TestDoctorPersistentPreRunExplicitDBTargetOverridesAmbientRedirect(t *testing.T) {
	ambientSourceDir := filepath.Join(t.TempDir(), "ambient-source")
	ambientSourceBeadsDir := filepath.Join(ambientSourceDir, ".beads")
	writeTestConfigYAML(t, ambientSourceBeadsDir, "")
	writeMetadataConfig(t, ambientSourceBeadsDir, configfile.DoltModeServer, "ambient_source_db")

	ambientSharedBeadsDir := filepath.Join(t.TempDir(), "ambient-shared", ".beads")
	writeTestConfigYAML(t, ambientSharedBeadsDir, "")
	writeMetadataConfig(t, ambientSharedBeadsDir, configfile.DoltModeServer, "ambient_shared_default_db")

	if err := os.WriteFile(filepath.Join(ambientSourceBeadsDir, "redirect"), []byte(ambientSharedBeadsDir), 0o600); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	targetRepo := filepath.Join(t.TempDir(), "target")
	targetBeadsDir := filepath.Join(targetRepo, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")
	writeMetadataConfig(t, targetBeadsDir, configfile.DoltModeServer, "explicit_target_db")

	// Run from within the ambient repo, whose own .beads has an active
	// redirect, while an explicit --db points at a wholly unrelated target.
	t.Chdir(ambientSourceDir)
	t.Setenv("BEADS_DIR", ambientSourceBeadsDir)
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

	if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "" {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want \"\" (ambient repo's redirect-source database leaked past an explicit --db target)", got)
	}
	if got := os.Getenv("BEADS_DIR"); got != targetBeadsDir {
		t.Fatalf("BEADS_DIR = %q, want %q (explicit --db target)", got, targetBeadsDir)
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
