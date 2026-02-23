//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestMigrateCommand removed: detectDatabases, getDBVersion, formatDBList, dbInfo
// were removed in Dolt-native pruning. Migration is now handled by bd init --to-dolt.

// TestFormatDBList removed: formatDBList and dbInfo types were removed.

func TestMigrateRespectsConfigJSON(t *testing.T) {
	t.Skip("SQLite-specific: Dolt backend always uses 'dolt' directory, not custom database filenames")
	// Test that migrate respects custom database name from metadata.json
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create metadata.json with custom database name
	configPath := filepath.Join(beadsDir, "metadata.json")
	configData := `{"database": "beady.db", "version": "0.21.1"}`
	if err := os.WriteFile(configPath, []byte(configData), 0600); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	// Create old database with custom name
	oldDBPath := filepath.Join(beadsDir, "beady.db")
	store, err := dolt.New(context.Background(), &dolt.Config{Path: oldDBPath})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	ctx := context.Background()
	if err := store.SetMetadata(ctx, "bd_version", "0.21.1"); err != nil {
		t.Fatalf("Failed to set version: %v", err)
	}
	_ = store.Close()

	// Load config
	cfg, err := loadOrCreateConfig(beadsDir)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify config respects custom database name
	if cfg.Database != "beady.db" {
		t.Errorf("Expected database name 'beady.db', got %s", cfg.Database)
	}

	expectedPath := filepath.Join(beadsDir, "beady.db")
	actualPath := cfg.DatabasePath(beadsDir)
	if actualPath != expectedPath {
		t.Errorf("Expected path %s, got %s", expectedPath, actualPath)
	}

	// Verify database exists at custom path
	if _, err := os.Stat(actualPath); os.IsNotExist(err) {
		t.Errorf("Database does not exist at custom path: %s", actualPath)
	}
}
