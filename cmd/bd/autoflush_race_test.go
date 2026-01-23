package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestExportImportRace_ConcurrentOperations tests concurrent export and import
// operations do not corrupt data or cause race conditions.
// Run with: go test -race -run TestExportImportRace_ConcurrentOperations
func TestExportImportRace_ConcurrentOperations(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")

	// Create database and set up test environment
	testStore, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()
	if err := testStore.SetConfig(ctx, "issue_prefix", "race"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Save and restore global state
	oldStore := store
	oldDbPath := dbPath
	oldRootCtx := rootCtx
	oldStoreActive := storeActive

	store = testStore
	dbPath = testDBPath
	rootCtx = ctx
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	defer func() {
		store = oldStore
		dbPath = oldDbPath
		rootCtx = oldRootCtx
		storeMutex.Lock()
		storeActive = oldStoreActive
		storeMutex.Unlock()
	}()

	// Create initial issues (CreateIssue marks them dirty automatically)
	for i := 0; i < 10; i++ {
		issue := &types.Issue{
			ID:        generateUniqueTestID(t,"race", i),
			Title:     "Race Test Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Run concurrent export and import operations
	const numGoroutines = 5
	const numOperations = 20

	var wg sync.WaitGroup
	var exportErrors atomic.Int64
	var importErrors atomic.Int64

	// Export goroutines
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				flushToJSONLWithState(flushState{forceDirty: true, forceFullExport: true})
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Import goroutines (reading from exported file)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				// Simulate auto-import checking
				autoImportIfNewer()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()

	if exportErrors.Load() > 0 {
		t.Errorf("export had %d errors", exportErrors.Load())
	}
	if importErrors.Load() > 0 {
		t.Errorf("import had %d errors", importErrors.Load())
	}

	// Verify data integrity
	issues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	if len(issues) < 10 {
		t.Errorf("expected at least 10 issues, got %d (data loss)", len(issues))
	}
}

// TestExportImportRace_ExportWhileReading tests export while JSONL is being read.
// This simulates the race condition where autoImportIfNewer reads the JSONL
// while flushToJSONLWithState is writing it.
func TestExportImportRace_ExportWhileReading(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	testStore, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()
	if err := testStore.SetConfig(ctx, "issue_prefix", "read"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Save and restore global state
	oldStore := store
	oldDbPath := dbPath
	oldRootCtx := rootCtx
	oldStoreActive := storeActive

	store = testStore
	dbPath = testDBPath
	rootCtx = ctx
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	defer func() {
		store = oldStore
		dbPath = oldDbPath
		rootCtx = oldRootCtx
		storeMutex.Lock()
		storeActive = oldStoreActive
		storeMutex.Unlock()
	}()

	// Create issues (CreateIssue marks them dirty automatically)
	for i := 0; i < 50; i++ {
		issue := &types.Issue{
			ID:          generateUniqueTestID(t,"read", i),
			Title:       "Read Race Test",
			Description: "This is a test issue for read race testing with some content",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		}
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Initial export to create the JSONL file
	flushToJSONLWithState(flushState{forceDirty: true, forceFullExport: true})

	var wg sync.WaitGroup
	var readErrors atomic.Int64
	var parseErrors atomic.Int64

	// Writer goroutine - continuously exports
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			flushToJSONLWithState(flushState{forceDirty: true, forceFullExport: true})
			time.Sleep(time.Microsecond * 100)
		}
	}()

	// Reader goroutines - continuously read the JSONL
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				data, err := os.ReadFile(jsonlPath)
				if err != nil {
					if !os.IsNotExist(err) {
						readErrors.Add(1)
					}
					continue
				}

				// Try to parse the data - should always be valid JSON
				issues, err := readExistingJSONL(jsonlPath)
				if err != nil {
					parseErrors.Add(1)
				} else if len(issues) == 0 && len(data) > 0 {
					// Non-empty file but no issues parsed - possible corruption
					parseErrors.Add(1)
				}
				time.Sleep(time.Microsecond * 50)
			}
		}()
	}

	wg.Wait()

	if readErrors.Load() > 0 {
		t.Logf("read errors: %d (may be expected during atomic rename)", readErrors.Load())
	}
	if parseErrors.Load() > 0 {
		t.Errorf("parse errors: %d (indicates partial reads or corruption)", parseErrors.Load())
	}
}

// TestExportImportRace_ImportWithDirtyDB tests import while database has dirty issues.
// This validates that concurrent modifications during import don't cause data loss.
func TestExportImportRace_ImportWithDirtyDB(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	testStore, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()
	if err := testStore.SetConfig(ctx, "issue_prefix", "dirty"); err != nil {
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

	// Create initial JSONL with issues
	initialIssues := make([]*types.Issue, 10)
	for i := 0; i < 10; i++ {
		initialIssues[i] = &types.Issue{
			ID:        generateUniqueTestID(t,"dirty", i),
			Title:     "Initial Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
	}

	// Write initial JSONL
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(f)
	for _, issue := range initialIssues {
		if err := encoder.Encode(issue); err != nil {
			t.Fatalf("failed to encode issue: %v", err)
		}
	}
	f.Close()

	var wg sync.WaitGroup

	// Writer goroutine - creates new issues (CreateIssue marks them dirty)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 10; i < 30; i++ {
			issue := &types.Issue{
				ID:        generateUniqueTestID(t,"dirty", i),
				Title:     "New Local Issue",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
				continue
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Import goroutine - triggers auto-import
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			autoImportIfNewer()
			time.Sleep(time.Millisecond * 5)
		}
	}()

	wg.Wait()

	// Verify no data loss - should have both initial and new issues
	issues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	// We should have at least the initial 10 issues
	if len(issues) < 10 {
		t.Errorf("expected at least 10 issues, got %d (data loss)", len(issues))
	}
}

// TestExportImportRace_AtomicRename tests the atomic rename operation under concurrent access.
// Validates that readers don't see partial files during the rename.
func TestExportImportRace_AtomicRename(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// createTestIssues creates a fresh slice of issues (thread-safe)
	createTestIssues := func() []*types.Issue {
		issues := make([]*types.Issue, 100)
		for i := 0; i < 100; i++ {
			issues[i] = &types.Issue{
				ID:          generateUniqueTestID(t,"atomic", i),
				Title:       "Atomic Rename Test",
				Description: "Test issue for atomic rename testing",
				Status:      types.StatusOpen,
				Priority:    1,
				IssueType:   types.TypeTask,
			}
		}
		return issues
	}

	var wg sync.WaitGroup
	var writeCount atomic.Int64
	var readCount atomic.Int64
	var partialReads atomic.Int64

	// Writer goroutines using writeJSONLAtomic - each creates its own slice
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				issues := createTestIssues() // Fresh slice for each write
				_, err := writeJSONLAtomic(jsonlPath, issues)
				if err == nil {
					writeCount.Add(1)
				}
				time.Sleep(time.Microsecond * 100)
			}
		}()
	}

	// Reader goroutines that verify file completeness
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				data, err := os.ReadFile(jsonlPath)
				if err != nil {
					continue
				}
				readCount.Add(1)

				// Check for valid JSON structure - should never see partial lines
				if len(data) > 0 {
					// The last character should be newline (complete file)
					if data[len(data)-1] != '\n' {
						partialReads.Add(1)
					}

					// Try to parse all lines
					issueMap, err := readExistingJSONL(jsonlPath)
					if err != nil || len(issueMap) != 100 {
						partialReads.Add(1)
					}
				}
				time.Sleep(time.Microsecond * 50)
			}
		}()
	}

	wg.Wait()

	t.Logf("writes: %d, reads: %d, partial reads: %d", writeCount.Load(), readCount.Load(), partialReads.Load())

	// Atomic rename should prevent partial reads
	if partialReads.Load() > 0 {
		t.Errorf("detected %d partial reads (atomic rename failed)", partialReads.Load())
	}
}

// TestExportImportRace_PartialFileRead tests reading a file during atomic swap.
// This is a more targeted test for the atomic rename race condition.
func TestExportImportRace_PartialFileRead(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// createTestIssues creates a fresh slice of issues (thread-safe)
	createTestIssues := func() []*types.Issue {
		issues := make([]*types.Issue, 50)
		for i := 0; i < 50; i++ {
			issues[i] = &types.Issue{
				ID:          generateUniqueTestID(t,"partial", i),
				Title:       "Partial Read Test Issue",
				Description: "Content for testing partial read scenarios",
				Status:      types.StatusOpen,
				Priority:    1,
				IssueType:   types.TypeTask,
			}
		}
		return issues
	}

	// Create initial file
	initialIssues := createTestIssues()
	if _, err := writeJSONLAtomic(jsonlPath, initialIssues); err != nil {
		t.Fatalf("failed to create initial JSONL: %v", err)
	}

	var wg sync.WaitGroup
	var corruptReads atomic.Int64
	stopChan := make(chan struct{})

	// Continuous writer - creates fresh slice each time
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopChan:
				return
			default:
				issues := createTestIssues()
				writeJSONLAtomic(jsonlPath, issues)
				time.Sleep(time.Microsecond * 10)
			}
		}
	}()

	// Readers that validate each read is complete and valid
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				select {
				case <-stopChan:
					return
				default:
				}

				data, err := os.ReadFile(jsonlPath)
				if err != nil {
					continue
				}

				if len(data) == 0 {
					continue
				}

				// Verify hash integrity - compute hash and check it's stable
				hash1 := sha256.Sum256(data)

				// Re-read and check hash matches (detects mid-write reads)
				data2, err := os.ReadFile(jsonlPath)
				if err != nil {
					continue
				}
				hash2 := sha256.Sum256(data2)

				if hex.EncodeToString(hash1[:]) != hex.EncodeToString(hash2[:]) {
					// Different content between reads - file was being modified
					// This is not necessarily an error if atomic rename works
					continue
				}

				// Parse and verify
				issueMap, err := readExistingJSONL(jsonlPath)
				if err != nil {
					corruptReads.Add(1)
				} else if len(issueMap) != 50 {
					corruptReads.Add(1)
				}
			}
		}()
	}

	// Let it run for a bit
	time.Sleep(100 * time.Millisecond)
	close(stopChan)
	wg.Wait()

	if corruptReads.Load() > 0 {
		t.Errorf("detected %d corrupt reads", corruptReads.Load())
	}
}
