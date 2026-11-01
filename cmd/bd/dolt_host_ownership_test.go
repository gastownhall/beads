package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// cmdHostSpellings covers each host kind in the spellings the classifier
// must agree on (configfile.ClassifyDoltHost).
var cmdHostSpellings = []string{
	"127.0.0.1", "LOCALHOST", "0.0.0.0", "::ffff:127.0.0.1",
	"::1", "[::1]", "0:0:0:0:0:0:0:1",
	"127.0.0.2",
	"db.example.com",
}

// TestShouldUseExternalDoltStatusHostOwnership pins bd dolt status to
// configfile.BdRunsServerAt: a server-mode workspace whose server bd does not
// run gets the external-server status, in every spelling, even with
// auto-start on.
func TestShouldUseExternalDoltStatusHostOwnership(t *testing.T) {
	for _, host := range cmdHostSpellings {
		cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltServerHost: host}
		t.Setenv("BEADS_DOLT_SERVER_HOST", "")
		if got, want := shouldUseExternalDoltStatus(cfg, false, false), !configfile.BdRunsServerAt(host); got != want {
			t.Errorf("shouldUseExternalDoltStatus(host %q) = %v, want %v", host, got, want)
		}
	}
}

// TestResolveDoltShowRemotesHostOwnership pins bd dolt show to the same
// invariant: .beads/dolt's remotes are the workspace's remotes only when bd
// runs its server, so a stale local root never stands in for the remotes of a
// server bd does not run.
func TestResolveDoltShowRemotesHostOwnership(t *testing.T) {
	for _, host := range cmdHostSpellings {
		t.Run(host, func(t *testing.T) {
			withNilStoreForShow(t)
			for _, k := range []string{"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_DATA_DIR", "BEADS_SHARED_SERVER_DIR"} {
				t.Setenv(k, "")
			}
			t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "none.json"))
			beadsDir := t.TempDir()
			cfg := configfile.DefaultConfig()
			cfg.Backend = configfile.BackendDolt
			cfg.DoltMode = configfile.DoltModeServer
			cfg.DoltServerHost = host
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			dbPath := filepath.Join(beadsDir, "dolt", cfg.GetDoltDatabase())
			if err := os.MkdirAll(filepath.Join(dbPath, ".dolt"), 0o755); err != nil {
				t.Fatal(err)
			}
			state := `{"remotes":{"origin":{"name":"origin","url":"file:///stale/remote"}}}`
			if err := os.WriteFile(filepath.Join(dbPath, ".dolt", "repo_state.json"), []byte(state), 0o644); err != nil {
				t.Fatal(err)
			}

			remotes := resolveDoltShowRemotes(beadsDir, cfg)
			if got, want := len(remotes) > 0, configfile.BdRunsServerAt(host); got != want {
				t.Errorf("resolveDoltShowRemotes(host %q) = %+v; local root's remotes shown = %v, want %v", host, remotes, got, want)
			}
		})
	}
}
