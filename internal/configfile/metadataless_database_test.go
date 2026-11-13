package configfile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// seedEmbeddedMarker gives beadsDir an embedded database named name as
// EmbeddedDoltDatabases discovers it: a directory with a non-empty .dolt.
// Discovery never opens the repository, so a fabricated marker is enough.
func seedEmbeddedMarker(t *testing.T, beadsDir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", name, ".dolt", "noms"), 0o750); err != nil {
		t.Fatal(err)
	}
}

// TestCheckMetadatalessEmbeddedDatabase pins which embedded stores a
// metadata-less open may use (GH#4381): an empty store, or one whose only
// database is the target. Anything else is refused with every discovered
// database named, including a target that is one of several.
func TestCheckMetadatalessEmbeddedDatabase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		seed   []string
		target string
		refuse []string // the Found list of the refusal; nil when allowed
	}{
		{name: "empty store", target: "beads"},
		{name: "sole matching database", seed: []string{"beads"}, target: "beads"},
		{name: "sole nondefault target", seed: []string{"foo"}, target: "foo"},
		{name: "sole different database", seed: []string{"myproj"}, target: "beads", refuse: []string{"myproj"}},
		{name: "several without the target", seed: []string{"zeta", "myproj"}, target: "beads", refuse: []string{"myproj", "zeta"}},
		{name: "several including the target", seed: []string{"myproj", "beads"}, target: "beads", refuse: []string{"beads", "myproj"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			for _, name := range tc.seed {
				seedEmbeddedMarker(t, beadsDir, name)
			}
			err := CheckMetadatalessEmbeddedDatabase(beadsDir, tc.target)
			if tc.refuse == nil {
				if err != nil {
					t.Fatalf("CheckMetadatalessEmbeddedDatabase(%v, %q) = %v, want nil", tc.seed, tc.target, err)
				}
				return
			}
			var refusal *MetadatalessDatabaseError
			if !errors.As(err, &refusal) {
				t.Fatalf("CheckMetadatalessEmbeddedDatabase(%v, %q) = %v, want *MetadatalessDatabaseError", tc.seed, tc.target, err)
			}
			if refusal.BeadsDir != beadsDir || refusal.Database != tc.target || !reflect.DeepEqual(refusal.Found, tc.refuse) {
				t.Fatalf("refusal = %+v, want BeadsDir %q, Database %q, Found %v", refusal, beadsDir, tc.target, tc.refuse)
			}
			msg := err.Error()
			for _, want := range append([]string{
				beadsDir,
				"would open " + tc.target,
				"holds " + strings.Join(tc.refuse, ", "),
				"no database was opened or created",
				filepath.Join(beadsDir, ConfigFileName),
				`"dolt_database"`,
				".beads/redirect",
			}, tc.refuse...) {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal %q does not contain %q", msg, want)
				}
			}
			if several := strings.Contains(msg, "do not delete any"); several != (len(tc.refuse) > 1) {
				t.Errorf("refusal %q: divergence caution present = %v, want %v", msg, several, len(tc.refuse) > 1)
			}
			if strings.Contains(msg, "bd init") {
				t.Errorf("refusal %q suggests bd init, which neither restores identity nor recovers writes", msg)
			}
		})
	}
}

// TestCheckMetadatalessEmbeddedDatabaseFollowsDiscovery pins that the check
// counts exactly what EmbeddedDoltDatabases discovers: symlinked database
// directories, symlinked or empty .dolt markers and stray files are not
// databases, so they neither refuse an open nor appear in a refusal.
func TestCheckMetadatalessEmbeddedDatabaseFollowsDiscovery(t *testing.T) {
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "embeddeddolt")
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(elsewhere, "real", ".dolt", "noms"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "emptymarker", ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "real"), filepath.Join(root, "linkeddb")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "linkedmarker"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "real", ".dolt"), filepath.Join(root, "linkedmarker", ".dolt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckMetadatalessEmbeddedDatabase(beadsDir, "beads"); err != nil {
		t.Fatalf("a store with no discoverable database refused: %v", err)
	}

	seedEmbeddedMarker(t, beadsDir, "myproj")
	var refusal *MetadatalessDatabaseError
	if err := CheckMetadatalessEmbeddedDatabase(beadsDir, "beads"); !errors.As(err, &refusal) {
		t.Fatalf("a store holding myproj = %v, want *MetadatalessDatabaseError", err)
	}
	if !reflect.DeepEqual(refusal.Found, []string{"myproj"}) {
		t.Fatalf("Found = %v, want only the discoverable [myproj]", refusal.Found)
	}
	if err := CheckMetadatalessEmbeddedDatabase(beadsDir, "myproj"); err != nil {
		t.Fatalf("myproj is the only discoverable database, so opening it must be allowed: %v", err)
	}
}

// TestMetadatalessDatabaseErrorEscapesValues pins that names and paths are
// spelled with remedy.PrintedValue: a control character in a database name
// cannot redraw or split the refusal.
func TestMetadatalessDatabaseErrorEscapesValues(t *testing.T) {
	err := &MetadatalessDatabaseError{BeadsDir: "/w/.beads", Database: "beads", Found: []string{"evil\x1b[2Jname", "myproj"}}
	msg := err.Error()
	if strings.ContainsRune(msg, '\x1b') {
		t.Fatalf("refusal %q carries a raw escape character", msg)
	}
	if !strings.Contains(msg, `"evil\x1b[2Jname", myproj`) {
		t.Fatalf("refusal %q does not name the escaped database beside myproj", msg)
	}
}
