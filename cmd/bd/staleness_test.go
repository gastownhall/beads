package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestEnsureDatabaseFresh_NoAutoImportFlag verifies that when noAutoImport is
// true, ensureDatabaseFresh returns an error instead of auto-importing.
func TestEnsureDatabaseFresh_NoAutoImportFlag(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "bd.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create database
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Set prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Set an old last_import_time to make DB appear stale
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := testStore.SetMetadata(ctx, "last_import_time", oldTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("Failed to set metadata: %v", err)
	}

	// Create JSONL with a new issue
	jsonlIssue := &types.Issue{
		ID:        "test-noauto-bd9dao",
		Title:     "Should NOT Auto Import",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(jsonlIssue); err != nil {
		t.Fatalf("Failed to encode issue: %v", err)
	}
	f.Close()

	// Save and set global state
	oldNoAutoImport := noAutoImport
	oldAutoImportEnabled := autoImportEnabled
	oldStore := store
	oldDbPath := dbPath
	oldRootCtx := rootCtx
	oldStoreActive := storeActive
	oldAllowStale := allowStale

	noAutoImport = true       // Disable auto-import
	autoImportEnabled = false // Disable auto-import
	allowStale = false        // Don't skip staleness check
	store = testStore
	dbPath = testDBPath
	rootCtx = ctx
	storeActive = true

	// Ensure sync mode is git-portable so JSONL staleness check runs
	oldSyncMode := config.GetString("sync.mode")
	config.Set("sync.mode", "git-portable")

	defer func() {
		config.Set("sync.mode", oldSyncMode)
		noAutoImport = oldNoAutoImport
		autoImportEnabled = oldAutoImportEnabled
		allowStale = oldAllowStale
		store = oldStore
		dbPath = oldDbPath
		rootCtx = oldRootCtx
		storeActive = oldStoreActive
	}()

	// Call ensureDatabaseFresh - should return error since noAutoImport is set
	err = ensureDatabaseFresh(ctx)
	if err == nil {
		t.Error("ensureDatabaseFresh() should have returned error when noAutoImport is true")
	}

	// Verify issue was NOT imported
	imported, err := testStore.GetIssue(ctx, "test-noauto-bd9dao")
	if err != nil {
		t.Fatalf("Failed to check for issue: %v", err)
	}
	if imported != nil {
		t.Error("ensureDatabaseFresh() imported despite noAutoImport=true")
	}
}

// TestEnsureDatabaseFresh_AllowStaleFlag verifies that when allowStale is
// true, ensureDatabaseFresh skips the check entirely.
func TestEnsureDatabaseFresh_AllowStaleFlag(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "bd.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create database
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Set prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Set an old last_import_time to make DB appear stale
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := testStore.SetMetadata(ctx, "last_import_time", oldTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("Failed to set metadata: %v", err)
	}

	// Create JSONL with a new issue
	jsonlIssue := &types.Issue{
		ID:        "test-allowstale-bd9dao",
		Title:     "Should Skip Check",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(jsonlIssue); err != nil {
		t.Fatalf("Failed to encode issue: %v", err)
	}
	f.Close()

	// Save and set global state
	oldNoAutoImport := noAutoImport
	oldAutoImportEnabled := autoImportEnabled
	oldStore := store
	oldDbPath := dbPath
	oldRootCtx := rootCtx
	oldStoreActive := storeActive
	oldAllowStale := allowStale

	noAutoImport = true // Disable auto-import (shouldn't matter with allowStale)
	autoImportEnabled = false
	allowStale = true // Skip staleness check entirely
	store = testStore
	dbPath = testDBPath
	rootCtx = ctx
	storeActive = true

	defer func() {
		noAutoImport = oldNoAutoImport
		autoImportEnabled = oldAutoImportEnabled
		allowStale = oldAllowStale
		store = oldStore
		dbPath = oldDbPath
		rootCtx = oldRootCtx
		storeActive = oldStoreActive
	}()

	// Call ensureDatabaseFresh - should return nil (skip check)
	err = ensureDatabaseFresh(ctx)
	if err != nil {
		t.Errorf("ensureDatabaseFresh() should have returned nil with allowStale=true: %v", err)
	}

	// Verify issue was NOT imported (check was skipped entirely)
	imported, err := testStore.GetIssue(ctx, "test-allowstale-bd9dao")
	if err != nil {
		t.Fatalf("Failed to check for issue: %v", err)
	}
	if imported != nil {
		t.Error("ensureDatabaseFresh() imported even though allowStale=true (should skip check entirely)")
	}
}

// TestEnsureDatabaseFresh_FreshDB verifies that when the database is fresh,
// ensureDatabaseFresh returns nil without doing anything.
func TestEnsureDatabaseFresh_FreshDB(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "bd.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create JSONL first
	jsonlIssue := &types.Issue{
		ID:        "test-fresh-bd9dao",
		Title:     "Fresh DB Test",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(jsonlIssue); err != nil {
		t.Fatalf("Failed to encode issue: %v", err)
	}
	f.Close()

	// Create database
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Set prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Set a future last_import_time to make DB appear fresh
	futureTime := time.Now().Add(1 * time.Hour)
	if err := testStore.SetMetadata(ctx, "last_import_time", futureTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("Failed to set metadata: %v", err)
	}

	// Save and set global state
	oldNoAutoImport := noAutoImport
	oldAutoImportEnabled := autoImportEnabled
	oldStore := store
	oldDbPath := dbPath
	oldRootCtx := rootCtx
	oldStoreActive := storeActive
	oldAllowStale := allowStale

	noAutoImport = false
	autoImportEnabled = true
	allowStale = false
	store = testStore
	dbPath = testDBPath
	rootCtx = ctx
	storeActive = true

	// Ensure sync mode is git-portable so JSONL staleness check runs
	oldSyncMode := config.GetString("sync.mode")
	config.Set("sync.mode", "git-portable")

	defer func() {
		config.Set("sync.mode", oldSyncMode)
		noAutoImport = oldNoAutoImport
		autoImportEnabled = oldAutoImportEnabled
		allowStale = oldAllowStale
		store = oldStore
		dbPath = oldDbPath
		rootCtx = oldRootCtx
		storeActive = oldStoreActive
	}()

	// Call ensureDatabaseFresh - should return nil (DB is fresh)
	err = ensureDatabaseFresh(ctx)
	if err != nil {
		t.Errorf("ensureDatabaseFresh() should have returned nil for fresh DB: %v", err)
	}
}

// TestEnsureDatabaseFresh_DoltNativeSkipsCheck verifies that in dolt-native mode,
// ensureDatabaseFresh skips the JSONL staleness check entirely. Dolt is the source
// of truth in this mode, so JSONL divergence is expected and should not block reads.
func TestEnsureDatabaseFresh_DoltNativeSkipsCheck(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "bd.db")

	// Create database
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Set prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Save and set global state
	oldNoAutoImport := noAutoImport
	oldAutoImportEnabled := autoImportEnabled
	oldStore := store
	oldDbPath := dbPath
	oldRootCtx := rootCtx
	oldStoreActive := storeActive
	oldAllowStale := allowStale

	noAutoImport = false
	autoImportEnabled = true
	allowStale = false
	store = testStore
	dbPath = testDBPath
	rootCtx = ctx
	storeActive = true

	// Set dolt-native mode — staleness check should be skipped entirely
	oldSyncMode := config.GetString("sync.mode")
	config.Set("sync.mode", "dolt-native")

	defer func() {
		config.Set("sync.mode", oldSyncMode)
		noAutoImport = oldNoAutoImport
		autoImportEnabled = oldAutoImportEnabled
		allowStale = oldAllowStale
		store = oldStore
		dbPath = oldDbPath
		rootCtx = oldRootCtx
		storeActive = oldStoreActive
	}()

	// Call ensureDatabaseFresh — should return nil without checking JSONL
	err = ensureDatabaseFresh(ctx)
	if err != nil {
		t.Errorf("ensureDatabaseFresh() should skip check in dolt-native mode: %v", err)
	}
}
