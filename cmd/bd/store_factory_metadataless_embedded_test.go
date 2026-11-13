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

// TestEmbeddedMetadatalessStoreFactoryAllowedTargets (GH#4381, AC-4) pins the
// factories' allowed metadata-less cases: an empty store, a sole database
// matching the effective target (default or environment-selected), and the
// two sanitation shapes the writable migration handles. These pass at the
// base commit too; the guard must leave them green.
func TestEmbeddedMetadatalessStoreFactoryAllowedTargets(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	metadatalessEnv(t)

	t.Run("empty store still bootstraps the default database", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
		if err != nil {
			t.Fatalf("writable factory on an empty store failed: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("closing bootstrapped store: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt")); statErr != nil {
			t.Fatalf("the default database was not created (stat error: %v)", statErr)
		}
	})

	t.Run("strict open of an empty store still refuses", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := newExistingDoltStoreFromConfig(t.Context(), beadsDir)
		if err == nil || !strings.Contains(err.Error(), "no embedded database at") {
			t.Fatalf("strict open of an empty store = %v, want the existing no-database refusal", err)
		}
	})

	t.Run("sole default database stays allowed", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		seedMetadatalessEmbeddedStore(t, beadsDir, "beads")

		store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
		if err != nil {
			t.Fatalf("writable factory on the sole default database failed: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("closing store: %v", err)
		}
		readOnly, err := newReadOnlyStoreFromConfig(t.Context(), beadsDir)
		if err != nil {
			t.Fatalf("read-only factory on the sole default database failed: %v", err)
		}
		if err := readOnly.Close(); err != nil {
			t.Fatalf("closing read-only store: %v", err)
		}
		if got := listDatabaseDirs(t, filepath.Join(beadsDir, "embeddeddolt")); strings.Join(got, ",") != "beads" {
			t.Fatalf("embedded store holds %v, want exactly [beads]", got)
		}
	})

	t.Run("environment-selected sole target stays allowed", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		seedMetadatalessEmbeddedStore(t, beadsDir, "foo")
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "foo")

		store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
		if err != nil {
			t.Fatalf("writable factory on the environment-selected sole database failed: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("closing store: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(statErr) {
			t.Fatalf("an allowed open of the sole target wrote metadata.json (stat error: %v)", statErr)
		}
	})

	t.Run("sole legacy hyphenated directory migrates instead of refusing", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		seedMetadatalessEmbeddedStore(t, beadsDir, "my_proj")
		if err := os.Rename(filepath.Join(beadsDir, "embeddeddolt", "my_proj"), filepath.Join(beadsDir, "embeddeddolt", "my-proj")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "my-proj")

		store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
		if err != nil {
			t.Fatalf("writable factory on the sole legacy directory failed: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("closing store: %v", err)
		}
		if got := listDatabaseDirs(t, filepath.Join(beadsDir, "embeddeddolt")); strings.Join(got, ",") != "my_proj" {
			t.Fatalf("legacy directory was not migrated; store holds %v", got)
		}
		reloaded, loadErr := configfile.Load(beadsDir)
		if loadErr != nil || reloaded == nil || reloaded.DoltDatabase != "my_proj" {
			t.Fatalf("reloaded metadata = %+v, %v; want dolt_database my_proj", reloaded, loadErr)
		}
	})

	t.Run("sole already-sanitized target opens without renaming", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		seedMetadatalessEmbeddedStore(t, beadsDir, "my_proj")
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "my-proj")

		store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
		if err != nil {
			t.Fatalf("writable factory on the sole sanitized database failed: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("closing store: %v", err)
		}
		if got := listDatabaseDirs(t, filepath.Join(beadsDir, "embeddeddolt")); strings.Join(got, ",") != "my_proj" {
			t.Fatalf("store holds %v, want exactly [my_proj]", got)
		}
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
