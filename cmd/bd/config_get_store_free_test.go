package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// setupStoreFreeConfigGetWorkspace builds a server-mode workspace whose
// database is absent, so beads.FindDatabasePath() answers from metadata.json
// and any store open fails. It returns the .beads dir and a drive function
// that runs rootCmd.PersistentPreRunE on fresh pre-run globals.
func setupStoreFreeConfigGetWorkspace(t *testing.T) (string, func(*cobra.Command, []string) error) {
	t.Helper()

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	writeMetadataConfig(t, beadsDir, configfile.DoltModeServer, "config_get_store_free_test")

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	// If the store IS opened, fail fast: never auto-start a server.
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	t.Cleanup(func() { store = oldStore })

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}

	drive := func(cmd *cobra.Command, args []string) error {
		serverMode = false
		cmdCtx = nil
		dbPath = ""
		readonlyMode = false
		doltAutoCommit = ""
		store = nil
		t.Setenv("BEADS_DIR", beadsDir)
		return rootCmd.PersistentPreRunE(cmd, args)
	}
	return beadsDir, drive
}

// TestPersistentPreRunSkipsStoreForStoreFreeConfigGet pins GH#5108: a
// `config get` whose key lives in config.yaml, git config or the user-global
// config must not open the store just because a database is discoverable.
// Opening it writes .local_version, can auto-migrate and auto-start, and
// refuses outright when the server is down, for a key the store never answers.
func TestPersistentPreRunSkipsStoreForStoreFreeConfigGet(t *testing.T) {
	beadsDir, drive := setupStoreFreeConfigGetWorkspace(t)
	localVersion := filepath.Join(beadsDir, localVersionFile)

	for _, key := range []string{"dolt.server", "dolt.max-conns", "beads.role", "backup.enabled"} {
		t.Run(key, func(t *testing.T) {
			if err := drive(configGetCmd, []string{key}); err != nil {
				t.Fatalf("config get %s must skip store init and return nil, got: %v", key, err)
			}
			if store != nil {
				t.Fatalf("config get %s must not open the store", key)
			}
			if _, err := os.Stat(localVersion); !os.IsNotExist(err) {
				t.Fatalf("config get %s must not write %s (stat err: %v)", key, localVersionFile, err)
			}
		})
	}

	// Control: a DB-backed key in the same workspace still reaches store init
	// and fails on the absent database, so the nil returns above mean the
	// store was skipped, not that an open happened to succeed.
	if err := drive(configGetCmd, []string{"storage.backend"}); err == nil {
		t.Fatal("config get storage.backend should attempt store init and fail to open the absent database; test-environment precondition broken")
	}
}
