//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedMetadatalessMultiDatabaseRefusal (GH#4381, AC-3): a
// metadata-less workspace whose embedded store holds SEVERAL databases is
// the maximally ambiguous case — even a database named beads among them does
// not license an open, because nothing says it is this workspace's. Every
// CLI path must refuse naming all of them, and once metadata.json is back,
// configured opens must work again without a third database appearing.
func TestEmbeddedMetadatalessMultiDatabaseRefusal(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	// Seed a real second database named beads beside myproj: the attempted
	// fallback name itself exists, which must not make the open allowed.
	f := newMetadatalessFixture(t)
	if err := copyDirRecursive(filepath.Join(f.storeDir, "myproj"), filepath.Join(f.storeDir, "beads")); err != nil {
		t.Fatalf("seeding the beads copy: %v", err)
	}
	f.removeMetadata(t)

	t.Run("read refuses naming both databases", func(t *testing.T) {
		out, err := runBDMetadataless(t, f.bd, f.dir, nil, "list")
		f.requireRefusal(t, out, err, "beads", "beads", "myproj")
		f.requireDatabases(t, "beads", "myproj")
	})

	t.Run("write refuses naming both databases", func(t *testing.T) {
		out, err := runBDMetadataless(t, f.bd, f.dir, []string{"BEADS_SKIP_IDENTITY_CHECK=1"}, "create", "--silent", "stray write")
		f.requireRefusal(t, out, err, "beads", "beads", "myproj")
		f.requireDatabases(t, "beads", "myproj")
		if _, statErr := os.Stat(filepath.Join(f.beadsDir, "metadata.json")); !os.IsNotExist(statErr) {
			t.Fatalf("a refused write left metadata.json behind (stat error: %v)", statErr)
		}
	})

	t.Run("configured opens keep working after restoring metadata", func(t *testing.T) {
		f.restoreMetadata(t)
		issues := bdListJSON(t, f.bd, f.dir)
		if len(issues) != 1 || !strings.Contains(issues[0].Title, "existing fixture issue") {
			t.Fatalf("after restoring metadata, list held %d issues, want exactly the original 1", len(issues))
		}
		f.requireDatabases(t, "beads", "myproj")
	})
}
