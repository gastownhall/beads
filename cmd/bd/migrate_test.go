//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestMigrateCommand removed: detectDatabases, getDBVersion, formatDBList, dbInfo
// were removed in Dolt-native pruning. Migration is now handled by bd init --from-jsonl.

// TestFormatDBList removed: formatDBList and dbInfo types were removed.

func TestMigrateSchemaFlagBoundaries(t *testing.T) {
	// Registration only. --json must come from the root persistent flag: a
	// local duplicate shadows it, so PersistentPreRunE never sees the root
	// flag as explicitly set and resets jsonOutput from config.
	for _, name := range []string{"inspect", "dry-run"} {
		if flag := migrateSchemaCmd.Flags().Lookup(name); flag != nil {
			t.Errorf("migrate schema unexpectedly registers local --%s", name)
		}
	}
	if f := migrateSchemaCmd.Flags().Lookup("json"); f != nil && f != rootCmd.PersistentFlags().Lookup("json") {
		t.Error("migrate schema must not register a local --json; it inherits the root flag")
	}
	if flag := migrateSchemaCmd.Flags().Lookup("force"); flag == nil {
		t.Error("migrate schema must register local --force")
	}
	if flag := rootCmd.PersistentFlags().Lookup("json"); flag == nil {
		t.Error("root command must register persistent --json")
	}
	if flag := rootCmd.PersistentFlags().Lookup("format"); flag == nil {
		t.Error("root command must register persistent --format")
	}
}

// TestSchemaMigrateFailedDirtyTablesHint pins the hint `bd migrate schema`
// gives when the dirty-table guard refuses its migration (#6929): commit the
// working set with a plain `bd dolt commit` on the database this command
// targeted, then re-run it. That commit opens leniently, so the hint no
// longer routes around it with raw SQL or the consent env var.
func TestSchemaMigrateFailedDirtyTablesHint(t *testing.T) {
	pinJSONOutput(t, false)
	dirty := fmt.Errorf("failed to initialize schema: %w", &schema.DirtyTablesError{Tables: []string{"issues"}})
	for _, tt := range []struct {
		global bool
		commit string
	}{
		{global: false, commit: "bd dolt commit"},
		{global: true, commit: "bd dolt commit --global"},
	} {
		t.Run(fmt.Sprintf("global=%v", tt.global), func(t *testing.T) {
			orig := globalFlag
			globalFlag = tt.global
			t.Cleanup(func() { globalFlag = orig })

			var err error
			out := captureNoticeStderr(t, func() { err = schemaMigrateFailed(dirty) })
			if err == nil {
				t.Fatal("schemaMigrateFailed returned nil for a refused migration")
			}
			want := "Hint: commit the working set with `" + tt.commit + "`, then re-run this command"
			if !strings.Contains(out, want) {
				t.Errorf("hint is missing %q:\n%s", want, out)
			}
			if strings.Contains(out, "DOLT_COMMIT(") || strings.Contains(out, schema.AllowRemoteMigrateEnv) {
				t.Errorf("hint still routes around `bd dolt commit`:\n%s", out)
			}
		})
	}
}

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
	if err := store.SetLocalMetadata(ctx, "bd_version", "0.21.1"); err != nil {
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
