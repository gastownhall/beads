package main

import (
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

func TestDoctorPersistentPreRunLoadsServerModeForNoDBCommand(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	if err := (&configfile.Config{
		Backend:      configfile.BackendDolt,
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "doctor_ctx_test",
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)

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

	if rootCmd.PersistentPreRun == nil {
		t.Fatal("rootCmd.PersistentPreRun must be set")
	}
	rootCmd.PersistentPreRun(doctorCmd, nil)

	if !serverMode {
		t.Fatal("doctor should load server mode before the no-store early return")
	}
}
