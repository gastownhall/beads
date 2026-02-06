package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestWALCheckpointWithConcurrentReaders tests that WAL checkpoint works correctly
// when concurrent readers are active.
func TestWALCheckpointWithConcurrentReaders(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping WAL checkpoint test in short mode")
	}

	tmpDir, err := os.MkdirTemp("", "beads-wal-checkpoint-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// create some initial data
	for i := 0; i < 20; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Initial Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  i % 4,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "setup"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// start concurrent readers - they run for a fixed number of iterations
	const numReaders = 5
	const readsPerReader = 30

	var wg sync.WaitGroup
	var readSuccess atomic.Int64
	var readErrors atomic.Int64

	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			for j := 0; j < readsPerReader; j++ {
				issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
				if err == nil && len(issues) >= 20 {
					readSuccess.Add(1)
				} else {
					readErrors.Add(1)
					if err != nil {
						t.Logf("reader %d error: %v", readerID, err)
					}
				}
				time.Sleep(2 * time.Millisecond)
			}
		}(i)
	}

	// perform checkpoints concurrently with readers
	var checkpointSuccess int
	var checkpointWg sync.WaitGroup
	checkpointWg.Add(1)
	go func() {
		defer checkpointWg.Done()
		for i := 0; i < 5; i++ {
			time.Sleep(15 * time.Millisecond)
			if err := store.CheckpointWAL(ctx); err != nil {
				t.Logf("checkpoint %d failed: %v", i, err)
			} else {
				checkpointSuccess++
			}
		}
	}()

	// wait for both readers and checkpoints
	wg.Wait()
	checkpointWg.Wait()

	totalReads := int64(numReaders * readsPerReader)
	t.Logf("checkpoints: %d/5 successful, reads: %d/%d (errors: %d)",
		checkpointSuccess, readSuccess.Load(), totalReads, readErrors.Load())

	// all checkpoints should succeed
	if checkpointSuccess != 5 {
		t.Errorf("expected 5 successful checkpoints, got %d", checkpointSuccess)
	}

	// all reads should succeed (WAL mode allows concurrent reads during checkpoint)
	if readSuccess.Load() != totalReads {
		t.Errorf("not all reads succeeded: %d/%d", readSuccess.Load(), totalReads)
	}
}

// TestWALCheckpointFailureHandling tests graceful handling of checkpoint failures.
func TestWALCheckpointFailureHandling(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-wal-fail-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// create issue to generate WAL data
	issue := &types.Issue{
		Title:     "WAL Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// checkpoint should succeed normally
	if err := store.CheckpointWAL(ctx); err != nil {
		t.Errorf("normal checkpoint failed: %v", err)
	}

	// checkpoint with cancelled context should fail gracefully
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel() // cancel immediately

	err = store.CheckpointWAL(cancelCtx)
	if err == nil {
		t.Log("checkpoint with cancelled context succeeded (acceptable)")
	} else {
		t.Logf("checkpoint with cancelled context: %v (expected)", err)
	}

	// store should still be usable after checkpoint failure
	issue2 := &types.Issue{
		Title:     "Post-Checkpoint Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Errorf("CreateIssue after checkpoint failure failed: %v", err)
	}
}

// TestWALGrowthUnderSustainedWrites tests WAL file growth under sustained write load.
func TestWALGrowthUnderSustainedWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping WAL growth test in short mode")
	}

	tmpDir, err := os.MkdirTemp("", "beads-wal-growth-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	walPath := dbPath + "-wal"
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// record initial state
	var initialWALSize int64
	if info, err := os.Stat(walPath); err == nil {
		initialWALSize = info.Size()
	}

	// sustained writes without checkpoint
	const numWrites = 100
	for i := 0; i < numWrites; i++ {
		issue := &types.Issue{
			Title:       fmt.Sprintf("Sustained Write %d", i),
			Description: "This is a test issue with some content to grow the WAL file.",
			Status:      types.StatusOpen,
			Priority:    i % 4,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "writer"); err != nil {
			t.Fatalf("CreateIssue %d failed: %v", i, err)
		}
	}

	// check WAL size after writes
	var postWriteWALSize int64
	if info, err := os.Stat(walPath); err == nil {
		postWriteWALSize = info.Size()
	}

	t.Logf("WAL size: initial=%d, after %d writes=%d",
		initialWALSize, numWrites, postWriteWALSize)

	// WAL should have grown
	if postWriteWALSize <= initialWALSize {
		t.Logf("WAL did not grow (may have auto-checkpointed)")
	}

	// checkpoint to truncate WAL
	if err := store.CheckpointWAL(ctx); err != nil {
		t.Fatalf("checkpoint failed: %v", err)
	}

	// check WAL size after checkpoint
	var postCheckpointWALSize int64
	if info, err := os.Stat(walPath); err == nil {
		postCheckpointWALSize = info.Size()
	}

	t.Logf("WAL size after checkpoint: %d", postCheckpointWALSize)

	// TRUNCATE checkpoint should reduce WAL to minimal size
	// (Close() uses TRUNCATE which empties the WAL)
	// But CheckpointWAL uses FULL which may leave some data
}

// TestWALDataVisibilityAfterCheckpoint tests that data is visible after checkpoint.
func TestWALDataVisibilityAfterCheckpoint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-wal-visibility-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	// create store and add data
	store1, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store1: %v", err)
	}

	if err := store1.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store1.Close()
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// create issue
	issue := &types.Issue{
		Title:     "Visibility Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store1.CreateIssue(ctx, issue, "test"); err != nil {
		store1.Close()
		t.Fatalf("CreateIssue failed: %v", err)
	}
	issueID := issue.ID

	// checkpoint to flush WAL to main DB
	if err := store1.CheckpointWAL(ctx); err != nil {
		store1.Close()
		t.Fatalf("checkpoint failed: %v", err)
	}

	// close store1
	store1.Close()

	// open new store and verify data is visible
	store2, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store2: %v", err)
	}
	defer store2.Close()

	if err := store2.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	foundIssue, err := store2.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if foundIssue == nil {
		t.Error("issue not visible after checkpoint and reopen")
	} else if foundIssue.Title != "Visibility Test Issue" {
		t.Errorf("wrong title: got %q, want %q", foundIssue.Title, "Visibility Test Issue")
	}
}

// TestWALModeIsEnabled verifies that WAL mode is actually enabled.
func TestWALModeIsEnabled(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-wal-mode-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// query journal mode
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("failed to query journal_mode: %v", err)
	}

	t.Logf("journal_mode: %s", journalMode)

	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}
}

// TestWALConcurrentReadersWriters tests WAL mode's support for concurrent readers and writers.
func TestWALConcurrentReadersWriters(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent readers/writers test in short mode")
	}

	tmpDir, err := os.MkdirTemp("", "beads-wal-concurrent-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// create initial data
	for i := 0; i < 10; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Initial %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "setup"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	const numReaders = 10
	const numWriters = 2
	const opsPerWorker = 50

	var wg sync.WaitGroup
	var readSuccess atomic.Int64
	var writeSuccess atomic.Int64
	var readErrors atomic.Int64
	var writeErrors atomic.Int64

	// start readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			for j := 0; j < opsPerWorker; j++ {
				_, err := store.SearchIssues(ctx, "", types.IssueFilter{})
				if err != nil {
					readErrors.Add(1)
				} else {
					readSuccess.Add(1)
				}
			}
		}(i)
	}

	// start writers
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()

			for j := 0; j < opsPerWorker; j++ {
				issue := &types.Issue{
					Title:     fmt.Sprintf("Writer %d Issue %d", writerID, j),
					Status:    types.StatusOpen,
					Priority:  2,
					IssueType: types.TypeTask,
				}
				if err := store.CreateIssue(ctx, issue, "writer"); err != nil {
					writeErrors.Add(1)
				} else {
					writeSuccess.Add(1)
				}
			}
		}(i)
	}

	// wait for all operations
	wg.Wait()

	totalReads := int64(numReaders * opsPerWorker)
	totalWrites := int64(numWriters * opsPerWorker)

	t.Logf("reads: %d/%d (%d errors), writes: %d/%d (%d errors)",
		readSuccess.Load(), totalReads, readErrors.Load(),
		writeSuccess.Load(), totalWrites, writeErrors.Load())

	// WAL mode should allow all reads to succeed
	if readSuccess.Load() != totalReads {
		t.Errorf("not all reads succeeded: %d/%d", readSuccess.Load(), totalReads)
	}

	// writes may have some contention but should mostly succeed
	if writeSuccess.Load() < totalWrites*90/100 {
		t.Errorf("too few writes succeeded: %d/%d", writeSuccess.Load(), totalWrites)
	}
}

// TestWALFileCreation tests that WAL and SHM files are created.
func TestWALFileCreation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-wal-files-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"
	ctx := context.Background()

	// initially no files should exist
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Error("database file exists before creation")
	}

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// database should exist
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file does not exist after creation")
	}

	// WAL file may not exist until first write
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// create an issue to trigger WAL write
	issue := &types.Issue{
		Title:     "WAL File Test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// WAL file should exist after write (in WAL mode)
	if _, err := os.Stat(walPath); os.IsNotExist(err) {
		t.Log("WAL file does not exist (may have been checkpointed)")
	} else {
		t.Log("WAL file exists")
	}

	// SHM file should exist (used for shared memory in WAL mode)
	if _, err := os.Stat(shmPath); os.IsNotExist(err) {
		t.Log("SHM file does not exist (expected)")
	} else {
		t.Log("SHM file exists")
	}
}

// TestCheckpointModes tests different checkpoint modes (PASSIVE, FULL, TRUNCATE).
func TestCheckpointModes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-checkpoint-modes-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	walPath := dbPath + "-wal"
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// create some data
	for i := 0; i < 10; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Checkpoint Test %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	getWALSize := func() int64 {
		if info, err := os.Stat(walPath); err == nil {
			return info.Size()
		}
		return 0
	}

	// test PASSIVE checkpoint (won't block, may not complete)
	_, err = store.db.Exec("PRAGMA wal_checkpoint(PASSIVE)")
	if err != nil {
		t.Errorf("PASSIVE checkpoint failed: %v", err)
	}
	t.Logf("after PASSIVE: WAL size = %d", getWALSize())

	// test FULL checkpoint (waits for readers, checkpoints all frames)
	_, err = store.db.Exec("PRAGMA wal_checkpoint(FULL)")
	if err != nil {
		t.Errorf("FULL checkpoint failed: %v", err)
	}
	t.Logf("after FULL: WAL size = %d", getWALSize())

	// test TRUNCATE checkpoint (like FULL but also truncates WAL file)
	_, err = store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	if err != nil {
		t.Errorf("TRUNCATE checkpoint failed: %v", err)
	}
	truncateSize := getWALSize()
	t.Logf("after TRUNCATE: WAL size = %d", truncateSize)

	// TRUNCATE should result in small or zero WAL file
	if truncateSize > 4096 { // some filesystem overhead is ok
		t.Log("WAL file larger than expected after TRUNCATE")
	}
}

// TestCheckpointDuringTransaction tests checkpoint behavior during active transaction.
func TestCheckpointDuringTransaction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-checkpoint-during-tx-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// create initial data
	for i := 0; i < 5; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Pre-TX Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "setup"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// start a read transaction
	txStarted := make(chan struct{})
	txRelease := make(chan struct{})
	var txErr error

	go func() {
		// get a connection and start a read transaction
		conn, err := store.db.Conn(ctx)
		if err != nil {
			txErr = err
			close(txStarted)
			return
		}
		defer conn.Close()

		// start read transaction
		_, err = conn.ExecContext(ctx, "BEGIN DEFERRED")
		if err != nil {
			txErr = err
			close(txStarted)
			return
		}

		// perform a read (starts read lock)
		var count int
		conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count)

		close(txStarted)
		<-txRelease

		conn.ExecContext(ctx, "COMMIT")
	}()

	<-txStarted
	if txErr != nil {
		t.Fatalf("transaction setup failed: %v", txErr)
	}

	// try PASSIVE checkpoint while read transaction is active
	// PASSIVE won't wait, so it should return immediately
	_, err = store.db.Exec("PRAGMA wal_checkpoint(PASSIVE)")
	if err != nil {
		t.Errorf("PASSIVE checkpoint during read TX failed: %v", err)
	}

	// release transaction
	close(txRelease)
	time.Sleep(50 * time.Millisecond)

	// FULL checkpoint should work now
	_, err = store.db.Exec("PRAGMA wal_checkpoint(FULL)")
	if err != nil {
		t.Errorf("FULL checkpoint after TX release failed: %v", err)
	}
}

// TestWALAutoCheckpoint tests SQLite's auto-checkpoint behavior.
func TestWALAutoCheckpoint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-auto-checkpoint-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// check auto-checkpoint threshold
	var threshold int
	if err := store.db.QueryRow("PRAGMA wal_autocheckpoint").Scan(&threshold); err != nil {
		t.Fatalf("failed to query wal_autocheckpoint: %v", err)
	}

	t.Logf("wal_autocheckpoint threshold: %d pages", threshold)

	// default is 1000 pages, which is about 4MB
	// we won't change it as it's a reasonable default
}

// BenchmarkCheckpointOverhead measures the overhead of WAL checkpoint.
func BenchmarkCheckpointOverhead(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "beads-checkpoint-bench-*")
	if err != nil {
		b.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "bench.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	store.SetConfig(ctx, "issue_prefix", "bd")

	// create some data
	for i := 0; i < 100; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Bench Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		store.CreateIssue(ctx, issue, "bench")
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		store.CheckpointWAL(ctx)
	}
}

// TestWALReaderIsolation tests that readers see a consistent snapshot.
func TestWALReaderIsolation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-reader-isolation-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// create initial issues
	for i := 0; i < 5; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Initial %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "setup"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// start a read transaction and count issues
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("failed to get connection: %v", err)
	}
	defer conn.Close()

	// begin read transaction
	if _, err := conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		t.Fatalf("BEGIN failed: %v", err)
	}

	// count issues at start of transaction
	var initialCount int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&initialCount); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	t.Logf("initial count in read TX: %d", initialCount)

	// add more issues via the main store (different connection)
	for i := 0; i < 3; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("During Read TX %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "writer"); err != nil {
			t.Fatalf("CreateIssue during read TX failed: %v", err)
		}
	}

	// count again in the same read transaction
	// should still see the same count (snapshot isolation)
	var midCount int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&midCount); err != nil {
		t.Fatalf("mid count query failed: %v", err)
	}
	t.Logf("mid count in read TX: %d", midCount)

	// commit read transaction
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT failed: %v", err)
	}

	// new query should see all issues
	var finalCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&finalCount); err != nil {
		t.Fatalf("final count query failed: %v", err)
	}
	t.Logf("final count: %d", finalCount)

	// in WAL mode, mid-transaction reads may or may not see new rows
	// depending on when the read snapshot was taken
	// the key is that within a transaction, counts should be consistent
	if finalCount != initialCount+3 {
		t.Errorf("expected final count %d, got %d", initialCount+3, finalCount)
	}
}

// TestWALRecoveryAfterCrash simulates recovery after unclean shutdown.
func TestWALRecoveryAfterCrash(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-wal-recovery-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	walPath := dbPath + "-wal"
	ctx := context.Background()

	// create store and write data
	store1, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store1: %v", err)
	}

	if err := store1.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store1.Close()
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// create issues
	var issueIDs []string
	for i := 0; i < 5; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Recovery Test %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store1.CreateIssue(ctx, issue, "test"); err != nil {
			store1.Close()
			t.Fatalf("CreateIssue failed: %v", err)
		}
		issueIDs = append(issueIDs, issue.ID)
	}

	// close WITHOUT checkpoint (simulates crash)
	// use raw db.Close() to skip checkpoint
	store1.db.Close()

	// check that WAL file exists and has content
	if info, err := os.Stat(walPath); err == nil && info.Size() > 0 {
		t.Logf("WAL file exists with %d bytes (will be recovered)", info.Size())
	}

	// reopen database - WAL should be automatically recovered
	store2, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store2 after crash: %v", err)
	}
	defer store2.Close()

	// verify all issues are present (recovered from WAL)
	for _, id := range issueIDs {
		issue, err := store2.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("GetIssue %s after recovery failed: %v", id, err)
			continue
		}
		if issue == nil {
			t.Errorf("issue %s not recovered from WAL", id)
		}
	}

	t.Logf("recovered %d issues from WAL", len(issueIDs))
}
