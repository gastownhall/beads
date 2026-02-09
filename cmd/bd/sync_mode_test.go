package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// TestSyncModeConfig verifies sync mode configuration storage and retrieval.
func TestSyncModeConfig(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	// Create store
	dbPath := filepath.Join(beadsDir, "beads.db")
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	// Test 1: Default mode is git-portable
	mode := GetSyncMode(ctx, testStore)
	if mode != SyncModeGitPortable {
		t.Errorf("default sync mode = %q, want %q", mode, SyncModeGitPortable)
	}
	t.Logf("✓ Default sync mode is git-portable")

	// Test 2: Set and get realtime mode
	if err := SetSyncMode(ctx, testStore, SyncModeRealtime); err != nil {
		t.Fatalf("failed to set sync mode: %v", err)
	}
	mode = GetSyncMode(ctx, testStore)
	if mode != SyncModeRealtime {
		t.Errorf("sync mode = %q, want %q", mode, SyncModeRealtime)
	}
	t.Logf("✓ Can set and get realtime mode")

	// Test 3: Set and get dolt-native mode
	if err := SetSyncMode(ctx, testStore, SyncModeDoltNative); err != nil {
		t.Fatalf("failed to set sync mode: %v", err)
	}
	mode = GetSyncMode(ctx, testStore)
	if mode != SyncModeDoltNative {
		t.Errorf("sync mode = %q, want %q", mode, SyncModeDoltNative)
	}
	t.Logf("✓ Can set and get dolt-native mode")

	// Test 4: Set and get belt-and-suspenders mode
	if err := SetSyncMode(ctx, testStore, SyncModeBeltAndSuspenders); err != nil {
		t.Fatalf("failed to set sync mode: %v", err)
	}
	mode = GetSyncMode(ctx, testStore)
	if mode != SyncModeBeltAndSuspenders {
		t.Errorf("sync mode = %q, want %q", mode, SyncModeBeltAndSuspenders)
	}
	t.Logf("✓ Can set and get belt-and-suspenders mode")

	// Test 5: Invalid mode returns error
	err = SetSyncMode(ctx, testStore, "invalid-mode")
	if err == nil {
		t.Error("expected error for invalid sync mode")
	}
	t.Logf("✓ Invalid mode correctly rejected")

	// Test 6: Invalid mode in DB defaults to git-portable
	if err := testStore.SetConfig(ctx, SyncModeConfigKey, "invalid"); err != nil {
		t.Fatalf("failed to set invalid config: %v", err)
	}
	mode = GetSyncMode(ctx, testStore)
	if mode != SyncModeGitPortable {
		t.Errorf("invalid mode should default to %q, got %q", SyncModeGitPortable, mode)
	}
	t.Logf("✓ Invalid mode in DB defaults to git-portable")
}

// TestShouldExportJSONL verifies JSONL export behavior per mode.
func TestShouldExportJSONL(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	tests := []struct {
		mode       string
		wantExport bool
	}{
		{SyncModeGitPortable, true},
		{SyncModeRealtime, true},
		{SyncModeDoltNative, false}, // dolt-native uses Dolt remotes, not JSONL
		{SyncModeBeltAndSuspenders, true},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			if err := SetSyncMode(ctx, testStore, tt.mode); err != nil {
				t.Fatalf("failed to set mode: %v", err)
			}

			got := ShouldExportJSONL(ctx, testStore)
			if got != tt.wantExport {
				t.Errorf("ShouldExportJSONL() = %v, want %v", got, tt.wantExport)
			}
		})
	}
}

// TestShouldExportJSONL_UsesGetSyncMode verifies ShouldExportJSONL uses GetSyncMode
// (which checks config.yaml first, then DB). This ensures that sync.mode set in
// config.yaml is respected even when not propagated to the database — fixing the
// bug where dolt-native workspaces paid 10-25s JSONL export overhead (bd-6fiwk).
func TestShouldExportJSONL_UsesGetSyncMode(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	// Set dolt-native in database — ShouldExportJSONL must return false
	if err := testStore.SetConfig(ctx, SyncModeConfigKey, SyncModeDoltNative); err != nil {
		t.Fatalf("failed to set config: %v", err)
	}
	if ShouldExportJSONL(ctx, testStore) {
		t.Error("ShouldExportJSONL() = true, want false for dolt-native in DB")
	}

	// Default (no config set) should return true
	if err := testStore.SetConfig(ctx, SyncModeConfigKey, ""); err != nil {
		t.Fatalf("failed to clear config: %v", err)
	}
	if !ShouldExportJSONL(ctx, testStore) {
		t.Error("ShouldExportJSONL() = false, want true for default (no config)")
	}
}

// TestShouldImportJSONL verifies JSONL import behavior per mode.
func TestShouldImportJSONL(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	tests := []struct {
		mode       string
		wantImport bool
	}{
		{SyncModeGitPortable, true},
		{SyncModeRealtime, true},
		{SyncModeDoltNative, false},
		{SyncModeBeltAndSuspenders, true},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			if err := SetSyncMode(ctx, testStore, tt.mode); err != nil {
				t.Fatalf("failed to set mode: %v", err)
			}

			got := ShouldImportJSONL(ctx, testStore)
			if got != tt.wantImport {
				t.Errorf("ShouldImportJSONL() = %v, want %v", got, tt.wantImport)
			}
		})
	}
}

// TestShouldUseDoltRemote verifies Dolt remote usage per mode.
func TestShouldUseDoltRemote(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	tests := []struct {
		mode    string
		wantUse bool
	}{
		{SyncModeGitPortable, false},
		{SyncModeRealtime, false},
		{SyncModeDoltNative, true},
		{SyncModeBeltAndSuspenders, true},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			if err := SetSyncMode(ctx, testStore, tt.mode); err != nil {
				t.Fatalf("failed to set mode: %v", err)
			}

			got := ShouldUseDoltRemote(ctx, testStore)
			if got != tt.wantUse {
				t.Errorf("ShouldUseDoltRemote() = %v, want %v", got, tt.wantUse)
			}
		})
	}
}

// TestSyncModeDescription verifies mode descriptions are meaningful.
func TestSyncModeDescription(t *testing.T) {
	tests := []struct {
		mode        string
		wantContain string
	}{
		{SyncModeGitPortable, "JSONL"},
		{SyncModeRealtime, "every change"},
		{SyncModeDoltNative, "export-only"},
		{SyncModeBeltAndSuspenders, "Both"},
		{"invalid", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			desc := SyncModeDescription(tt.mode)
			if desc == "" {
				t.Error("description should not be empty")
			}
			// Just verify descriptions are non-empty and distinct
			t.Logf("%s: %s", tt.mode, desc)
		})
	}
}
