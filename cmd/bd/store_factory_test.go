//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestNewDoltStoreFromConfig_NoMetadata verifies that newDoltStoreFromConfig
// succeeds when the beads directory has no metadata.json (fresh project).
// Regression test for GH#2988: "no database selected" error.
func TestNewDoltStoreFromConfig_NoMetadata(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	beadsDir := t.TempDir()

	// Confirm no config exists.
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("unexpected error loading config: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config for empty dir")
	}

	// This should succeed using the default database name, not fail with
	// "no database selected".
	store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("newDoltStoreFromConfig failed: %v", err)
	}
	defer store.Close()
}

// TestEmbeddedNew_EmptyDatabaseRejected verifies that embeddeddolt.New fails
// with a clear error when called with an empty database name, rather than
// deferring to a confusing "no database selected" SQL error.
// Belt-and-suspenders defense for be-sy8 / GH#2988.
func TestEmbeddedNew_EmptyDatabaseRejected(t *testing.T) {
	_, err := embeddeddolt.New(t.Context(), t.TempDir(), "", "main")
	if err == nil {
		t.Fatal("expected error for empty database name")
	}
	if !strings.Contains(err.Error(), "database name must not be empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestNewDoltStoreFromConfig_HyphenatedDBName verifies that
// newDoltStoreFromConfig auto-sanitizes hyphenated database names for embedded
// mode and persists the fix to metadata.json.
// Regression test for GH#3231: pre-#2142 projects break on embedded upgrade.
func TestNewDoltStoreFromConfig_HyphenatedDBName(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	beadsDir := t.TempDir()

	cfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "my-cool-project",
		DoltMode:     configfile.DoltModeEmbedded,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("newDoltStoreFromConfig failed (should have auto-sanitized): %v", err)
	}
	defer store.Close()

	reloaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if reloaded.DoltDatabase != "my_cool_project" {
		t.Errorf("expected dolt_database to be sanitized to %q, got %q", "my_cool_project", reloaded.DoltDatabase)
	}
}

// TestSanitizeDBName verifies the sanitization logic for database names.
func TestSanitizeDBName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-project", "my_project"},
		{"jtbot-core", "jtbot_core"},
		{"no-hyphens-here", "no_hyphens_here"},
		{"dots.and-hyphens", "dots_and_hyphens"},
		{"already_clean", "already_clean"},
		{"beads", "beads"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeDBName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeDBName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestMigrateHyphenatedDB_PersistsToMetadata verifies that migrateHyphenatedDB
// updates metadata.json with the sanitized database name.
func TestMigrateHyphenatedDB_PersistsToMetadata(t *testing.T) {
	beadsDir := t.TempDir()

	cfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "my-project",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	if err := migrateHyphenatedDB(beadsDir, cfg, "my-project", "my_project"); err != nil {
		t.Fatalf("migrateHyphenatedDB failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("failed to read metadata.json: %v", err)
	}

	var saved configfile.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("failed to parse metadata.json: %v", err)
	}
	if saved.DoltDatabase != "my_project" {
		t.Errorf("expected dolt_database %q in metadata.json, got %q", "my_project", saved.DoltDatabase)
	}
}

// TestMigrateHyphenatedDB_RenamesDirectory verifies that migrateHyphenatedDB
// renames the old hyphenated database directory to the sanitized name.
func TestMigrateHyphenatedDB_RenamesDirectory(t *testing.T) {
	beadsDir := t.TempDir()

	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	oldDir := filepath.Join(dataDir, "my-project")
	newDir := filepath.Join(dataDir, "my_project")

	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("failed to create old dir: %v", err)
	}
	sentinel := filepath.Join(oldDir, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write sentinel: %v", err)
	}

	cfg := &configfile.Config{DoltDatabase: "my-project"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	if err := migrateHyphenatedDB(beadsDir, cfg, "my-project", "my_project"); err != nil {
		t.Fatalf("migrateHyphenatedDB failed: %v", err)
	}

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("old directory should no longer exist after rename")
	}
	if _, err := os.Stat(filepath.Join(newDir, "sentinel.txt")); err != nil {
		t.Error("sentinel file should exist in renamed directory")
	}
}

// TestMigrateHyphenatedDB_CollisionError verifies that migrateHyphenatedDB
// returns an error when both old and new directories exist (GH#3231).
func TestMigrateHyphenatedDB_CollisionError(t *testing.T) {
	beadsDir := t.TempDir()

	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	oldDir := filepath.Join(dataDir, "my-project")
	newDir := filepath.Join(dataDir, "my_project")

	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("failed to create old dir: %v", err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("failed to create new dir: %v", err)
	}

	cfg := &configfile.Config{DoltDatabase: "my-project"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	err := migrateHyphenatedDB(beadsDir, cfg, "my-project", "my_project")
	if err == nil {
		t.Fatal("expected error when both directories exist, got nil")
	}
	if !strings.Contains(err.Error(), "both") {
		t.Errorf("expected collision error message, got: %v", err)
	}
}

// TestMigrateHyphenatedDB_NoOldDir verifies that migrateHyphenatedDB still
// updates metadata.json even when the old directory doesn't exist (e.g., fresh
// project where only metadata.json has the bad name).
func TestMigrateHyphenatedDB_NoOldDir(t *testing.T) {
	beadsDir := t.TempDir()

	cfg := &configfile.Config{DoltDatabase: "my-project"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	if err := migrateHyphenatedDB(beadsDir, cfg, "my-project", "my_project"); err != nil {
		t.Fatalf("migrateHyphenatedDB failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("failed to read metadata.json: %v", err)
	}
	var saved configfile.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("failed to parse metadata.json: %v", err)
	}
	if saved.DoltDatabase != "my_project" {
		t.Errorf("expected %q, got %q", "my_project", saved.DoltDatabase)
	}
}

// TestNewDoltStoreFromConfig_DottedDBName verifies that dots are also
// auto-sanitized, not just hyphens (GH#3231).
func TestNewDoltStoreFromConfig_DottedDBName(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	beadsDir := t.TempDir()

	cfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "my.project",
		DoltMode:     configfile.DoltModeEmbedded,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("newDoltStoreFromConfig failed (should have auto-sanitized dots): %v", err)
	}
	defer store.Close()

	reloaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if reloaded.DoltDatabase != "my_project" {
		t.Errorf("expected dolt_database %q, got %q", "my_project", reloaded.DoltDatabase)
	}
}
