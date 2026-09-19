//go:build cgo

package embeddeddolt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActiveDatabaseSizeScopesToActiveEmbeddedDatabase(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	active := filepath.Join(root, "active")
	sibling := filepath.Join(root, "sibling")
	if err := os.Mkdir(active, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "data"), []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "data"), []byte("much larger sibling data"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &EmbeddedDoltStore{dataDir: root, database: "active"}
	got, err := store.ActiveDatabaseSize(t.Context())
	if err != nil {
		t.Fatalf("ActiveDatabaseSize: %v", err)
	}
	if got != int64(len("active")) {
		t.Fatalf("ActiveDatabaseSize = %d, want %d", got, len("active"))
	}
}

func TestActiveDatabaseSizeFailsForMissingEmbeddedDatabase(t *testing.T) {
	t.Parallel()

	store := &EmbeddedDoltStore{dataDir: t.TempDir(), database: "missing"}
	if _, err := store.ActiveDatabaseSize(t.Context()); err == nil {
		t.Fatal("ActiveDatabaseSize succeeded, want missing-path error")
	}
}
