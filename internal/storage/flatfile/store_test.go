package flatfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewFlatFileStore(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")

	s, err := NewFlatFileStore(beadsDir)
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}
	defer s.Close()

	// Verify subdirectories were created.
	for _, sub := range []string{"issues", "comments", "memories", "events"} {
		path := filepath.Join(beadsDir, sub)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", sub)
		}
	}

	// Verify Path and CLIDir.
	if got := s.Path(); got != beadsDir {
		t.Errorf("Path() = %q, want %q", got, beadsDir)
	}
	if got := s.CLIDir(); got != beadsDir {
		t.Errorf("CLIDir() = %q, want %q", got, beadsDir)
	}
}

func TestCloseAndIsClosed(t *testing.T) {
	s, err := NewFlatFileStore(filepath.Join(t.TempDir(), ".beads"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}

	if s.IsClosed() {
		t.Error("IsClosed() = true before Close()")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !s.IsClosed() {
		t.Error("IsClosed() = false after Close()")
	}
}

func TestCheckClosed(t *testing.T) {
	s, err := NewFlatFileStore(filepath.Join(t.TempDir(), ".beads"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}

	if err := s.checkClosed(); err != nil {
		t.Errorf("checkClosed before Close: %v", err)
	}

	s.Close()

	if err := s.checkClosed(); err == nil {
		t.Error("checkClosed after Close: expected error, got nil")
	}
}

func TestNewFlatFileStoreIdempotent(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")

	// Create twice — second call should not fail.
	s1, err := NewFlatFileStore(beadsDir)
	if err != nil {
		t.Fatalf("first NewFlatFileStore: %v", err)
	}
	s1.Close()

	s2, err := NewFlatFileStore(beadsDir)
	if err != nil {
		t.Fatalf("second NewFlatFileStore: %v", err)
	}
	s2.Close()
}
