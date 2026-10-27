package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestPersistentPreRunSkipsStoreForStoreFreeConfigGet pins the store-free half
// of GH#5108: PersistentPreRunE must send `bd config get <store-free key>`
// down the no-store early return even when a database exists. In server mode
// beads.FindDatabasePath() answers from metadata.json alone, so at base the
// full store path ran for these keys: it wrote .beads/.local_version, could
// auto-migrate and auto-start, and refused outright once the server was
// unreachable — so during an outage the read failed instead of answering from
// config.yaml, git config or the user-global config.
//
// The fixture is modeled on TestPersistentPreRunHonorsSkipStoreAnnotation,
// with one hardening: the server endpoint is a port the test verifiably
// closed. The ambient environment (and the default port on a shared host)
// can point at a live Dolt server, which would make every drive below
// connect instead of fail and void the test's signal.
func TestPersistentPreRunSkipsStoreForStoreFreeConfigGet(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	closedPort := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close the reserved port: %v", err)
	}
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltDatabase:   "store_free_config_get_test",
		DoltServerHost: "127.0.0.1",
		DoltServerPort: closedPort,
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	// If the store IS opened (a broken gate), fail fast: never auto-start a server.
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	t.Cleanup(func() { store = oldStore })

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}

	// Return the mutable globals a PersistentPreRunE drive touches to zero so
	// the independent drives below cannot cross-contaminate.
	resetPreRunGlobals := func() {
		serverMode = false
		cmdCtx = nil
		dbPath = ""
		readonlyMode = false
		doltAutoCommit = ""
		store = nil
	}

	// Every key the config get RunE answers without the store (config.yaml,
	// user-global config.yaml, git config, and the backup.enabled effective
	// value) must skip store init whether or not a database exists.
	for _, key := range []string{"dolt.server", "dolt.max-conns", "beads.role", "backup.enabled"} {
		t.Run("store-free key "+key, func(t *testing.T) {
			resetPreRunGlobals()
			if err := rootCmd.PersistentPreRunE(configGetCmd, []string{key}); err != nil {
				t.Errorf("config get %s must skip store init and return nil, got: %v", key, err)
			}
			if store != nil {
				t.Errorf("config get %s must not open the store", key)
			}
			if _, err := os.Stat(filepath.Join(beadsDir, localVersionFile)); err == nil {
				t.Errorf("config get %s must not write .beads/%s", key, localVersionFile)
			} else if !os.IsNotExist(err) {
				t.Fatalf("stat %s: %v", localVersionFile, err)
			}
		})
	}
}
