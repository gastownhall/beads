package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestJSONLLock_MissingLockCoordination tests scenarios where multiple processes
// attempt to access JSONL without proper lock coordination.
// Run with: go test -race -run TestJSONLLock_MissingLockCoordination
func TestJSONLLock_MissingLockCoordination(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	lockPath := filepath.Join(beadsDir, ".sync.lock")

	// Create initial JSONL
	issues := make([]*types.Issue, 20)
	for i := 0; i < 20; i++ {
		issues[i] = &types.Issue{
			ID:        generateUniqueTestID(t, "lock", i),
			Title:     "Lock Test Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
	}

	if _, err := writeJSONLAtomic(jsonlPath, issues); err != nil {
		t.Fatalf("failed to create initial JSONL: %v", err)
	}

	var wg sync.WaitGroup
	var lockConflicts atomic.Int64
	var successfulWrites atomic.Int64

	// Simulate multiple processes trying to acquire the lock
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				lock := flock.New(lockPath)
				locked, err := lock.TryLock()
				if err != nil {
					continue
				}
				if !locked {
					lockConflicts.Add(1)
					continue
				}

				// Simulate write operation
				time.Sleep(time.Millisecond)
				issues[id%20].Title = "Updated by process"
				_, err = writeJSONLAtomic(jsonlPath, issues)
				if err == nil {
					successfulWrites.Add(1)
				}

				_ = lock.Unlock()
			}
		}(i)
	}

	wg.Wait()

	t.Logf("lock conflicts: %d, successful writes: %d", lockConflicts.Load(), successfulWrites.Load())

	// We expect some lock conflicts when multiple processes compete
	// But all successful writes should be serialized and no data corruption
	if successfulWrites.Load() == 0 {
		t.Error("no successful writes occurred")
	}

	// Verify final file integrity
	issueMap, err := readExistingJSONL(jsonlPath)
	if err != nil {
		t.Errorf("final JSONL is corrupt: %v", err)
	}
	if len(issueMap) != 20 {
		t.Errorf("expected 20 issues, got %d", len(issueMap))
	}
}

// TestJSONLLock_ExportLockAcquisitionFailure tests behavior when export
// cannot acquire the lock due to another process holding it.
func TestJSONLLock_ExportLockAcquisitionFailure(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	lockPath := filepath.Join(beadsDir, ".sync.lock")

	testStore, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()
	if err := testStore.SetConfig(ctx, "issue_prefix", "lockfail"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create some issues in the database
	for i := 0; i < 5; i++ {
		issue := &types.Issue{
			ID:        generateUniqueTestID(t, "lockfail", i),
			Title:     "Lock Failure Test",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Hold the lock to simulate another process
	holdLock := flock.New(lockPath)
	locked, err := holdLock.TryLock()
	if err != nil || !locked {
		t.Fatalf("failed to acquire hold lock: %v, locked=%v", err, locked)
	}
	defer holdLock.Unlock()

	// Now try to export while lock is held
	// The export should either wait or fail gracefully
	exportDone := make(chan bool, 1)

	go func() {
		// This simulates doExportOnlySync trying to acquire the lock
		syncLock := flock.New(lockPath)
		locked, err := syncLock.TryLock()
		if err != nil {
			exportDone <- false
			return
		}
		if !locked {
			// Expected - lock is held by another process
			exportDone <- false
			return
		}
		defer syncLock.Unlock()
		exportDone <- true
	}()

	select {
	case success := <-exportDone:
		if success {
			t.Error("export should have failed to acquire lock")
		}
		// Expected: export failed to acquire lock
	case <-time.After(time.Second):
		t.Error("export timed out waiting for lock")
	}

	// Release the hold lock
	holdLock.Unlock()

	// Now export should succeed
	exportLock := flock.New(lockPath)
	locked, err = exportLock.TryLock()
	if err != nil || !locked {
		t.Fatalf("failed to acquire lock after release: %v", err)
	}
	defer exportLock.Unlock()

	// Write the export
	issues, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if _, err := writeJSONLAtomic(jsonlPath, issues); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}

	// Verify the export worked
	issueMap, err := readExistingJSONL(jsonlPath)
	if err != nil {
		t.Errorf("JSONL is corrupt: %v", err)
	}
	if len(issueMap) != 5 {
		t.Errorf("expected 5 issues, got %d", len(issueMap))
	}
}

// TestJSONLLock_ImportWithExportInProgress tests import attempting to run
// while an export is in progress.
func TestJSONLLock_ImportWithExportInProgress(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	lockPath := filepath.Join(beadsDir, ".sync.lock")

	testStore, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()
	if err := testStore.SetConfig(ctx, "issue_prefix", "impexp"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Save and restore global state
	oldStore := store
	oldDbPath := dbPath
	oldRootCtx := rootCtx
	oldStoreActive := storeActive
	oldNoAutoImport := noAutoImport

	store = testStore
	dbPath = testDBPath
	rootCtx = ctx
	noAutoImport = false
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	defer func() {
		store = oldStore
		dbPath = oldDbPath
		rootCtx = oldRootCtx
		noAutoImport = oldNoAutoImport
		storeMutex.Lock()
		storeActive = oldStoreActive
		storeMutex.Unlock()
	}()

	// Create JSONL with issues
	issues := make([]*types.Issue, 10)
	for i := 0; i < 10; i++ {
		issues[i] = &types.Issue{
			ID:        generateUniqueTestID(t, "impexp", i),
			Title:     "Import Export Test",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
	}

	if _, err := writeJSONLAtomic(jsonlPath, issues); err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}

	// Import issues into DB first so export won't overwrite with empty content
	for _, issue := range issues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue in DB: %v", err)
		}
	}

	var wg sync.WaitGroup
	var importAttempts atomic.Int64
	var exportAttempts atomic.Int64

	// Export goroutine that holds lock for extended time
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			lock := flock.New(lockPath)
			locked, _ := lock.TryLock()
			if locked {
				exportAttempts.Add(1)
				// Simulate slow export
				time.Sleep(time.Millisecond * 10)
				flushToJSONLWithState(flushState{forceDirty: true})
				lock.Unlock()
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Import goroutine that competes for lock
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			// autoImportIfNewer doesn't use a lock currently
			// but should still handle concurrent access safely
			importAttempts.Add(1)
			autoImportIfNewer()
			time.Sleep(time.Millisecond * 2)
		}
	}()

	wg.Wait()

	t.Logf("export attempts: %d, import attempts: %d", exportAttempts.Load(), importAttempts.Load())

	// Verify database integrity
	dbIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	if len(dbIssues) < 10 {
		t.Errorf("expected at least 10 issues, got %d", len(dbIssues))
	}
}

// TestJSONLLock_TimeoutScenarios tests lock timeout behavior.
func TestJSONLLock_TimeoutScenarios(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	lockPath := filepath.Join(beadsDir, ".sync.lock")

	// Test 1: Short timeout when lock is held
	t.Run("short_timeout", func(t *testing.T) {
		holdLock := flock.New(lockPath)
		locked, _ := holdLock.TryLock()
		if !locked {
			t.Fatal("failed to acquire hold lock")
		}
		defer holdLock.Unlock()

		// Try to acquire with context timeout
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		tryLock := flock.New(lockPath)
		start := time.Now()

		// Use TryLock in a loop with timeout
		acquired := false
		for time.Since(start) < 50*time.Millisecond {
			locked, _ := tryLock.TryLock()
			if locked {
				acquired = true
				tryLock.Unlock()
				break
			}
			select {
			case <-ctx.Done():
				break
			default:
				time.Sleep(time.Millisecond)
			}
		}

		if acquired {
			t.Error("should not have acquired lock while held")
		}

		elapsed := time.Since(start)
		if elapsed < 40*time.Millisecond {
			t.Errorf("timeout too short: %v", elapsed)
		}
	})

	// Test 2: Lock released before timeout
	t.Run("released_before_timeout", func(t *testing.T) {
		holdLock := flock.New(lockPath)
		locked, _ := holdLock.TryLock()
		if !locked {
			t.Fatal("failed to acquire hold lock")
		}

		// Release lock after 20ms
		go func() {
			time.Sleep(20 * time.Millisecond)
			holdLock.Unlock()
		}()

		// Try to acquire with 100ms timeout
		tryLock := flock.New(lockPath)
		start := time.Now()

		acquired := false
		for time.Since(start) < 100*time.Millisecond {
			locked, _ := tryLock.TryLock()
			if locked {
				acquired = true
				tryLock.Unlock()
				break
			}
			time.Sleep(time.Millisecond)
		}

		elapsed := time.Since(start)

		if !acquired {
			t.Error("should have acquired lock after release")
		}
		if elapsed > 50*time.Millisecond {
			t.Errorf("took too long to acquire: %v", elapsed)
		}
	})

	// Test 3: Multiple waiters for lock
	t.Run("multiple_waiters", func(t *testing.T) {
		holdLock := flock.New(lockPath)
		locked, _ := holdLock.TryLock()
		if !locked {
			t.Fatal("failed to acquire hold lock")
		}

		var wg sync.WaitGroup
		var acquisitions atomic.Int64

		// Multiple goroutines waiting for lock
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				waiterLock := flock.New(lockPath)
				start := time.Now()
				for time.Since(start) < 200*time.Millisecond {
					locked, _ := waiterLock.TryLock()
					if locked {
						acquisitions.Add(1)
						time.Sleep(time.Millisecond)
						waiterLock.Unlock()
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()
		}

		// Release after 50ms
		time.Sleep(50 * time.Millisecond)
		holdLock.Unlock()

		wg.Wait()

		// All waiters should eventually acquire the lock
		if acquisitions.Load() != 5 {
			t.Errorf("expected 5 acquisitions, got %d", acquisitions.Load())
		}
	})
}

// TestJSONLLock_ConcurrentSyncOperations tests multiple sync operations
// competing for the same lock.
func TestJSONLLock_ConcurrentSyncOperations(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	lockPath := filepath.Join(beadsDir, ".sync.lock")

	testStore, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()
	if err := testStore.SetConfig(ctx, "issue_prefix", "csync"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create initial issues
	for i := 0; i < 10; i++ {
		issue := &types.Issue{
			ID:        generateUniqueTestID(t, "csync", i),
			Title:     "Concurrent Sync Test",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Initial export
	issues, _ := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if _, err := writeJSONLAtomic(jsonlPath, issues); err != nil {
		t.Fatalf("failed to create initial JSONL: %v", err)
	}

	var wg sync.WaitGroup
	var syncCompleted atomic.Int64
	var syncFailed atomic.Int64

	// Simulate multiple concurrent sync operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < 5; j++ {
				// Acquire lock
				lock := flock.New(lockPath)
				locked, err := lock.TryLock()
				if err != nil || !locked {
					syncFailed.Add(1)
					time.Sleep(time.Millisecond * 5)
					continue
				}

				// Simulate sync: read -> modify -> write
				issueMap, err := readExistingJSONL(jsonlPath)
				if err != nil {
					lock.Unlock()
					syncFailed.Add(1)
					continue
				}

				// Modify
				for _, issue := range issueMap {
					issue.Title = "Updated by worker"
				}

				// Write back
				issueSlice := make([]*types.Issue, 0, len(issueMap))
				for _, issue := range issueMap {
					issueSlice = append(issueSlice, issue)
				}
				_, err = writeJSONLAtomic(jsonlPath, issueSlice)
				if err != nil {
					lock.Unlock()
					syncFailed.Add(1)
					continue
				}

				syncCompleted.Add(1)
				lock.Unlock()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()

	t.Logf("sync completed: %d, sync failed: %d", syncCompleted.Load(), syncFailed.Load())

	// Verify data integrity
	finalIssues, err := readExistingJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("final JSONL is corrupt: %v", err)
	}
	if len(finalIssues) != 10 {
		t.Errorf("expected 10 issues, got %d", len(finalIssues))
	}
}
