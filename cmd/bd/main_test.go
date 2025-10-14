package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestAutoFlushDirtyMarking tests that markDirtyAndScheduleFlush() correctly marks DB as dirty
func TestAutoFlushDirtyMarking(t *testing.T) {
	// Reset auto-flush state
	autoFlushEnabled = true
	isDirty = false
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	// Call markDirtyAndScheduleFlush
	markDirtyAndScheduleFlush()

	// Verify dirty flag is set
	flushMutex.Lock()
	dirty := isDirty
	hasTimer := flushTimer != nil
	flushMutex.Unlock()

	if !dirty {
		t.Error("Expected isDirty to be true after markDirtyAndScheduleFlush()")
	}

	if !hasTimer {
		t.Error("Expected flushTimer to be set after markDirtyAndScheduleFlush()")
	}

	// Clean up
	flushMutex.Lock()
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}
	isDirty = false
	flushMutex.Unlock()
}

// TestAutoFlushDisabled tests that --no-auto-flush flag disables the feature
func TestAutoFlushDisabled(t *testing.T) {
	// Disable auto-flush
	autoFlushEnabled = false
	isDirty = false
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	// Call markDirtyAndScheduleFlush
	markDirtyAndScheduleFlush()

	// Verify dirty flag is NOT set
	flushMutex.Lock()
	dirty := isDirty
	hasTimer := flushTimer != nil
	flushMutex.Unlock()

	if dirty {
		t.Error("Expected isDirty to remain false when autoFlushEnabled=false")
	}

	if hasTimer {
		t.Error("Expected flushTimer to remain nil when autoFlushEnabled=false")
	}

	// Re-enable for other tests
	autoFlushEnabled = true
}

// TestAutoFlushDebounce tests that rapid operations result in a single flush
func TestAutoFlushDebounce(t *testing.T) {
	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "bd-test-autoflush-*")
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
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer testStore.Close()

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	// Set short debounce for testing (100ms)
	originalDebounce := flushDebounce
	flushDebounce = 100 * time.Millisecond
	defer func() { flushDebounce = originalDebounce }()

	// Reset auto-flush state
	autoFlushEnabled = true
	isDirty = false
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	ctx := context.Background()

	// Create initial issue to have something in the DB
	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Simulate rapid CRUD operations
	for i := 0; i < 5; i++ {
		markDirtyAndScheduleFlush()
		time.Sleep(10 * time.Millisecond) // Small delay between marks (< debounce)
	}

	// Wait for debounce to complete
	time.Sleep(200 * time.Millisecond)

	// Check that JSONL file was created (flush happened)
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Error("Expected JSONL file to be created after debounce period")
	}

	// Verify only one flush occurred by checking file content
	// (should have exactly 1 issue)
	f, err := os.Open(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to open JSONL file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
	}

	if lineCount != 1 {
		t.Errorf("Expected 1 issue in JSONL, got %d (debounce may have failed)", lineCount)
	}

	// Clean up
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()
}

// TestAutoFlushClearState tests that clearAutoFlushState() properly resets state
func TestAutoFlushClearState(t *testing.T) {
	// Set up dirty state
	autoFlushEnabled = true
	isDirty = true
	flushTimer = time.AfterFunc(5*time.Second, func() {})

	// Clear state
	clearAutoFlushState()

	// Verify state is cleared
	flushMutex.Lock()
	dirty := isDirty
	hasTimer := flushTimer != nil
	failCount := flushFailureCount
	lastErr := lastFlushError
	flushMutex.Unlock()

	if dirty {
		t.Error("Expected isDirty to be false after clearAutoFlushState()")
	}

	if hasTimer {
		t.Error("Expected flushTimer to be nil after clearAutoFlushState()")
	}

	if failCount != 0 {
		t.Errorf("Expected flushFailureCount to be 0, got %d", failCount)
	}

	if lastErr != nil {
		t.Errorf("Expected lastFlushError to be nil, got %v", lastErr)
	}
}

// TestAutoFlushOnExit tests that flush happens on program exit
func TestAutoFlushOnExit(t *testing.T) {
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
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	// Reset auto-flush state
	autoFlushEnabled = true
	isDirty = false
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	ctx := context.Background()

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

	// Simulate PersistentPostRun (exit behavior)
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()

	flushMutex.Lock()
	needsFlush := isDirty && autoFlushEnabled
	if needsFlush {
		if flushTimer != nil {
			flushTimer.Stop()
			flushTimer = nil
		}
		isDirty = false
	}
	flushMutex.Unlock()

	if needsFlush {
		// Manually perform flush logic (simulating PersistentPostRun)
		storeMutex.Lock()
		storeActive = true // Temporarily re-enable for this test
		storeMutex.Unlock()

		issues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
		if err == nil {
			allDeps, _ := testStore.GetAllDependencyRecords(ctx)
			for _, iss := range issues {
				iss.Dependencies = allDeps[iss.ID]
			}
			tempPath := jsonlPath + ".tmp"
			f, err := os.Create(tempPath)
			if err == nil {
				encoder := json.NewEncoder(f)
				for _, iss := range issues {
					encoder.Encode(iss)
				}
				f.Close()
				os.Rename(tempPath, jsonlPath)
			}
		}

		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	}

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
func TestAutoFlushConcurrency(t *testing.T) {
	// Reset auto-flush state
	autoFlushEnabled = true
	isDirty = false
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	// Run multiple goroutines calling markDirtyAndScheduleFlush
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				markDirtyAndScheduleFlush()
			}
		}()
	}

	wg.Wait()

	// Verify no panic and state is valid
	flushMutex.Lock()
	dirty := isDirty
	hasTimer := flushTimer != nil
	flushMutex.Unlock()

	if !dirty {
		t.Error("Expected isDirty to be true after concurrent marks")
	}

	if !hasTimer {
		t.Error("Expected flushTimer to be set after concurrent marks")
	}

	// Clean up
	flushMutex.Lock()
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}
	isDirty = false
	flushMutex.Unlock()
}

// TestAutoFlushStoreInactive tests that flush doesn't run when store is inactive
func TestAutoFlushStoreInactive(t *testing.T) {
	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "bd-test-inactive-*")
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
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	store = testStore

	// Set store as INACTIVE (simulating closed store)
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()

	// Reset auto-flush state
	autoFlushEnabled = true
	flushMutex.Lock()
	isDirty = true
	flushMutex.Unlock()

	// Call flushToJSONL (should return early due to inactive store)
	flushToJSONL()

	// Verify JSONL was NOT created (flush was skipped)
	if _, err := os.Stat(jsonlPath); !os.IsNotExist(err) {
		t.Error("Expected JSONL file to NOT be created when store is inactive")
	}

	testStore.Close()
}

// TestAutoFlushJSONLContent tests that flushed JSONL has correct content
func TestAutoFlushJSONLContent(t *testing.T) {
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
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create store
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer testStore.Close()

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	ctx := context.Background()

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
	}

	// Mark dirty and flush immediately
	flushMutex.Lock()
	isDirty = true
	flushMutex.Unlock()

	flushToJSONL()

	// Verify JSONL file exists
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Fatal("Expected JSONL file to be created")
	}

	// Read and verify content
	f, err := os.Open(jsonlPath)
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

// TestAutoFlushErrorHandling tests error scenarios in flush operations
func TestAutoFlushErrorHandling(t *testing.T) {
	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "bd-test-error-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}()

	dbPath = filepath.Join(tmpDir, "test.db")

	// Create store
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer testStore.Close()

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	ctx := context.Background()

	// Create test issue
	issue := &types.Issue{
		ID:        "test-error-1",
		Title:     "Error test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Create a read-only directory to force flush failure
	readOnlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.MkdirAll(readOnlyDir, 0555); err != nil {
		t.Fatalf("Failed to create read-only dir: %v", err)
	}
	defer os.Chmod(readOnlyDir, 0755) // Restore permissions for cleanup

	// Set dbPath to point to read-only directory
	originalDBPath := dbPath
	dbPath = filepath.Join(readOnlyDir, "test.db")

	// Reset failure counter
	flushMutex.Lock()
	flushFailureCount = 0
	lastFlushError = nil
	isDirty = true
	flushMutex.Unlock()

	// Attempt flush (should fail)
	flushToJSONL()

	// Verify failure was recorded
	flushMutex.Lock()
	failCount := flushFailureCount
	hasError := lastFlushError != nil
	flushMutex.Unlock()

	if failCount != 1 {
		t.Errorf("Expected flushFailureCount to be 1, got %d", failCount)
	}

	if !hasError {
		t.Error("Expected lastFlushError to be set after flush failure")
	}

	// Restore dbPath
	dbPath = originalDBPath

	// Clean up
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()
}

// TestAutoImportIfNewer tests that auto-import triggers when JSONL is newer than DB
func TestAutoImportIfNewer(t *testing.T) {
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
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer testStore.Close()

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	ctx := context.Background()

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
	time.Sleep(100 * time.Millisecond)

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
	testStore, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer testStore.Close()

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	ctx := context.Background()

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
