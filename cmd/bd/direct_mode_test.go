package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

func TestFallbackToDirectModeEnablesFlush(t *testing.T) {
	// FIX: Initialize rootCtx for flush operations (issue #355)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	origDaemonClient := daemonClient
	origDaemonStatus := daemonStatus
	origStore := store
	origStoreActive := storeActive
	origDBPath := dbPath
	origAutoImport := autoImportEnabled
	origAutoFlush := autoFlushEnabled
	origFlushFailures := flushFailureCount
	origLastFlushErr := lastFlushError
	origFlushManager := flushManager

	// Shutdown any existing FlushManager
	if flushManager != nil {
		_ = flushManager.Shutdown()
		flushManager = nil
	}

	defer func() {
		if store != nil && store != origStore {
			_ = store.Close()
		}
		storeMutex.Lock()
		store = origStore
		storeActive = origStoreActive
		storeMutex.Unlock()

		daemonClient = origDaemonClient
		daemonStatus = origDaemonStatus
		dbPath = origDBPath
		autoImportEnabled = origAutoImport
		autoFlushEnabled = origAutoFlush
		flushFailureCount = origFlushFailures
		lastFlushError = origLastFlushErr

		// Restore FlushManager
		if flushManager != nil {
			_ = flushManager.Shutdown()
		}
		flushManager = origFlushManager
	}()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	testDBPath := filepath.Join(beadsDir, "test.db")

	// Seed database with issues
	setupStore := newTestStore(t, testDBPath)

	setupCtx := context.Background()
	target := &types.Issue{
		Title:     "Issue to delete",
		IssueType: types.TypeTask,
		Priority:  2,
		Status:    types.StatusOpen,
	}
	if err := setupStore.CreateIssue(setupCtx, target, "test"); err != nil {
		t.Fatalf("failed to create target issue: %v", err)
	}

	neighbor := &types.Issue{
		Title:       "Neighbor issue",
		Description: "See " + target.ID,
		IssueType:   types.TypeTask,
		Priority:    2,
		Status:      types.StatusOpen,
	}
	if err := setupStore.CreateIssue(setupCtx, neighbor, "test"); err != nil {
		t.Fatalf("failed to create neighbor issue: %v", err)
	}
	if err := setupStore.Close(); err != nil {
		t.Fatalf("failed to close seed store: %v", err)
	}

	// Simulate daemon-connected state before fallback
	dbPath = testDBPath
	storeMutex.Lock()
	store = nil
	storeActive = false
	storeMutex.Unlock()
	daemonClient = &rpc.Client{}
	daemonStatus = DaemonStatus{}
	autoImportEnabled = false
	autoFlushEnabled = true

	if err := fallbackToDirectMode("test fallback"); err != nil {
		t.Fatalf("fallbackToDirectMode failed: %v", err)
	}

	if daemonClient != nil {
		t.Fatal("expected daemonClient to be nil after fallback")
	}

	storeMutex.Lock()
	active := storeActive && store != nil
	storeMutex.Unlock()
	if !active {
		t.Fatal("expected store to be active after fallback")
	}

	// Force a full export and flush synchronously
	flushToJSONLWithState(flushState{forceDirty: true, forceFullExport: true})

	jsonlPath := findJSONLPath()
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read JSONL export: %v", err)
	}

	if !bytes.Contains(data, []byte(target.ID)) {
		t.Fatalf("expected JSONL export to contain deleted issue ID %s", target.ID)
	}
	if !bytes.Contains(data, []byte(neighbor.ID)) {
		t.Fatalf("expected JSONL export to contain neighbor issue ID %s", neighbor.ID)
	}
}

// TestImportFromJSONLInlineAfterDaemonDisconnect verifies that importFromJSONLInline
// works after daemon disconnect when ensureStoreActive is called.
//
// This tests the fix for the bug where `bd sync --import-only` fails with
// "no database store available for inline import" when daemon mode was active.
//
// The bug occurs because:
// 1. PersistentPreRun connects to daemon and returns early (store = nil)
// 2. sync command closes daemon connection
// 3. sync --import-only calls importFromJSONLInline which requires store != nil
// 4. Without ensureStoreActive(), the store is never initialized
//
// The fix: call ensureStoreActive() after closing daemon in sync.go
func TestImportFromJSONLInlineAfterDaemonDisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Save and restore all global state
	oldRootCtx := rootCtx
	rootCtx = ctx
	origDaemonClient := daemonClient
	origDaemonStatus := daemonStatus
	origStore := store
	origStoreActive := storeActive
	origDBPath := dbPath
	origAutoImport := autoImportEnabled

	defer func() {
		rootCtx = oldRootCtx
		if store != nil && store != origStore {
			_ = store.Close()
		}
		storeMutex.Lock()
		store = origStore
		storeActive = origStoreActive
		storeMutex.Unlock()
		daemonClient = origDaemonClient
		daemonStatus = origDaemonStatus
		dbPath = origDBPath
		autoImportEnabled = origAutoImport
	}()

	// Setup: Create temp directory with .beads structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create and seed the database
	setupStore := newTestStore(t, testDBPath)
	issue := &types.Issue{
		Title:     "Test Issue",
		IssueType: types.TypeTask,
		Priority:  2,
		Status:    types.StatusOpen,
	}
	if err := setupStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	issueID := issue.ID

	// Export to JSONL
	issues, err := setupStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}
	for _, iss := range issues {
		data, _ := json.Marshal(iss)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	f.Close()

	// Close setup store
	if err := setupStore.Close(); err != nil {
		t.Fatalf("failed to close setup store: %v", err)
	}

	// Simulate daemon-connected state (as PersistentPreRun leaves it)
	dbPath = testDBPath
	storeMutex.Lock()
	store = nil
	storeActive = false
	storeMutex.Unlock()
	daemonClient = &rpc.Client{} // Non-nil means daemon was connected
	autoImportEnabled = false

	// Simulate what sync.go does: close daemon but DON'T initialize store
	// This is the bug scenario
	_ = daemonClient.Close()
	daemonClient = nil

	// BUG: Without ensureStoreActive(), importFromJSONLInline fails
	err = importFromJSONLInline(ctx, jsonlPath, false, false)
	if err == nil {
		t.Fatal("expected importFromJSONLInline to fail when store is nil")
	}
	if err.Error() != "no database store available for inline import" {
		t.Fatalf("unexpected error: %v", err)
	}

	// FIX: Call ensureStoreActive() after daemon disconnect
	if err := ensureStoreActive(); err != nil {
		t.Fatalf("ensureStoreActive failed: %v", err)
	}

	// Now importFromJSONLInline should work
	err = importFromJSONLInline(ctx, jsonlPath, false, false)
	if err != nil {
		t.Fatalf("importFromJSONLInline failed after ensureStoreActive: %v", err)
	}

	// Verify the import worked by checking the issue exists
	storeMutex.Lock()
	currentStore := store
	storeMutex.Unlock()

	imported, err := currentStore.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("failed to get imported issue: %v", err)
	}
	if imported.Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %q", imported.Title)
	}
}
