package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestBootstrapRejectsRemovedBackendsBeforeWorkspaceWrites(t *testing.T) {
	bd := buildBDForInitTests(t)

	for _, backend := range []string{configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite} {
		backend := backend
		for _, args := range [][]string{{"bootstrap", "--dry-run"}, {"bootstrap", "--yes"}} {
			args := args
			mode := "execute"
			if len(args) > 1 && args[1] == "--dry-run" {
				mode = "dry-run"
			}

			t.Run(backend+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				beadsDir := filepath.Join(root, ".beads")
				if err := os.MkdirAll(beadsDir, 0o700); err != nil {
					t.Fatalf("create .beads: %v", err)
				}

				metadata := []byte(fmt.Sprintf("{\n  \"database\": \"legacy.db\",\n  \"backend\": %q,\n  \"project_id\": \"legacy-project\"\n}\n", backend))
				metadataPath := filepath.Join(beadsDir, configfile.ConfigFileName)
				if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
					t.Fatalf("write metadata.json: %v", err)
				}

				cmd := exec.Command(bd, args...)
				cmd.Dir = root
				cmd.Env = bootstrapBackendGuardEnv(root, beadsDir)
				out, err := cmd.CombinedOutput()
				if err == nil {
					t.Errorf("bd %s unexpectedly succeeded for removed backend %q:\n%s", strings.Join(args, " "), backend, out)
				}

				message := strings.ToLower(string(out))
				for _, want := range removedBackendWantSubstrings(backend) {
					if !strings.Contains(message, want) {
						t.Errorf("bd %s error for %q missing %q:\n%s", strings.Join(args, " "), backend, want, message)
					}
				}

				after, readErr := os.ReadFile(metadataPath)
				if readErr != nil {
					t.Fatalf("read metadata.json after rejected bootstrap: %v", readErr)
				}
				if !bytes.Equal(after, metadata) {
					t.Errorf("bd %s rewrote metadata.json for removed backend %q:\nbefore:\n%s\nafter:\n%s", strings.Join(args, " "), backend, metadata, after)
				}
				assertNoBootstrapStorageArtifacts(t, beadsDir)
			})
		}
	}
}

func TestBootstrapRejectsUnknownBackendBeforeWorkspaceWrites(t *testing.T) {
	bd := buildBDForInitTests(t)

	for _, args := range [][]string{{"bootstrap", "--dry-run"}, {"bootstrap", "--yes"}} {
		mode := "execute"
		if args[1] == "--dry-run" {
			mode = "dry-run"
		}

		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatalf("create .beads: %v", err)
			}

			metadata := []byte("{\n  \"backend\": \"mystery\",\n  \"project_id\": \"legacy-project\"\n}\n")
			metadataPath := filepath.Join(beadsDir, configfile.ConfigFileName)
			if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
				t.Fatalf("write metadata.json: %v", err)
			}

			cmd := exec.Command(bd, args...)
			cmd.Dir = root
			cmd.Env = bootstrapBackendGuardEnv(root, beadsDir)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("bd %s unexpectedly accepted an unknown backend:\n%s", strings.Join(args, " "), out)
			}
			message := string(out)
			for _, want := range []string{"not recognized", "no storage database was opened or modified", "dolt"} {
				if !strings.Contains(message, want) {
					t.Errorf("bd %s error missing %q:\n%s", strings.Join(args, " "), want, message)
				}
			}

			after, readErr := os.ReadFile(metadataPath)
			if readErr != nil {
				t.Fatalf("read metadata.json after rejected bootstrap: %v", readErr)
			}
			if !bytes.Equal(after, metadata) {
				t.Errorf("bd %s rewrote unknown-backend metadata:\nbefore:\n%s\nafter:\n%s", strings.Join(args, " "), metadata, after)
			}
			assertNoBootstrapStorageArtifacts(t, beadsDir)
		})
	}
}

func TestBootstrapRejectsCorruptMetadataBeforeWorkspaceWrites(t *testing.T) {
	bd := buildBDForInitTests(t)

	for _, args := range [][]string{{"bootstrap", "--dry-run"}, {"bootstrap", "--yes"}} {
		mode := "execute"
		if args[1] == "--dry-run" {
			mode = "dry-run"
		}

		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatalf("create .beads: %v", err)
			}

			metadata := []byte("{\n")
			metadataPath := filepath.Join(beadsDir, configfile.ConfigFileName)
			if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
				t.Fatalf("write corrupt metadata.json: %v", err)
			}

			cmd := exec.Command(bd, args...)
			cmd.Dir = root
			cmd.Env = bootstrapBackendGuardEnv(root, beadsDir)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("bd %s unexpectedly ignored corrupt metadata:\n%s", strings.Join(args, " "), out)
			}
			message := strings.ToLower(string(out))
			for _, want := range []string{"metadata.json", "no storage database was opened or modified"} {
				if !strings.Contains(message, want) {
					t.Errorf("bd %s error missing %q:\n%s", strings.Join(args, " "), want, out)
				}
			}

			after, readErr := os.ReadFile(metadataPath)
			if readErr != nil {
				t.Fatalf("read corrupt metadata after rejected bootstrap: %v", readErr)
			}
			if !bytes.Equal(after, metadata) {
				t.Errorf("bd %s rewrote corrupt metadata:\nbefore: %q\nafter:  %q", strings.Join(args, " "), metadata, after)
			}
			assertNoBootstrapStorageArtifacts(t, beadsDir)
		})
	}
}

func TestBootstrapDoesNotConvertExistingSQLiteWorkspace(t *testing.T) {
	bd := buildBDForInitTests(t)

	for _, args := range [][]string{{"bootstrap", "--dry-run"}, {"bootstrap", "--yes"}} {
		args := args
		mode := "execute"
		if len(args) > 1 && args[1] == "--dry-run" {
			mode = "dry-run"
		}

		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			for _, gitArgs := range [][]string{
				{"init", "-q"},
				{"config", "user.email", "test@test.com"},
				{"config", "user.name", "Test"},
				{"config", "core.hooksPath", ".git/hooks"},
			} {
				runGitForBootstrapTest(t, root, gitArgs...)
			}

			// Hand-seed a workspace created by an earlier build's SQLite backend:
			// metadata selecting the removed backend plus its database file. The
			// guard must reject it without reading or rewriting either.
			beadsDir := filepath.Join(root, ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatalf("create .beads: %v", err)
			}
			metadataPath := filepath.Join(beadsDir, configfile.ConfigFileName)
			metadataBefore := []byte("{\n  \"database\": \"beads.db\",\n  \"backend\": \"sqlite\",\n  \"sqlite_path\": \"beads.db\",\n  \"project_id\": \"sqlite-guard\"\n}\n")
			if err := os.WriteFile(metadataPath, metadataBefore, 0o600); err != nil {
				t.Fatalf("write metadata.json: %v", err)
			}
			databasePath := filepath.Join(beadsDir, "beads.db")
			databaseContent := []byte("sqlite-guard database placeholder")
			if err := os.WriteFile(databasePath, databaseContent, 0o600); err != nil {
				t.Fatalf("write beads.db: %v", err)
			}
			databaseBefore := sha256.Sum256(databaseContent)
			localVersionBefore, hadLocalVersion := readOptionalBootstrapGuardFile(t, filepath.Join(beadsDir, ".local_version"))

			cmd := exec.Command(bd, args...)
			cmd.Dir = root
			cmd.Env = bootstrapBackendGuardEnv(root, beadsDir)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("bd %s unexpectedly accepted an existing SQLite workspace:\n%s", strings.Join(args, " "), out)
			}

			message := strings.ToLower(string(out))
			for _, want := range []string{
				"historical sqlite workspace",
				"explicit migration is required",
				"preserve .beads unchanged",
				"cross-era-upgrades",
			} {
				if !strings.Contains(message, want) {
					t.Errorf("bd %s error missing %q:\n%s", strings.Join(args, " "), want, out)
				}
			}

			metadataAfter := readBootstrapGuardFile(t, metadataPath)
			if !bytes.Equal(metadataAfter, metadataBefore) {
				t.Errorf("bd %s rewrote SQLite metadata.json:\nbefore:\n%s\nafter:\n%s", strings.Join(args, " "), metadataBefore, metadataAfter)
			}
			databaseAfter := sha256.Sum256(readBootstrapGuardFile(t, databasePath))
			if databaseAfter != databaseBefore {
				t.Errorf("bd %s modified the existing SQLite database", strings.Join(args, " "))
			}
			localVersionAfter, hasLocalVersion := readOptionalBootstrapGuardFile(t, filepath.Join(beadsDir, ".local_version"))
			if hasLocalVersion != hadLocalVersion || !bytes.Equal(localVersionAfter, localVersionBefore) {
				t.Errorf("bd %s created or modified SQLite workspace version tracking", strings.Join(args, " "))
			}
			for _, name := range []string{"embeddeddolt", "dolt"} {
				path := filepath.Join(beadsDir, name)
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Errorf("bd %s converted SQLite by creating Dolt state at %s (stat error: %v)", strings.Join(args, " "), path, statErr)
				}
			}
		})
	}
}

// TestUnprovenSiblingDatabases pins the guard that keeps the #5915 skeleton
// removal from destroying a database it never proved empty. embeddeddolt/ is a
// Dolt multi-database directory but embeddedDBIsEmpty only proves dbName, so
// cloneViaEmbedded refuses when any other database directory is present.
//
// Tagless on purpose: the helper is pure os.ReadDir plus string work, so this is
// real coverage in the CGO_ENABLED=0 lanes, where every bootstrap_*_embedded and
// bootstrap_guard test is compiled out.
func TestUnprovenSiblingDatabases(t *testing.T) {
	const dbName = "beads"

	for _, tc := range []struct {
		name    string
		entries []string // "d:<name>" directory, "f:<name>" file
		want    []string
	}{
		// The shape every workspace bd produces: one database directory. Must
		// stay empty or the guard would block the fix it is protecting.
		{"only the proven database", []string{"d:beads"}, nil},
		{"dolt bookkeeping is allowed", []string{"d:beads", "d:.doltcfg", "d:.dolt"}, nil},
		{"a sibling database is reported", []string{"d:beads", "d:bd_metrics_repo_2424946071"}, []string{"bd_metrics_repo_2424946071"}},
		// Only a directory can be a Dolt database.
		{"stray files are ignored", []string{"d:beads", "f:README", "f:.DS_Store"}, nil},
		{"several siblings come back sorted", []string{"d:beads", "d:zeta", "d:alpha"}, []string{"alpha", "zeta"}},
		// The database bd expects can be absent entirely (a name divergence);
		// everything present is then unproven.
		{"proven database absent", []string{"d:other"}, []string{"other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "embeddeddolt")
			if err := os.MkdirAll(dataDir, 0o750); err != nil {
				t.Fatal(err)
			}
			for _, spec := range tc.entries {
				kind, name := spec[:1], spec[2:]
				path := filepath.Join(dataDir, name)
				if kind == "d" {
					if err := os.MkdirAll(path, 0o750); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			got, err := unprovenSiblingDatabases(dataDir, dbName)
			if err != nil {
				t.Fatalf("unprovenSiblingDatabases: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("unprovenSiblingDatabases(%v) = %v, want %v", tc.entries, got, tc.want)
			}
		})
	}

	// An unreadable directory must surface as an error, never as "no siblings" —
	// that is the same fail-closed rule embeddedDBIsEmpty uses, and treating it
	// as empty would re-open the destructive path this guard closes.
	t.Run("missing directory is an error, not an empty answer", func(t *testing.T) {
		got, err := unprovenSiblingDatabases(filepath.Join(t.TempDir(), "absent"), dbName)
		if err == nil {
			t.Fatalf("want an error for a missing dataDir, got %v", got)
		}
	})
}

func bootstrapBackendGuardEnv(home, beadsDir string) []string {
	env := make([]string, 0, len(os.Environ())+7)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "BEADS_") || strings.HasPrefix(entry, "BD_") || strings.HasPrefix(entry, "HOME=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"HOME="+home,
		"BEADS_DIR="+beadsDir,
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_NO_DAEMON=1",
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BD_NON_INTERACTIVE=1",
	)
}

func assertNoBootstrapStorageArtifacts(t *testing.T, beadsDir string) {
	t.Helper()
	for _, name := range []string{"embeddeddolt", "dolt", "beads.db", ".local_version"} {
		path := filepath.Join(beadsDir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("rejected bootstrap created local state at %s (stat error: %v)", path, err)
		}
	}
}

func readBootstrapGuardFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func readOptionalBootstrapGuardFile(t *testing.T, path string) ([]byte, bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data, true
}
