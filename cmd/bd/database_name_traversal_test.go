//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

const traversalDatabaseName = "../../victim/name-with-hyphen"

// plantTraversalVictim creates the directory traversalDatabaseName reaches
// from root/.beads/<data dir>, with a sentinel in it.
func plantTraversalVictim(t *testing.T, root string) string {
	t.Helper()
	victim := filepath.Join(root, "victim", "name-with-hyphen", "sentinel")
	writeFileForTraversalTest(t, victim, "unchanged")
	return victim
}

func writeFileForTraversalTest(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertDatabaseNameRefused(t *testing.T, step string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "dolt_database") {
		t.Errorf("%s: %v, want an error naming dolt_database", step, err)
	}
}

func assertUnchanged(t *testing.T, path, want string) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Errorf("%s changed: %q, %v", path, got, err)
	}
}

// TestDatabaseMigrationRejectsTraversal: a dolt_database that is not a single
// directory name (from metadata.json or the environment) is refused before the
// embedded open, the hyphen migration or bootstrap uses it, so the directory
// it names outside .beads and the metadata stay unchanged.
func TestDatabaseMigrationRejectsTraversal(t *testing.T) {
	t.Run("hyphen migration", func(t *testing.T) {
		root := t.TempDir()
		beadsDir := filepath.Join(root, ".beads")
		victim := plantTraversalVictim(t, root)
		if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := &configfile.Config{DoltDatabase: traversalDatabaseName}
		err := migrateHyphenatedDB(beadsDir, cfg, traversalDatabaseName, sanitizeDBName(traversalDatabaseName))
		assertDatabaseNameRefused(t, "migrateHyphenatedDB", err)
		assertUnchanged(t, victim, "unchanged")
		if cfg.DoltDatabase != traversalDatabaseName {
			t.Errorf("metadata modified: %q", cfg.DoltDatabase)
		}
		if _, err := os.Stat(configfile.ConfigPath(beadsDir)); !os.IsNotExist(err) {
			t.Errorf("metadata.json written: %v", err)
		}
	})

	for _, source := range []string{"metadata", "env"} {
		t.Run("embedded open/"+source, func(t *testing.T) {
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BEADS_DOLT_SERVER_MODE", "")
			t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			victim := plantTraversalVictim(t, root)
			if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
				t.Fatal(err)
			}
			database := traversalDatabaseName
			if source == "env" {
				database = "beads"
				t.Setenv("BEADS_DOLT_SERVER_DATABASE", traversalDatabaseName)
			}
			writeMetadataConfig(t, beadsDir, configfile.DoltModeEmbedded, database)
			metadata := configfile.ConfigPath(beadsDir)
			before, err := os.ReadFile(metadata)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			s, err := newDoltStoreFromConfig(ctx, beadsDir)
			if err == nil {
				_ = s.Close()
			}
			assertDatabaseNameRefused(t, "newDoltStoreFromConfig", err)
			s, err = newReadOnlyStoreFromConfig(ctx, beadsDir)
			if err == nil {
				_ = s.Close()
			}
			assertDatabaseNameRefused(t, "newReadOnlyStoreFromConfig", err)

			assertUnchanged(t, victim, "unchanged")
			assertUnchanged(t, metadata, string(before))
			entries, err := os.ReadDir(filepath.Join(beadsDir, "embeddeddolt"))
			if err != nil || len(entries) != 0 {
				t.Errorf("embeddeddolt gained entries: %v, %v", entries, err)
			}
		})
	}

	t.Run("bootstrap data-directory probe", func(t *testing.T) {
		endpointCredentialsEnv(t, []string{"127.0.0.1"}, 0, 3307, 3308)
		t.Setenv("BEADS_DOLT_AUTO_START", "")
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
		t.Setenv("BEADS_DIR", "")
		root := t.TempDir()
		beadsDir := filepath.Join(root, ".beads")
		// Joined under .beads/dolt the name reaches root/victim/name-with-hyphen.
		writeFileForTraversalTest(t, filepath.Join(root, "victim", "name-with-hyphen", ".dolt", "repo_state.json"), "{}")
		cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: traversalDatabaseName, ProjectID: "proj-traversal"}
		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		plan, probe, ok := unresolvedBootstrapDBPlan(BootstrapPlan{BeadsDir: beadsDir}, beadsDir, cfg, true)
		if ok || probe.Exists || plan.HasExisting {
			t.Errorf("probe accepted a database outside the data directory: plan %+v, probe %+v", plan, probe)
		}
		assertDatabaseNameRefused(t, "unresolvedBootstrapDBPlan", probe.Err)
	})

	t.Run("bootstrap sync", func(t *testing.T) {
		clonePortTestEnv(t)
		t.Chdir(t.TempDir())
		t.Setenv("BEADS_DIR", "")
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeEmbedded, DoltDatabase: traversalDatabaseName}
		orig := remoteCloneViaEmbedded
		t.Cleanup(func() { remoteCloneViaEmbedded = orig })
		cloned := false
		remoteCloneViaEmbedded = func(context.Context, string, string, string) error {
			cloned = true
			return nil
		}
		plan := BootstrapPlan{Action: "sync", BeadsDir: beadsDir, Database: traversalDatabaseName, SyncRemote: "file:///nonexistent/remote"}
		assertDatabaseNameRefused(t, "executeSyncAction", executeSyncAction(t.Context(), plan, cfg))
		if cloned {
			t.Error("cloned under an invalid dolt_database")
		}
		if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
			t.Errorf("beads directory created: %v", err)
		}
	})
}
