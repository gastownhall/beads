//go:build cgo

package beads_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads"
)

// The SDK's half of GH#4381: OpenBestAvailable on a workspace with no
// metadata.json must not fall back to the default database name and open or
// create a sibling of a database the workspace already holds. The refusal
// subtests never reach the storage engine — a directory carrying only the
// discovery marker is enough — so they run in every cgo lane; the two
// allowed cases really open a database and need the embedded-Dolt gate.

// plantEmbeddedMarker plants the embedded discovery marker for a database
// named name under beadsDir, without creating a real repository.
func plantEmbeddedMarker(t *testing.T, beadsDir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", name, ".dolt", "noms"), 0o750); err != nil {
		t.Fatalf("planting marker for %s: %v", name, err)
	}
}

// embeddedDirNames lists the database directory names under beadsDir's
// embedded store, sorted.
func embeddedDirNames(t *testing.T, beadsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(beadsDir, "embeddeddolt"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("listing embedded store: %v", err)
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

// requireMetadatalessRefusal asserts err carries the shared metadata-less
// refusal for the attempted database against the named existing ones, and
// that no sibling appeared.
func requireMetadatalessRefusal(t *testing.T, beadsDir string, store beads.Storage, err error, attempted string, existing ...string) {
	t.Helper()
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenBestAvailable returned a store for a metadata-less workspace holding other databases")
	}
	if err == nil {
		t.Fatal("OpenBestAvailable succeeded against a metadata-less workspace holding other databases; want the shared refusal")
	}
	for _, want := range append([]string{beadsDir, attempted, "no storage database was opened or modified", "metadata.json"}, existing...) {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("OpenBestAvailable refusal must name %q: %v", want, err)
		}
	}
	if got := embeddedDirNames(t, beadsDir); strings.Join(got, ",") != strings.Join(existing, ",") {
		t.Fatalf("embedded store holds %v after the refused open, want exactly %v", got, existing)
	}
}

// TestOpenBestAvailableRefusesMetadatalessEmbeddedFallback (GH#4381): the
// public open refuses the default-database fallback whenever it cannot be
// told apart from the databases the workspace already holds — a sole
// different database, several databases (the attempted name among them or
// not), and an environment-selected target that names none of them.
func TestOpenBestAvailableRefusesMetadatalessEmbeddedFallback(t *testing.T) {
	for _, name := range []string{"BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_MODE"} {
		t.Setenv(name, "")
	}

	t.Run("several databases refuse even with the attempted name among them", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		plantEmbeddedMarker(t, beadsDir, "myproj")
		plantEmbeddedMarker(t, beadsDir, "beads")

		store, err := beads.OpenBestAvailable(context.Background(), beadsDir)
		requireMetadatalessRefusal(t, beadsDir, store, err, "beads", "beads", "myproj")
	})

	t.Run("sole target is admitted past the guard", func(t *testing.T) {
		if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
			t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
		}
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		plantEmbeddedMarker(t, beadsDir, "foo")
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "foo")

		// The planted marker is not a real repository, so the engine open may
		// fail — but it must not be the metadata-less refusal, and it must not
		// create a sibling beside the admitted target.
		store, err := beads.OpenBestAvailable(context.Background(), beadsDir)
		if store != nil {
			_ = store.Close()
		}
		if err != nil && strings.Contains(err.Error(), "no workspace metadata at") {
			t.Fatalf("the sole target was refused as metadata-less: %v", err)
		}
		if got := embeddedDirNames(t, beadsDir); strings.Join(got, ",") != "foo" {
			t.Fatalf("embedded store holds %v, want exactly [foo]", got)
		}
	})

	t.Run("empty store bootstraps the default database", func(t *testing.T) {
		if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
			t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
		}
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}

		store, err := beads.OpenBestAvailable(context.Background(), beadsDir)
		if err != nil {
			t.Fatalf("OpenBestAvailable on an empty store failed: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("closing bootstrapped store: %v", err)
		}
		if got := embeddedDirNames(t, beadsDir); strings.Join(got, ",") != "beads" {
			t.Fatalf("embedded store holds %v, want the bootstrapped [beads]", got)
		}
	})
}
