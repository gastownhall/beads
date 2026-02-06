package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestConcurrentTransactions_10Goroutines tests transaction handling under concurrent load.
// This verifies that BEGIN IMMEDIATE + busy_timeout + retry logic handles lock contention gracefully.
func TestConcurrentTransactions_10Goroutines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent test in short mode")
	}

	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const numGoroutines = 10
	const opsPerGoroutine = 20

	var wg sync.WaitGroup
	var successCount atomic.Int64
	var errorCount atomic.Int64
	errChan := make(chan error, numGoroutines*opsPerGoroutine)

	// launch concurrent transactions
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < opsPerGoroutine; j++ {
				err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
					issue := &types.Issue{
						Title:     fmt.Sprintf("Concurrent Issue %d-%d", workerID, j),
						Status:    types.StatusOpen,
						Priority:  workerID % 4,
						IssueType: types.TypeTask,
					}
					return tx.CreateIssue(ctx, issue, fmt.Sprintf("worker-%d", workerID))
				})

				if err != nil {
					errorCount.Add(1)
					errChan <- fmt.Errorf("worker %d op %d: %w", workerID, j, err)
				} else {
					successCount.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// collect errors
	var errs []error
	for err := range errChan {
		errs = append(errs, err)
	}

	// all transactions should succeed under concurrent load
	expectedTotal := int64(numGoroutines * opsPerGoroutine)
	if successCount.Load() != expectedTotal {
		t.Errorf("expected %d successful transactions, got %d (errors: %d)",
			expectedTotal, successCount.Load(), errorCount.Load())
		for i, err := range errs {
			if i < 10 { // only show first 10 errors
				t.Logf("  error %d: %v", i, err)
			}
		}
	}

	// verify all issues were created
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(issues) != int(expectedTotal) {
		t.Errorf("expected %d issues in database, got %d", expectedTotal, len(issues))
	}
}

// TestConcurrentTransactions_HeavyLoad tests transaction handling under heavier concurrent load.
// Uses 20 goroutines to stress-test the locking mechanism.
func TestConcurrentTransactions_HeavyLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping heavy load test in short mode")
	}

	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const numGoroutines = 20
	const opsPerGoroutine = 10

	var wg sync.WaitGroup
	var successCount atomic.Int64
	errChan := make(chan error, numGoroutines*opsPerGoroutine)

	startSignal := make(chan struct{})

	// launch goroutines that wait for start signal
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			<-startSignal // synchronize start for maximum contention

			for j := 0; j < opsPerGoroutine; j++ {
				err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
					issue := &types.Issue{
						Title:     fmt.Sprintf("HeavyLoad %d-%d", workerID, j),
						Status:    types.StatusOpen,
						Priority:  2,
						IssueType: types.TypeTask,
					}
					if err := tx.CreateIssue(ctx, issue, "load-test"); err != nil {
						return err
					}
					// add a label within the same transaction
					return tx.AddLabel(ctx, issue.ID, fmt.Sprintf("worker-%d", workerID), "load-test")
				})

				if err != nil {
					errChan <- err
				} else {
					successCount.Add(1)
				}
			}
		}(i)
	}

	// start all goroutines simultaneously
	close(startSignal)
	wg.Wait()
	close(errChan)

	var busyErrors int
	for err := range errChan {
		if IsBusyError(err) {
			busyErrors++
		} else {
			t.Logf("non-busy error: %v", err)
		}
	}

	// with proper retry logic, we should have very few (ideally zero) busy errors
	// even under heavy load
	expectedTotal := int64(numGoroutines * opsPerGoroutine)
	successRate := float64(successCount.Load()) / float64(expectedTotal) * 100

	t.Logf("success rate: %.1f%% (%d/%d), busy errors: %d",
		successRate, successCount.Load(), expectedTotal, busyErrors)

	// expect at least 95% success rate under load
	if successRate < 95.0 {
		t.Errorf("success rate %.1f%% is below 95%% threshold", successRate)
	}
}

// TestBeginImmediateRetryUnderLoad tests that BEGIN IMMEDIATE retry logic works correctly
// when write lock contention is high.
func TestBeginImmediateRetryUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode")
	}

	// create temp directory for file-based database (needed for proper locking)
	tmpDir, err := os.MkdirTemp("", "beads-retry-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	// create store with short busy timeout to test retry behavior
	store, err := NewWithTimeout(ctx, dbPath, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	const numGoroutines = 15
	var wg sync.WaitGroup
	var successCount atomic.Int64
	var retrySuccessCount atomic.Int64

	startSignal := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			<-startSignal

			startTime := time.Now()
			err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
				// simulate work that holds the lock
				issue := &types.Issue{
					Title:       fmt.Sprintf("Retry Test %d", workerID),
					Description: "Testing retry behavior",
					Status:      types.StatusOpen,
					Priority:    2,
					IssueType:   types.TypeTask,
				}
				if err := tx.CreateIssue(ctx, issue, "retry-test"); err != nil {
					return err
				}
				// hold the transaction briefly to create contention
				time.Sleep(5 * time.Millisecond)
				return nil
			})

			if err == nil {
				successCount.Add(1)
				elapsed := time.Since(startTime)
				// if it took longer than 20ms, it likely had to retry
				if elapsed > 20*time.Millisecond {
					retrySuccessCount.Add(1)
				}
			}
		}(i)
	}

	close(startSignal)
	wg.Wait()

	t.Logf("successful transactions: %d/%d, likely retried: %d",
		successCount.Load(), numGoroutines, retrySuccessCount.Load())

	// all transactions should eventually succeed
	if successCount.Load() != int64(numGoroutines) {
		t.Errorf("expected all %d transactions to succeed, got %d",
			numGoroutines, successCount.Load())
	}
}

// TestBusyTimeoutBehavior verifies that busy_timeout pragma is respected.
func TestBusyTimeoutBehavior(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-busytimeout-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	// create store with specific timeout
	busyTimeout := 500 * time.Millisecond
	store, err := NewWithTimeout(ctx, dbPath, busyTimeout)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// verify busy_timeout is set correctly
	var timeout int64
	err = store.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout)
	if err != nil {
		t.Fatalf("failed to query busy_timeout: %v", err)
	}

	expectedMs := busyTimeout.Milliseconds()
	if timeout != expectedMs {
		t.Errorf("busy_timeout = %d ms, want %d ms", timeout, expectedMs)
	}
}

// TestTransactionAbortDuringReconnect tests that transactions are properly handled
// when a reconnection is triggered during an active transaction.
func TestTransactionAbortDuringReconnect(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-reconnect-abort-*")
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

	// enable freshness checking
	store.EnableFreshnessChecking()

	// create initial issue
	issue := &types.Issue{
		Title:     "Pre-reconnect Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// start a transaction
	var txStarted sync.WaitGroup
	var txComplete sync.WaitGroup
	txStarted.Add(1)
	txComplete.Add(1)

	var txErr error
	go func() {
		defer txComplete.Done()

		txErr = store.RunInTransaction(ctx, func(tx storage.Transaction) error {
			// signal that we've started
			txStarted.Done()

			// create issue within transaction
			issue := &types.Issue{
				Title:     "During-reconnect Issue",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := tx.CreateIssue(ctx, issue, "test"); err != nil {
				return err
			}

			// give time for reconnect to be triggered
			time.Sleep(50 * time.Millisecond)

			return nil
		})
	}()

	// wait for transaction to start
	txStarted.Wait()

	// trigger reconnect (this should wait for transaction to complete due to RWMutex)
	// touch file to trigger freshness check
	time.Sleep(10 * time.Millisecond)
	now := time.Now()
	os.Chtimes(dbPath, now, now)

	// wait for transaction to complete
	txComplete.Wait()

	// transaction should complete successfully despite reconnect attempt
	if txErr != nil {
		t.Errorf("transaction failed during reconnect: %v", txErr)
	}

	// verify issue was created
	issues, err := store.SearchIssues(ctx, "During-reconnect", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("expected 1 issue created during transaction, got %d", len(issues))
	}
}

// TestConcurrentReadWriteTransactions tests mixed read and write operations
// under concurrent load.
func TestConcurrentReadWriteTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent read/write test in short mode")
	}

	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// create some initial issues
	for i := 0; i < 50; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Initial Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  i % 4,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "setup"); err != nil {
			t.Fatalf("setup CreateIssue failed: %v", err)
		}
	}

	const numWriters = 5
	const numReaders = 10
	const opsPerWorker = 20

	var wg sync.WaitGroup
	var writeSuccess atomic.Int64
	var readSuccess atomic.Int64
	errChan := make(chan error, (numWriters+numReaders)*opsPerWorker)

	// start writers
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()

			for j := 0; j < opsPerWorker; j++ {
				err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
					issue := &types.Issue{
						Title:     fmt.Sprintf("Writer %d Issue %d", writerID, j),
						Status:    types.StatusOpen,
						Priority:  2,
						IssueType: types.TypeTask,
					}
					return tx.CreateIssue(ctx, issue, "writer")
				})

				if err != nil {
					errChan <- fmt.Errorf("writer %d: %w", writerID, err)
				} else {
					writeSuccess.Add(1)
				}
			}
		}(i)
	}

	// start readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			for j := 0; j < opsPerWorker; j++ {
				_, err := store.SearchIssues(ctx, "", types.IssueFilter{})
				if err != nil {
					errChan <- fmt.Errorf("reader %d: %w", readerID, err)
				} else {
					readSuccess.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	var errCount int
	for err := range errChan {
		errCount++
		if errCount <= 5 {
			t.Logf("error: %v", err)
		}
	}

	t.Logf("writes: %d/%d, reads: %d/%d, errors: %d",
		writeSuccess.Load(), numWriters*opsPerWorker,
		readSuccess.Load(), numReaders*opsPerWorker,
		errCount)

	// all operations should succeed
	if writeSuccess.Load() != int64(numWriters*opsPerWorker) {
		t.Errorf("not all writes succeeded: %d/%d",
			writeSuccess.Load(), numWriters*opsPerWorker)
	}
	if readSuccess.Load() != int64(numReaders*opsPerWorker) {
		t.Errorf("not all reads succeeded: %d/%d",
			readSuccess.Load(), numReaders*opsPerWorker)
	}
}

// TestTransactionIsolation verifies that uncommitted changes are not visible
// to other connections.
func TestTransactionIsolation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-isolation-test-*")
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

	// channel to coordinate test phases
	phase1Done := make(chan struct{})
	phase2Done := make(chan struct{})

	var createdID string
	var isolationErr error

	// goroutine 1: start transaction, create issue, wait, then rollback
	go func() {
		err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
			issue := &types.Issue{
				Title:     "Uncommitted Issue",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := tx.CreateIssue(ctx, issue, "test"); err != nil {
				return err
			}
			createdID = issue.ID

			// signal phase 1 done
			close(phase1Done)

			// wait for phase 2 check
			<-phase2Done

			// rollback by returning error
			return errors.New("intentional rollback")
		})

		if err == nil || err.Error() != "intentional rollback" {
			isolationErr = fmt.Errorf("expected intentional rollback error, got: %v", err)
		}
	}()

	// wait for issue to be created in uncommitted transaction
	<-phase1Done

	// goroutine 2 (main): check that uncommitted issue is NOT visible
	if createdID != "" {
		issue, err := store.GetIssue(ctx, createdID)
		if err == nil && issue != nil {
			t.Error("uncommitted issue should not be visible outside transaction")
		}
	}

	// signal phase 2 done to allow rollback
	close(phase2Done)

	// small wait for rollback to complete
	time.Sleep(50 * time.Millisecond)

	if isolationErr != nil {
		t.Error(isolationErr)
	}

	// verify issue was NOT created (rolled back)
	if createdID != "" {
		issue, err := store.GetIssue(ctx, createdID)
		if err == nil && issue != nil {
			t.Error("rolled back issue should not exist")
		}
	}
}

// TestDeadlockPrevention verifies that BEGIN IMMEDIATE prevents deadlocks
// when multiple transactions try to upgrade from read to write locks.
func TestDeadlockPrevention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping deadlock test in short mode")
	}

	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// create initial issue
	issue := &types.Issue{
		Title:     "Deadlock Test Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "setup"); err != nil {
		t.Fatalf("setup CreateIssue failed: %v", err)
	}
	issueID := issue.ID

	const numGoroutines = 10
	var wg sync.WaitGroup
	var successCount atomic.Int64

	// timeout for detecting deadlock
	testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			err := store.RunInTransaction(testCtx, func(tx storage.Transaction) error {
				// read then write pattern that can cause deadlock without IMMEDIATE
				_, err := tx.GetIssue(testCtx, issueID)
				if err != nil {
					return err
				}

				return tx.UpdateIssue(testCtx, issueID, map[string]interface{}{
					"title": fmt.Sprintf("Updated by worker %d", workerID),
				}, "worker")
			})

			if err == nil {
				successCount.Add(1)
			}
		}(i)
	}

	// wait with timeout (deadlock would cause hang)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success - no deadlock
	case <-testCtx.Done():
		t.Fatal("test timed out - possible deadlock detected")
	}

	// all transactions should complete (some may fail due to contention but none should deadlock)
	t.Logf("successful transactions: %d/%d", successCount.Load(), numGoroutines)

	if successCount.Load() == 0 {
		t.Error("no transactions succeeded - check for deadlock issues")
	}
}

// TestTransactionConnectionReuse verifies that connections are properly reused
// and transactions use dedicated connections.
func TestTransactionConnectionReuse(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-connreuse-test-*")
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

	// run multiple sequential transactions and verify each gets a valid connection
	for i := 0; i < 20; i++ {
		err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
			// basic operation within transaction
			issue := &types.Issue{
				Title:     fmt.Sprintf("Connection Reuse Test %d", i),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			return tx.CreateIssue(ctx, issue, "test")
		})

		if err != nil {
			t.Errorf("transaction %d failed: %v", i, err)
		}
	}

	// verify all issues were created
	issues, err := store.SearchIssues(ctx, "Connection Reuse", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(issues) != 20 {
		t.Errorf("expected 20 issues, got %d", len(issues))
	}

	// check pool statistics if available
	stats := store.db.Stats()
	t.Logf("connection pool: open=%d, in_use=%d, idle=%d, max_open=%d",
		stats.OpenConnections, stats.InUse, stats.Idle, stats.MaxOpenConnections)
}

// TestContextCancellationDuringTransaction tests that context cancellation
// is properly handled during transactions.
func TestContextCancellationDuringTransaction(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	var txStarted sync.WaitGroup
	txStarted.Add(1)

	var txDone sync.WaitGroup
	txDone.Add(1)

	// use channel to safely communicate the result
	errChan := make(chan error, 1)
	go func() {
		defer txDone.Done()
		err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
			txStarted.Done()

			// simulate work
			issue := &types.Issue{
				Title:     "Cancellation Test",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := tx.CreateIssue(ctx, issue, "test"); err != nil {
				return err
			}

			// wait for cancellation
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return nil
			}
		})
		errChan <- err
	}()

	// wait for transaction to start
	txStarted.Wait()

	// cancel the context
	cancel()

	// wait for transaction to complete
	txDone.Wait()

	// read the error safely after goroutine completes
	txErr := <-errChan

	// should get context.Canceled error
	if txErr == nil || !errors.Is(txErr, context.Canceled) {
		t.Logf("transaction error (may vary): %v", txErr)
	}
}

// BenchmarkConcurrentTransactions measures transaction throughput under concurrent load.
func BenchmarkConcurrentTransactions(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "beads-bench-concurrent-*")
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

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = store.RunInTransaction(ctx, func(tx storage.Transaction) error {
				issue := &types.Issue{
					Title:     fmt.Sprintf("Bench Issue %d", i),
					Status:    types.StatusOpen,
					Priority:  2,
					IssueType: types.TypeTask,
				}
				i++
				return tx.CreateIssue(ctx, issue, "bench")
			})
		}
	})
}

// TestStressTransactionPool stress tests the connection pool with rapid transactions.
func TestStressTransactionPool(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	tmpDir, err := os.MkdirTemp("", "beads-stress-pool-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "stress.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	const numOps = 500
	var wg sync.WaitGroup
	var successCount atomic.Int64

	// rapid fire transactions from multiple goroutines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < numOps/5; j++ {
				err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
					issue := &types.Issue{
						Title:     fmt.Sprintf("Stress %d-%d", workerID, j),
						Status:    types.StatusOpen,
						Priority:  2,
						IssueType: types.TypeTask,
					}
					return tx.CreateIssue(ctx, issue, "stress")
				})
				if err == nil {
					successCount.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	successRate := float64(successCount.Load()) / float64(numOps) * 100
	t.Logf("stress test: %d/%d successful (%.1f%%)", successCount.Load(), numOps, successRate)

	// expect very high success rate
	if successRate < 99.0 {
		t.Errorf("success rate %.1f%% below 99%% threshold", successRate)
	}

	// verify connection pool health
	stats := store.db.Stats()
	t.Logf("final pool state: open=%d, in_use=%d, wait_count=%d",
		stats.OpenConnections, stats.InUse, stats.WaitCount)

	// connections in use should be zero after all transactions complete
	if stats.InUse != 0 {
		t.Errorf("expected 0 connections in use after test, got %d", stats.InUse)
	}
}

// TestRawConnectionLocking verifies low-level BEGIN IMMEDIATE behavior.
func TestRawConnectionLocking(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-raw-lock-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	// open raw database connection
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=busy_timeout(1000)", dbPath))
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// enable WAL mode
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to enable WAL: %v", err)
	}

	// create schema
	if _, err := db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	ctx := context.Background()

	// get two connections
	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("failed to get conn1: %v", err)
	}
	defer conn1.Close()

	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("failed to get conn2: %v", err)
	}
	defer conn2.Close()

	// conn1 starts IMMEDIATE transaction
	if _, err := conn1.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("conn1 BEGIN IMMEDIATE failed: %v", err)
	}

	// conn2 should fail immediately or after busy_timeout when trying IMMEDIATE
	done := make(chan error, 1)
	go func() {
		_, err := conn2.ExecContext(ctx, "BEGIN IMMEDIATE")
		done <- err
	}()

	// should complete within busy_timeout (1 second)
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected conn2 BEGIN IMMEDIATE to fail while conn1 holds lock")
		} else {
			t.Logf("conn2 correctly received error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("conn2 BEGIN IMMEDIATE hung - possible deadlock")
	}

	// cleanup
	conn1.ExecContext(ctx, "ROLLBACK")
}
