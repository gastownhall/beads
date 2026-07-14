package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestConcurrentAccessNoLockError guards the SQLite locking posture (single-conn
// pool + WAL + busy_timeout in the DSN): overlapping reads and writes from several
// goroutines sharing one *Store must complete without ever surfacing a raw
// "database is locked" (SQLITE_BUSY). Before the pool was pinned to one connection
// and the DSN gained busy_timeout/WAL, `_txlock=immediate` took a RESERVED lock on
// every transaction (reads included) and overlapping operations collided with an
// immediate SQLITE_BUSY.
func TestConcurrentAccessNoLockError(t *testing.T) {
	ctx := context.Background()
	st, err := Provision(ctx, filepath.Join(t.TempDir(), "lock.db"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	const workers, perWorker = 8, 15
	var wg sync.WaitGroup
	errCh := make(chan error, workers*perWorker*2)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				issue := withDefaults(&types.Issue{ID: fmt.Sprintf("test-%d-%d", w, i), Title: "concurrent"})
				if err := st.CreateIssue(ctx, issue, "actor"); err != nil {
					errCh <- fmt.Errorf("worker %d create %d: %w", w, i, err)
				}
				// A read overlapping the other workers' writes: under the old posture
				// this could also collide with an immediate SQLITE_BUSY.
				if _, err := st.GetReadyWork(ctx, types.WorkFilter{}); err != nil {
					errCh <- fmt.Errorf("worker %d ready %d: %w", w, i, err)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if isLockError(err) {
			t.Errorf("concurrent access surfaced a lock error: %v", err)
		} else {
			t.Errorf("unexpected concurrent-access error: %v", err)
		}
	}
}

// TestReadConcurrentWithHeldWriteLock guards WAL reader/writer independence
// across separate store handles — the cross-process analog: two bd processes on
// one database file. Handle A holds SQLite's single write lock in an open
// transaction (the DSN's `_txlock=immediate` takes it at BEGIN); a read through
// handle B must still succeed, because under WAL readers never wait on the
// writer. Before withReadTx passed sql.TxOptions{ReadOnly: true}, B's read also
// issued BEGIN IMMEDIATE, queued on the write lock, and surfaced SQLITE_BUSY
// after busy_timeout — so during any long write (e.g. a large import) every
// concurrent read failed, forfeiting WAL's reader concurrency entirely.
func TestReadConcurrentWithHeldWriteLock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal-read.db")
	stA, err := Provision(ctx, path)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Cleanup(func() { _ = stA.Close() })
	if err := stA.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	// Second, independent handle (own pool/connection) on the same file.
	stB, err := New(ctx, Config{DSN: dsn(path)})
	if err != nil {
		t.Fatalf("New (second handle): %v", err)
	}
	t.Cleanup(func() { _ = stB.Close() })

	// Hold the write lock on A: with _txlock=immediate, BeginTx issues BEGIN
	// IMMEDIATE. The uncommitted INSERT makes it a real in-flight write.
	writeTx, err := stA.(*Store).DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin write tx on handle A: %v", err)
	}
	defer func() { _ = writeTx.Rollback() }()
	if _, err := writeTx.ExecContext(ctx,
		"INSERT INTO metadata (key, value) VALUES ('lock_probe', 'held')"); err != nil {
		t.Fatalf("write inside held tx: %v", err)
	}

	// The read on B must complete while A's write transaction is still open.
	got, err := stB.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("read during held write lock: %v", err)
	}
	if got != "test" {
		t.Fatalf("read during held write lock: got %q, want %q", got, "test")
	}
}

// withDefaults mirrors the conformance helper (Status/IssueType defaulting) so this
// package-internal test can build a minimally-valid issue without the harness.
func withDefaults(i *types.Issue) *types.Issue {
	if i.Status == "" {
		i.Status = types.StatusOpen
	}
	if i.IssueType == "" {
		i.IssueType = types.TypeTask
	}
	return i
}

func isLockError(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "database is locked") || strings.Contains(s, "sqlite_busy")
}
