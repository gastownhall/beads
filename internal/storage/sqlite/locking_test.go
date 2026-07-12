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
