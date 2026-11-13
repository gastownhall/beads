//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// In-process pins of the store factories' half of GH#4381: a metadata-less
// workspace (no metadata.json) whose embedded store already holds a database
// must not let a factory fall back to a default database name and open or
// create a sibling. These tests call the factories directly — the same code
// routed opens, foreign-workspace reads and the identity preview run — so
// they cover callers the CLI pre-run guard never sees.

// metadatalessEnv neutralizes the environment that would reroute a factory
// open away from the embedded store, so the tests resolve the embedded arm
// deterministically.
func metadatalessEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"BEADS_DOLT_SERVER_DATABASE",
		"BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_SERVER",
		"BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_SERVER_PORT",
		"BEADS_DOLT_SERVER_MODE",
	} {
		t.Setenv(name, "")
	}
}

// seedMetadatalessEmbeddedStore creates a real embedded database named
// database under beadsDir, then deletes metadata.json: the workspace is left
// metadata-less with that sole real database in its store.
func seedMetadatalessEmbeddedStore(t *testing.T, beadsDir, database string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("creating %s: %v", beadsDir, err)
	}
	cfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: database,
		DoltMode:     configfile.DoltModeEmbedded,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("saving seed metadata for %s: %v", database, err)
	}
	store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("seeding database %s: %v", database, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closing seed store for %s: %v", database, err)
	}
	removeWorkspaceMetadata(t, beadsDir)
}

// requireFactoryRefusal asserts err is the shared metadata-less refusal for
// the attempted database against the named existing ones.
func requireFactoryRefusal(t *testing.T, err error, beadsDir, attempted string, existing ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("factory opened a metadata-less store holding other databases; want the shared refusal")
	}
	for _, want := range append([]string{beadsDir, attempted, "no storage database was opened or modified", "metadata.json"}, existing...) {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("factory refusal must name %q: %v", want, err)
		}
	}
}

// requireStoreIntact asserts the embedded store still holds exactly the named
// databases and no metadata.json was written by a refused open.
func requireStoreIntact(t *testing.T, beadsDir string, databases ...string) {
	t.Helper()
	got := listDatabaseDirs(t, filepath.Join(beadsDir, "embeddeddolt"))
	if strings.Join(got, ",") != strings.Join(databases, ",") {
		t.Fatalf("embedded store holds %v, want exactly %v", got, databases)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(statErr) {
		t.Fatalf("refused open left metadata.json behind (stat error: %v)", statErr)
	}
}

// TestEmbeddedMetadatalessStoreFactoryRefusesMultipleDatabases (GH#4381,
// AC-3): with several databases in a metadata-less store, every factory —
// writable, read-only and preview — refuses naming all of them, even though
// one carries the attempted default name.
func TestEmbeddedMetadatalessStoreFactoryRefusesMultipleDatabases(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	metadatalessEnv(t)

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	seedMetadatalessEmbeddedStore(t, beadsDir, "myproj")
	if err := copyDirRecursive(filepath.Join(beadsDir, "embeddeddolt", "myproj"), filepath.Join(beadsDir, "embeddeddolt", "beads")); err != nil {
		t.Fatalf("seeding the beads copy: %v", err)
	}

	t.Run("writable factory refuses naming both", func(t *testing.T) {
		_, err := newDoltStoreFromConfig(t.Context(), beadsDir)
		requireFactoryRefusal(t, err, beadsDir, "beads", "beads", "myproj")
		requireStoreIntact(t, beadsDir, "beads", "myproj")
	})

	t.Run("read-only factory refuses naming both", func(t *testing.T) {
		_, err := newReadOnlyStoreFromConfig(t.Context(), beadsDir)
		requireFactoryRefusal(t, err, beadsDir, "beads", "beads", "myproj")
		requireStoreIntact(t, beadsDir, "beads", "myproj")
	})

	t.Run("preview factory refuses naming both", func(t *testing.T) {
		_, err := newPreviewStoreFromConfig(t.Context(), beadsDir)
		requireFactoryRefusal(t, err, beadsDir, "beads", "beads", "myproj")
		requireStoreIntact(t, beadsDir, "beads", "myproj")
	})
}

// removeWorkspaceMetadata deletes a workspace's configuration files without
// capturing them, for tests that never restore metadata.
func removeWorkspaceMetadata(t *testing.T, beadsDir string) {
	t.Helper()
	for _, name := range []string{"metadata.json", "config.json", "config.yaml"} {
		if err := os.Remove(filepath.Join(beadsDir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("removing %s: %v", name, err)
		}
	}
}

// listDatabaseDirs lists the directory names directly under dir, sorted.
func listDatabaseDirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}
