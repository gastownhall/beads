package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

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
	// the independent drives below cannot cross-contaminate. The store path
	// writes .local_version before its open fails, so remove it too: one
	// regressed key must not report a .local_version failure in every later
	// subtest.
	resetPreRunGlobals := func(t *testing.T) {
		t.Helper()
		serverMode = false
		cmdCtx = nil
		dbPath = ""
		readonlyMode = false
		doltAutoCommit = ""
		store = nil
		if err := os.Remove(filepath.Join(beadsDir, localVersionFile)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", localVersionFile, err)
		}
	}

	// Every key the config get RunE answers without the store (config.yaml,
	// user-global config.yaml, git config, and the backup.enabled effective
	// value) must skip store init whether or not a database exists.
	for _, key := range []string{"dolt.server", "dolt.max-conns", "beads.role", "backup.enabled"} {
		t.Run("store-free key "+key, func(t *testing.T) {
			resetPreRunGlobals(t)
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

	// Control: a DB-backed key must still attempt store init and fail on the
	// absent server, proving the fixture above really reaches the store path
	// for a key the classification does not exempt (as at base).
	resetPreRunGlobals(t)
	if err := rootCmd.PersistentPreRunE(configGetCmd, []string{"storage.backend"}); err == nil {
		t.Fatal("config get storage.backend should still attempt store init and fail on the absent database; test-environment precondition broken")
	}

	// The exemption is pinned to the `config get` command path, not to the
	// classifier alone: configCommandCanRunWithoutStore also answers true for
	// `config set`/`config unset` on a yaml-only key, so those two rows pin the
	// path check. `config list` and `kv get` are sanity controls: the
	// classifier never exempts them (it refuses any command whose parent is
	// not `config`). All four must keep attempting store init (and failing on
	// the absent server) exactly as at base.
	mustStillOpenStore := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"config set", configSetCmd, []string{"dolt.max-conns", "8"}},
		{"config unset", configUnsetCmd, []string{"dolt.max-conns"}},
		{"config list", configListCmd, nil},
		{"kv get", kvGetCmd, []string{"k"}},
	}
	for _, tc := range mustStillOpenStore {
		t.Run(tc.name+" still attempts store init", func(t *testing.T) {
			resetPreRunGlobals(t)
			if err := rootCmd.PersistentPreRunE(tc.cmd, tc.args); err == nil {
				t.Errorf("%s must still attempt store init and fail on the absent database", tc.name)
			}
		})
	}
}
