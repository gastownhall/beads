package configfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantDatabase plants the embedded discovery marker for a database named
// name (the same shape EmbeddedDoltDatabases probes for), without creating a
// real repository.
func plantDatabase(t *testing.T, beadsDir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", name, ".dolt", "noms"), 0o750); err != nil {
		t.Fatalf("planting %s: %v", name, err)
	}
}

// TestCheckMetadatalessEmbeddedDatabase pins the guard's decision table and
// its refusal shape: an empty store and a sole matching database are allowed,
// everything else is refused with a *MetadatalessDatabaseError naming the
// workspace, the attempted database, every existing one (sorted), the
// untouched-data guarantee, and the metadata remedy.
func TestCheckMetadatalessEmbeddedDatabase(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := CheckMetadatalessEmbeddedDatabase(beadsDir, "beads"); err != nil {
		t.Fatalf("empty store refused: %v", err)
	}

	plantDatabase(t, beadsDir, "myproj")
	if err := CheckMetadatalessEmbeddedDatabase(beadsDir, "beads"); err == nil {
		t.Fatal("sole different database allowed")
	}
	if err := CheckMetadatalessEmbeddedDatabase(beadsDir, "myproj"); err != nil {
		t.Fatalf("sole matching database refused: %v", err)
	}

	plantDatabase(t, beadsDir, "beads")
	err := CheckMetadatalessEmbeddedDatabase(beadsDir, "beads")
	if err == nil {
		t.Fatal("several databases allowed even with the attempted name among them")
	}
	var metadataless *MetadatalessDatabaseError
	if !errors.As(err, &metadataless) {
		t.Fatalf("refusal = %#v, want a *MetadatalessDatabaseError", err)
	}
	if metadataless.BeadsDir != beadsDir || metadataless.Attempted != "beads" {
		t.Fatalf("refusal fields = %+v, want beadsDir %s attempted beads", metadataless, beadsDir)
	}
	if strings.Join(metadataless.Existing, ",") != "beads,myproj" {
		t.Fatalf("Existing = %v, want sorted [beads myproj]", metadataless.Existing)
	}
	for _, want := range []string{
		beadsDir,
		"beads",
		"myproj",
		filepath.Join(beadsDir, "embeddeddolt"),
		BackendNotOpenedGuarantee,
		filepath.Join(beadsDir, "metadata.json"),
		"dolt_database",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q: %v", want, err)
		}
	}
	if advice := strings.ToLower(err.Error()); strings.Contains(advice, "delete") || strings.Contains(advice, "bd init") {
		t.Errorf("refusal must not advise deletion or reinitialization: %v", err)
	}
}

// TestCheckMetadatalessEmbeddedDatabaseDiscoverySemantics pins that the guard
// sees databases exactly as EmbeddedDoltDatabases does: a symlinked database,
// an empty .dolt marker, or a plain directory without one do not count, so
// none of them can either block an allowed open or license a refused one.
func TestCheckMetadatalessEmbeddedDatabaseDiscoverySemantics(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store := filepath.Join(beadsDir, "embeddeddolt", "myproj", ".dolt")
	if err := os.MkdirAll(filepath.Join(store, "noms"), 0o750); err != nil {
		t.Fatal(err)
	}
	// An empty marker: not a database.
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "empty", ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	// No marker at all: not a database.
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "plain"), 0o750); err != nil {
		t.Fatal(err)
	}
	// A symlinked directory: not a database.
	if err := os.Symlink(filepath.Join(beadsDir, "embeddeddolt", "myproj"), filepath.Join(beadsDir, "embeddeddolt", "link")); err != nil {
		t.Fatal(err)
	}

	if got := EmbeddedDoltDatabases(beadsDir); strings.Join(got, ",") != "myproj" {
		t.Fatalf("EmbeddedDoltDatabases = %v, want [myproj]", got)
	}
	if err := CheckMetadatalessEmbeddedDatabase(beadsDir, "myproj"); err != nil {
		t.Fatalf("the sole real database was refused: %v", err)
	}
	if err := CheckMetadatalessEmbeddedDatabase(beadsDir, "beads"); err == nil {
		t.Fatal("a sole different database was allowed")
	}
}

// TestMetadatalessDatabaseErrorEscapesHostileNames pins that database names a
// terminal would act on are spelled by remedy.PrintedValue/PrintedList, so the
// refusal cannot be redrawn or reordered by a hostile workspace.
func TestMetadatalessDatabaseErrorEscapesHostileNames(t *testing.T) {
	err := &MetadatalessDatabaseError{
		BeadsDir:  "/tmp/x/\r\x1b[2K/.beads",
		Attempted: "beads",
		Existing:  []string{"my\rproj", "beads"},
	}
	for _, raw := range []string{"/tmp/x/\r", "my\rproj"} {
		if strings.Contains(err.Error(), raw) {
			t.Errorf("refusal repeats %q unescaped: %v", raw, err)
		}
	}
}
