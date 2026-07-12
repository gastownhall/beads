package flatfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestIsFlatFileBackend(t *testing.T) {
	dir := t.TempDir()

	// No metadata.json → not flatfile
	if IsFlatFileBackend(dir) {
		t.Error("empty dir should not be flatfile backend")
	}

	// Dolt metadata → not flatfile
	os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644)
	if IsFlatFileBackend(dir) {
		t.Error("dolt backend should not be flatfile")
	}

	// Flatfile metadata → is flatfile
	os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"backend":"flatfile"}`), 0o644)
	if !IsFlatFileBackend(dir) {
		t.Error("flatfile backend should be detected")
	}

	// Empty backend field → not flatfile (backward compat = dolt)
	os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{}`), 0o644)
	if IsFlatFileBackend(dir) {
		t.Error("empty backend should default to dolt, not flatfile")
	}
}

func TestOpenStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".beads")
	os.MkdirAll(dir, 0o755)

	// Write flatfile metadata
	os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"backend":"flatfile"}`), 0o644)

	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Should be usable and implement StoreLocator
	loc, ok := store.(storage.StoreLocator)
	if !ok {
		t.Fatal("store does not implement StoreLocator")
	}
	if loc.Path() != dir {
		t.Errorf("Path() = %q, want %q", loc.Path(), dir)
	}
}

func TestOpenStoreRejectsDolt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".beads")
	os.MkdirAll(dir, 0o755)

	os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644)

	_, err := OpenStore(context.Background(), dir)
	if err == nil {
		t.Error("OpenStore should reject dolt backend")
	}
}

func TestOpenStoreNoMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".beads")
	os.MkdirAll(dir, 0o755)

	_, err := OpenStore(context.Background(), dir)
	if err == nil {
		t.Error("OpenStore should fail without metadata.json")
	}
}
