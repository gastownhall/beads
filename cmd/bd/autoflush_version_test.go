package main

import (
	"context"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestCheckVersionMismatch_NoVersion(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDB := tmpDir + "/test.db"
	
	sqliteStore, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	
	// Set prefix to initialize DB
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Save and restore global store
	oldStore := store
	store = sqliteStore
	defer func() { store = oldStore }()

	// Should not panic when no version is set
	checkVersionMismatch()

	// Should have set the version
	version, err := sqliteStore.GetMetadata(ctx, "bd_version")
	if err != nil {
		t.Fatalf("Failed to get version: %v", err)
	}
	
	if version != Version {
		t.Errorf("Expected version %s, got %s", Version, version)
	}
}

func TestCheckVersionMismatch_SameVersion(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDB := tmpDir + "/test.db"
	
	sqliteStore, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	
	// Set prefix to initialize DB
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Set same version
	if err := sqliteStore.SetMetadata(ctx, "bd_version", Version); err != nil {
		t.Fatalf("Failed to set version: %v", err)
	}

	// Save and restore global store
	oldStore := store
	store = sqliteStore
	defer func() { store = oldStore }()

	// Should not print warning (we can't easily test stderr, but ensure no panic)
	checkVersionMismatch()
}

func TestCheckVersionMismatch_OlderBinary(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDB := tmpDir + "/test.db"
	
	sqliteStore, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	
	// Set prefix to initialize DB
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Set a newer version in DB
	if err := sqliteStore.SetMetadata(ctx, "bd_version", "99.99.99"); err != nil {
		t.Fatalf("Failed to set version: %v", err)
	}

	// Save and restore global store
	oldStore := store
	store = sqliteStore
	defer func() { store = oldStore }()

	// Should print warning (we can't easily test stderr, but ensure no panic)
	checkVersionMismatch()
}

func TestCheckVersionMismatch_NewerBinary(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDB := tmpDir + "/test.db"
	
	sqliteStore, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	
	// Set prefix to initialize DB
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Set an older version in DB
	if err := sqliteStore.SetMetadata(ctx, "bd_version", "0.1.0"); err != nil {
		t.Fatalf("Failed to set version: %v", err)
	}

	// Save and restore global store
	oldStore := store
	store = sqliteStore
	defer func() { store = oldStore }()

	// Should print warning and update version
	checkVersionMismatch()

	// Check that version was updated
	version, err := sqliteStore.GetMetadata(ctx, "bd_version")
	if err != nil {
		t.Fatalf("Failed to get version: %v", err)
	}
	
	if version != Version {
		t.Errorf("Expected version to be updated to %s, got %s", Version, version)
	}
}

func TestCheckVersionMismatch_DebugMode(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDB := tmpDir + "/test.db"
	
	sqliteStore, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	
	// Set prefix to initialize DB
	if err := sqliteStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Save and restore global store
	oldStore := store
	store = sqliteStore
	defer func() { store = oldStore }()

	// Set debug mode
	os.Setenv("BD_DEBUG", "1")
	defer os.Unsetenv("BD_DEBUG")

	// Close the store to trigger metadata error
	sqliteStore.Close()

	// Should not panic even with error in debug mode
	checkVersionMismatch()
}
