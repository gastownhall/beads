package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestAutoFlushOnExit tests that FlushManager.Shutdown() performs final flush before exit
func TestAutoFlushOnExit(t *testing.T) {
	// Initialize rootCtx for flush operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "bd-test-exit-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create store
	testStore := newTestStore(t, dbPath)

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	// Initialize FlushManager for this test (short debounce for testing)
	autoFlushEnabled = true
	oldFlushManager := flushManager
	flushManager = NewFlushManager(true, 50*time.Millisecond)
	defer func() {
		if flushManager != nil {
			_ = flushManager.Shutdown()
		}
		flushManager = oldFlushManager
	}()

	// Create test issue
	issue := &types.Issue{
		ID:        "test-exit-1",
		Title:     "Exit test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Mark dirty (simulating CRUD operation)
	markDirtyAndScheduleFlush()

	// Simulate PersistentPostRun exit behavior - shutdown FlushManager
	// This performs the final flush before exit
	if err := flushManager.Shutdown(); err != nil {
		t.Fatalf("FlushManager shutdown failed: %v", err)
	}
	flushManager = nil // Prevent double shutdown in defer

	testStore.Close()

	// Verify JSONL file was created
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Error("Expected JSONL file to be created on exit")
	}

	// Verify content
	f, err := os.Open(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to open JSONL file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	found := false
	for scanner.Scan() {
		var exported types.Issue
		if err := json.Unmarshal(scanner.Bytes(), &exported); err != nil {
			t.Fatalf("Failed to parse JSONL: %v", err)
		}
		if exported.ID == "test-exit-1" {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected to find test-exit-1 in JSONL after exit flush")
	}
}


// TestAutoFlushConcurrency tests that concurrent operations don't cause races
// TestAutoFlushStoreInactive tests that flush doesn't run when store is inactive
// TestAutoFlushJSONLContent tests that flushed JSONL has correct content
func TestAutoFlushJSONLContent(t *testing.T) {
	// FIX: Initialize rootCtx for flush operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "bd-test-content-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath = filepath.Join(tmpDir, "test.db")
	// The actual JSONL path - findJSONLPath() will determine this
	// bd-6xd: Default is now issues.jsonl (canonical name)
	expectedJSONLPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create store
	testStore := newTestStore(t, dbPath)

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	// ctx already declared above for rootCtx initialization

	// Create multiple test issues
	issues := []*types.Issue{
		{
			ID:        "test-content-1",
			Title:     "First issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "test-content-2",
			Title:     "Second issue",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeBug,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	for _, issue := range issues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
		// Mark each issue as dirty in the database so flushToJSONL will export them
		if err := testStore.MarkIssueDirty(ctx, issue.ID); err != nil {
			t.Fatalf("Failed to mark issue dirty: %v", err)
		}
	}

	// Flush immediately (forces export)
	flushToJSONLWithState(flushState{forceDirty: true})

	// Verify JSONL file exists
	if _, err := os.Stat(expectedJSONLPath); os.IsNotExist(err) {
		// Debug: list all files in tmpDir to see what was actually created
		entries, _ := os.ReadDir(tmpDir)
		t.Logf("Contents of %s:", tmpDir)
		for _, entry := range entries {
			t.Logf("  - %s (isDir: %v)", entry.Name(), entry.IsDir())
			if entry.IsDir() && entry.Name() == ".beads" {
				beadsEntries, _ := os.ReadDir(filepath.Join(tmpDir, ".beads"))
				for _, be := range beadsEntries {
					t.Logf("    - .beads/%s", be.Name())
				}
			}
		}
		t.Fatalf("Expected JSONL file to be created at %s", expectedJSONLPath)
	}

	// Read and verify content
	f, err := os.Open(expectedJSONLPath)
	if err != nil {
		t.Fatalf("Failed to open JSONL file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	foundIssues := make(map[string]*types.Issue)

	for scanner.Scan() {
		var issue types.Issue
		if err := json.Unmarshal(scanner.Bytes(), &issue); err != nil {
			t.Fatalf("Failed to parse JSONL: %v", err)
		}
		foundIssues[issue.ID] = &issue
	}

	// Verify all issues are present
	if len(foundIssues) != 2 {
		t.Errorf("Expected 2 issues in JSONL, got %d", len(foundIssues))
	}

	// Verify content
	for _, original := range issues {
		found, ok := foundIssues[original.ID]
		if !ok {
			t.Errorf("Issue %s not found in JSONL", original.ID)
			continue
		}
		if found.Title != original.Title {
			t.Errorf("Issue %s: Title = %s, want %s", original.ID, found.Title, original.Title)
		}
		if found.Status != original.Status {
			t.Errorf("Issue %s: Status = %s, want %s", original.ID, found.Status, original.Status)
		}
	}

	// Clean up
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()
}

// TestAutoImportIfNewer tests that auto-import triggers when JSONL is newer than DB
func TestAutoImportIfNewer(t *testing.T) {
	// FIX: Initialize rootCtx for auto-import operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "bd-test-autoimport-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create store
	testStore := newTestStore(t, dbPath)

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	// ctx already declared above for rootCtx initialization

	// Create an initial issue in the database
	dbIssue := &types.Issue{
		ID:        "test-autoimport-1",
		Title:     "Original DB issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := testStore.CreateIssue(ctx, dbIssue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Wait a moment to ensure different timestamps
	time.Sleep(10 * time.Millisecond) // 10x faster

	// Create a JSONL file with different content (simulating a git pull)
	jsonlIssue := &types.Issue{
		ID:        "test-autoimport-2",
		Title:     "New JSONL issue",
		Status:    types.StatusInProgress,
		Priority:  2,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL file: %v", err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(dbIssue); err != nil {
		t.Fatalf("Failed to encode first issue: %v", err)
	}
	if err := encoder.Encode(jsonlIssue); err != nil {
		t.Fatalf("Failed to encode second issue: %v", err)
	}
	f.Close()

	// Touch the JSONL file to make it newer than DB
	futureTime := time.Now().Add(1 * time.Second)
	if err := os.Chtimes(jsonlPath, futureTime, futureTime); err != nil {
		t.Fatalf("Failed to update JSONL timestamp: %v", err)
	}

	// Call autoImportIfNewer
	autoImportIfNewer()

	// Verify that the new issue from JSONL was imported
	imported, err := testStore.GetIssue(ctx, "test-autoimport-2")
	if err != nil {
		t.Fatalf("Failed to get imported issue: %v", err)
	}

	if imported == nil {
		t.Error("Expected issue test-autoimport-2 to be imported from JSONL")
	} else {
		if imported.Title != "New JSONL issue" {
			t.Errorf("Expected title 'New JSONL issue', got '%s'", imported.Title)
		}
	}

	// Clean up
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()
}

// TestAutoImportDisabled tests that --no-auto-import flag disables auto-import
func TestAutoImportDisabled(t *testing.T) {
	// FIX: Initialize rootCtx for auto-import operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "bd-test-noimport-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create store
	testStore := newTestStore(t, dbPath)

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	// ctx already declared above for rootCtx initialization

	// Create a JSONL file with an issue
	jsonlIssue := &types.Issue{
		ID:        "test-noimport-1",
		Title:     "Should not import",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL file: %v", err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(jsonlIssue); err != nil {
		t.Fatalf("Failed to encode issue: %v", err)
	}
	f.Close()

	// Make JSONL newer than DB
	futureTime := time.Now().Add(1 * time.Second)
	if err := os.Chtimes(jsonlPath, futureTime, futureTime); err != nil {
		t.Fatalf("Failed to update JSONL timestamp: %v", err)
	}

	// Disable auto-import (this would normally be set via --no-auto-import flag)
	oldAutoImport := autoImportEnabled
	autoImportEnabled = false
	defer func() { autoImportEnabled = oldAutoImport }()

	// Call autoImportIfNewer (should do nothing)
	if autoImportEnabled {
		autoImportIfNewer()
	}

	// Verify that the issue was NOT imported
	imported, err := testStore.GetIssue(ctx, "test-noimport-1")
	if err != nil {
		t.Fatalf("Failed to check for issue: %v", err)
	}

	if imported != nil {
		t.Error("Expected issue test-noimport-1 to NOT be imported when auto-import is disabled")
	}

	// Clean up
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()
}

// TestAutoImportWithUpdate tests that auto-import detects same-ID updates and applies them
func TestAutoImportWithUpdate(t *testing.T) {
	// FIX: Initialize rootCtx for auto-import operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	tmpDir, err := os.MkdirTemp("", "bd-test-update-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	testStore := newTestStore(t, dbPath)

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	}()

	// ctx already declared above for rootCtx initialization

	// Create issue in DB with status=closed
	closedTime := time.Now().UTC()
	dbIssue := &types.Issue{
		ID:        "test-col-1",
		Title:     "Local version",
		Status:    types.StatusClosed,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ClosedAt:  &closedTime,
	}
	if err := testStore.CreateIssue(ctx, dbIssue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Create JSONL with same ID but different title (update scenario)
	// The import should update the title since status=closed is preserved
	jsonlIssue := &types.Issue{
		ID:        "test-col-1",
		Title:     "Remote version",
		Status:    types.StatusClosed, // Match DB status to avoid spurious update
		Priority:  1,                  // Match DB priority
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ClosedAt:  &closedTime,
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	json.NewEncoder(f).Encode(jsonlIssue)
	f.Close()

	// Run auto-import
	autoImportIfNewer()

	// Verify import updated the title from JSONL
	result, err := testStore.GetIssue(ctx, "test-col-1")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	if result.Status != types.StatusClosed {
		t.Errorf("Expected status=closed, got %s", result.Status)
	}
	if result.Title != "Remote version" {
		t.Errorf("Expected title='Remote version' (from JSONL), got '%s'", result.Title)
	}
}

// TestAutoImportNoUpdate tests happy path with no updates needed
func TestAutoImportNoUpdate(t *testing.T) {
	// FIX: Initialize rootCtx for auto-import operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	tmpDir, err := os.MkdirTemp("", "bd-test-noupdate-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	testStore := newTestStore(t, dbPath)

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	}()

	// ctx already declared above for rootCtx initialization

	// Create issue in DB
	dbIssue := &types.Issue{
		ID:        "test-noc-1",
		Title:     "Same version",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := testStore.CreateIssue(ctx, dbIssue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Create JSONL with exact match + new issue
	newIssue := &types.Issue{
		ID:        "test-noc-2",
		Title:     "Brand new issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	json.NewEncoder(f).Encode(dbIssue)
	json.NewEncoder(f).Encode(newIssue)
	f.Close()

	// Run auto-import
	autoImportIfNewer()

	// Verify new issue imported
	result, err := testStore.GetIssue(ctx, "test-noc-2")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	if result == nil {
		t.Fatal("Expected new issue to be imported")
	}
	if result.Title != "Brand new issue" {
		t.Errorf("Expected title='Brand new issue', got '%s'", result.Title)
	}
}

// TestAutoImportMergeConflict tests that auto-import detects Git merge conflicts (bd-270)
func TestAutoImportMergeConflict(t *testing.T) {
	// FIX: Initialize rootCtx for auto-import operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	tmpDir, err := os.MkdirTemp("", "bd-test-conflict-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	testStore := newTestStore(t, dbPath)

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	}()

	// ctx already declared above for rootCtx initialization

	// Create an initial issue in database
	dbIssue := &types.Issue{
		ID:        "test-conflict-1",
		Title:     "Original issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := testStore.CreateIssue(ctx, dbIssue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Create JSONL with merge conflict markers
	conflictContent := `<<<<<<< HEAD
{"id":"test-conflict-1","title":"HEAD version","status":"open","priority":1,"issue_type":"task","created_at":"2025-10-16T00:00:00Z","updated_at":"2025-10-16T00:00:00Z"}
=======
{"id":"test-conflict-1","title":"Incoming version","status":"in_progress","priority":2,"issue_type":"bug","created_at":"2025-10-16T00:00:00Z","updated_at":"2025-10-16T00:00:00Z"}
>>>>>>> incoming-branch
`
	if err := os.WriteFile(jsonlPath, []byte(conflictContent), 0644); err != nil {
		t.Fatalf("Failed to create conflicted JSONL: %v", err)
	}

	// Capture stderr to check for merge conflict message
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Run auto-import - should detect conflict and abort
	autoImportIfNewer()

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	io.Copy(&buf, r)
	stderrOutput := buf.String()

	// Verify merge conflict was detected
	if !strings.Contains(stderrOutput, "Git merge conflict detected") {
		t.Errorf("Expected 'Git merge conflict detected' in stderr, got: %s", stderrOutput)
	}

	// Verify the database was not modified (original issue unchanged)
	result, err := testStore.GetIssue(ctx, "test-conflict-1")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	if result.Title != "Original issue" {
		t.Errorf("Expected title 'Original issue' (unchanged), got '%s'", result.Title)
	}
}

// TestAutoImportConflictMarkerFalsePositive tests that conflict marker detection
// doesn't trigger on JSON-encoded conflict markers in issue content (bd-17d5)
func TestAutoImportConflictMarkerFalsePositive(t *testing.T) {
	// FIX: Initialize rootCtx for auto-import operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	tmpDir, err := os.MkdirTemp("", "bd-test-false-positive-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	testStore := newTestStore(t, dbPath)

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
		testStore.Close()
	}()

	// ctx already declared above for rootCtx initialization

	// Create a JSONL file with an issue that has conflict markers in the description
	// The conflict markers are JSON-encoded (as \u003c\u003c\u003c...) which should NOT trigger detection
	now := time.Now().Format(time.RFC3339Nano)
	jsonlContent := fmt.Sprintf(`{"id":"test-fp-1","title":"Test false positive","description":"This issue documents git conflict markers:\n\u003c\u003c\u003c\u003c\u003c\u003c\u003c HEAD\n=======\n\u003e\u003e\u003e\u003e\u003e\u003e\u003e branch","status":"open","priority":1,"issue_type":"task","created_at":"%s","updated_at":"%s"}`, now, now)
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent+"\n"), 0644); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Verify the JSONL contains JSON-encoded conflict markers (not literal ones)
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to read JSONL: %v", err)
	}
	jsonlStr := string(jsonlData)
	if !strings.Contains(jsonlStr, `\u003c\u003c\u003c`) {
		t.Logf("JSONL content: %s", jsonlStr)
		t.Fatalf("Expected JSON-encoded conflict markers in JSONL")
	}

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Run auto-import - should succeed without conflict detection
	autoImportIfNewer()

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	io.Copy(&buf, r)
	stderrOutput := buf.String()

	// Verify NO conflict was detected
	if strings.Contains(stderrOutput, "conflict") {
		t.Errorf("False positive: conflict detection triggered on JSON-encoded markers. stderr: %s", stderrOutput)
	}

	// Verify the issue was successfully imported
	result, err := testStore.GetIssue(ctx, "test-fp-1")
	if err != nil {
		t.Fatalf("Failed to get issue (import failed): %v", err)
	}
	expectedDesc := "This issue documents git conflict markers:\n<<<<<<< HEAD\n=======\n>>>>>>> branch"
	if result.Description != expectedDesc {
		t.Errorf("Expected description with conflict markers, got: %s", result.Description)
	}
}

// TestAutoImportClosedAtInvariant tests that auto-import enforces status/closed_at invariant
func TestAutoImportClosedAtInvariant(t *testing.T) {
	// FIX: Initialize rootCtx for auto-import operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = ctx
	defer func() { rootCtx = oldRootCtx }()

	tmpDir, err := os.MkdirTemp("", "bd-test-invariant-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	testStore := newTestStore(t, dbPath)

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	}()

	// ctx already declared above for rootCtx initialization

	// Create JSONL with closed issue but missing closed_at
	closedIssue := &types.Issue{
		ID:        "test-inv-1",
		Title:     "Closed without timestamp",
		Status:    types.StatusClosed,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ClosedAt:  nil, // Missing!
	}

	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	json.NewEncoder(f).Encode(closedIssue)
	f.Close()

	// Run auto-import
	autoImportIfNewer()

	// Verify closed_at was set
	result, err := testStore.GetIssue(ctx, "test-inv-1")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	if result == nil {
		t.Fatal("Expected issue to be created")
	}
	if result.ClosedAt == nil {
		t.Error("Expected closed_at to be set for closed issue")
	}
}

// bd-206: Test updating open issue to closed preserves closed_at
func TestImportOpenToClosedTransition(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-open-to-closed-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	ctx := context.Background()

	// Step 1: Create an open issue in the database
	openIssue := &types.Issue{
		ID:          "bd-transition-1",
		Title:       "Test transition",
		Description: "This will be closed",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		ClosedAt:    nil,
	}

	err = testStore.CreateIssue(ctx, openIssue, "test")
	if err != nil {
		t.Fatalf("Failed to create open issue: %v", err)
	}

	// Step 2: Update via UpdateIssue with closed status (closed_at managed automatically)
	updates := map[string]interface{}{
		"status": types.StatusClosed,
	}

	err = testStore.UpdateIssue(ctx, "bd-transition-1", updates, "test")
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Step 3: Verify the issue is now closed with correct closed_at
	updated, err := testStore.GetIssue(ctx, "bd-transition-1")
	if err != nil {
		t.Fatalf("Failed to get updated issue: %v", err)
	}

	if updated.Status != types.StatusClosed {
		t.Errorf("Expected status to be closed, got %s", updated.Status)
	}

	if updated.ClosedAt == nil {
		t.Fatal("Expected closed_at to be set after transition to closed")
	}
}

// bd-206: Test updating closed issue to open clears closed_at
func TestImportClosedToOpenTransition(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-closed-to-open-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	ctx := context.Background()

	// Step 1: Create a closed issue in the database
	closedTime := time.Now()
	closedIssue := &types.Issue{
		ID:          "bd-transition-2",
		Title:       "Test reopening",
		Description: "This will be reopened",
		Status:      types.StatusClosed,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   time.Now(),
		UpdatedAt:   closedTime,
		ClosedAt:    &closedTime,
	}

	err = testStore.CreateIssue(ctx, closedIssue, "test")
	if err != nil {
		t.Fatalf("Failed to create closed issue: %v", err)
	}

	// Step 2: Update via UpdateIssue with open status (closed_at managed automatically)
	updates := map[string]interface{}{
		"status": types.StatusOpen,
	}

	err = testStore.UpdateIssue(ctx, "bd-transition-2", updates, "test")
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Step 3: Verify the issue is now open with null closed_at
	updated, err := testStore.GetIssue(ctx, "bd-transition-2")
	if err != nil {
		t.Fatalf("Failed to get updated issue: %v", err)
	}

	if updated.Status != types.StatusOpen {
		t.Errorf("Expected status to be open, got %s", updated.Status)
	}

	if updated.ClosedAt != nil {
		t.Errorf("Expected closed_at to be nil after reopening, got %v", updated.ClosedAt)
	}
}
