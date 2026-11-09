//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// engineOpenCounter wraps the production opener, counting engine opens and
// engine disposals. Installed on the store's engine slot (the opener seam)
// before the store's first operation, it sees every open the slot makes.
type engineOpenCounter struct {
	inner  engineOpener
	opens  atomic.Int64
	closes atomic.Int64
}

func (c *engineOpenCounter) open(ctx context.Context, dir, database, branch string) (*sql.DB, func() error, error) {
	db, cleanup, err := c.inner(ctx, dir, database, branch)
	if err != nil {
		return nil, nil, err
	}
	c.opens.Add(1)
	return db, func() error {
		err := cleanup()
		c.closes.Add(1)
		return err
	}, nil
}

// engineTestStore opens a store on a fresh database with the counting opener
// and linger (an idle interval long enough to suppress idle disposal)
// installed. Seams go in before the first operation, the design's requirement,
// and the store's bootstrap open (initSchema's one-shot raw OpenSQL) stays
// outside the count: only opens the engine slot makes are counted.
func engineTestStore(t *testing.T, database string, linger time.Duration) (*EmbeddedDoltStore, *engineOpenCounter) {
	t.Helper()
	skipUnlessEmbeddedDolt(t)
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), ".beads"), database, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.engine.opener != nil || store.engine.db != nil {
		t.Fatalf("engine slot already in use before test seams installed")
	}
	counter := &engineOpenCounter{inner: OpenSQL}
	store.engine.opener = counter.open
	store.engine.idleAfter = linger
	return store, counter
}

// engineOpenCountersEqual waits until want opens and closes have been counted,
// or fails after timeout with the current counts. Idle disposal and Close run
// on their own goroutine timing, so the exact moment a disposal lands is not
// observable synchronously.
func engineWaitForCounters(t *testing.T, counter *engineOpenCounter, wantOpens, wantCloses int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if counter.opens.Load() == wantOpens && counter.closes.Load() == wantCloses {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine opens/closes = %d/%d after %v, want %d/%d",
				counter.opens.Load(), counter.closes.Load(), timeout, wantOpens, wantCloses)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestEmbeddedEngineReusedAcrossTransactions pins AC-1 of gastownhall/beads#5397:
// one cached engine serves every operation a store runs across its lifetime —
// config writes, the journal-shape probe, journaled and unjournaled
// transactions, reads, searches and the dependency-tree walk — instead of the
// per-operation cold-start open the base paid (measured ~110ms each). The tree
// walk is the expensive case on the base: WalkDescendants issues one search per
// node, 25 nodes here, so the base opened the engine 25+ times for the walk
// alone.
func TestEmbeddedEngineReusedAcrossTransactions(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := context.Background()
	store, counter := engineTestStore(t, "engine_reuse", time.Hour)

	if err := store.SetConfig(ctx, "issue_prefix", "er"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	// Journal activation probes the journal table's shape through the engine
	// slot (probeJournalShape); journaled transactions then dual-write.
	store.SetEventsJournalEnabled(true)

	const children = 24
	root := &types.Issue{ID: "er-root", Title: "er-root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, root, "tester"); err != nil {
		t.Fatalf("CreateIssue root: %v", err)
	}
	seen := make(map[string]bool)
	for i := range children {
		id := fmt.Sprintf("er-child-%02d", i)
		seen[id] = true
		child := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, child, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: id, DependsOnID: "er-root", Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("AddDependency %s: %v", id, err)
		}
	}

	// Reads over the same cached engine.
	got, err := store.GetIssue(ctx, "er-root")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "er-root" {
		t.Fatalf("GetIssue().Title = %q, want %q", got.Title, "er-root")
	}
	hits, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(hits) != children+1 {
		t.Fatalf("SearchIssues found %d issues, want %d", len(hits), children+1)
	}

	// The node-by-node tree walk issues one search per node; the
	// single-transaction capability walk issues one query. Both must find the
	// whole seeded tree, and neither may reopen the engine.
	walked, err := storage.WalkDescendants(ctx, "er-root", types.IssueFilter{}, func(ctx context.Context, f types.IssueFilter) ([]*types.Issue, error) {
		return store.SearchIssues(ctx, "", f)
	})
	if err != nil {
		t.Fatalf("WalkDescendants: %v", err)
	}
	if len(walked) != children {
		t.Fatalf("WalkDescendants found %d issues, want %d", len(walked), children)
	}
	treed, err := storage.SearchDescendants(ctx, store, "er-root", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchDescendants: %v", err)
	}
	if len(treed) != children {
		t.Fatalf("SearchDescendants found %d issues, want %d", len(treed), children)
	}
	for _, iss := range treed {
		if !seen[iss.ID] {
			t.Fatalf("SearchDescendants returned unexpected issue %s", iss.ID)
		}
	}

	if opens := counter.opens.Load(); opens != 1 {
		t.Fatalf("engine opened %d times across the store's operations, want 1", opens)
	}
	if closes := counter.closes.Load(); closes != 0 {
		t.Fatalf("engine disposed %d times while the store is open, want 0", closes)
	}
}

// TestEmbeddedEngineSerializesOperations pins AC-2 of gastownhall/beads#5397:
// the cached engine is served one operation at a time. The base's
// engine-open exclusion already serialized operations; the slot must not
// regress that when the engine is now shared. Two contracts:
//
//   - an operation paused mid-body (a transaction between statements, or a
//     withDBConn between two stored procedures) keeps the slot for its whole
//     callback, so a queued operation cannot touch the engine in the meantime;
//   - a waiter whose context is canceled while queued returns its context
//     error without opening the engine or running any SQL.
//
// The not-entered bounds are one-sided: on the cached engine a queued read
// finishes in single-digit milliseconds, so 50ms without completion can only
// mean it never entered.
func TestEmbeddedEngineSerializesOperations(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := context.Background()
	store, counter := engineTestStore(t, "engine_serialize", time.Hour)
	if err := store.SetConfig(ctx, "issue_prefix", "es"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.CreateIssue(ctx, &types.Issue{ID: "es-1", Title: "es-1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if opens := counter.opens.Load(); opens != 1 {
		t.Fatalf("engine opens after seed = %d, want 1", opens)
	}

	// Phase 1: a transaction paused between statements holds the slot across
	// the pause; the queued search cannot enter.
	paused := make(chan struct{})
	resume := make(chan struct{})
	txResult := make(chan error, 1)
	go func() {
		txResult <- store.withConn(ctx, false, func(tx *sql.Tx) error {
			var one int
			if err := tx.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
				return err
			}
			paused <- struct{}{}
			<-resume
			return tx.QueryRowContext(ctx, "SELECT 1").Scan(&one)
		})
	}()
	<-paused
	searchResult := make(chan error, 1)
	go func() {
		issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err == nil && len(issues) != 1 {
			err = fmt.Errorf("found %d issues, want 1", len(issues))
		}
		searchResult <- err
	}()
	select {
	case err := <-searchResult:
		t.Fatalf("queued search finished while a transaction held the engine: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(resume)
	if err := <-txResult; err != nil {
		t.Fatalf("paused transaction: %v", err)
	}
	if err := <-searchResult; err != nil {
		t.Fatalf("queued search after release: %v", err)
	}

	// Phase 2: a withDBConn paused between statements holds the slot the same
	// way — the whole-callback hold is what a multi-statement sequence needs.
	dbPaused := make(chan struct{})
	dbResume := make(chan struct{})
	dbResult := make(chan error, 1)
	go func() {
		dbResult <- store.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
			var one int
			if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
				return err
			}
			dbPaused <- struct{}{}
			<-dbResume
			return db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
		})
	}()
	<-dbPaused
	blocked := make(chan error, 1)
	go func() {
		issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err == nil && len(issues) != 1 {
			err = fmt.Errorf("found %d issues, want 1", len(issues))
		}
		blocked <- err
	}()
	select {
	case err := <-blocked:
		t.Fatalf("queued search finished while a withDBConn held the engine: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	// While the slot is still held: a waiter canceled in the queue returns
	// its context error without opening the engine (opens stays 1).
	cancelCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := store.GetIssue(cancelCtx, "es-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled waiter error = %v, want context.DeadlineExceeded", err)
	}
	if opens := counter.opens.Load(); opens != 1 {
		t.Fatalf("engine opens after canceled waiter = %d, want 1", opens)
	}
	close(dbResume)
	if err := <-dbResult; err != nil {
		t.Fatalf("paused withDBConn: %v", err)
	}
	if err := <-blocked; err != nil {
		t.Fatalf("queued search after release: %v", err)
	}

	if opens := counter.opens.Load(); opens != 1 {
		t.Fatalf("engine opens after serial phases = %d, want 1", opens)
	}
	if closes := counter.closes.Load(); closes != 0 {
		t.Fatalf("engine disposed %d times while the store is open, want 0", closes)
	}
}

// TestEmbeddedEngineBlockedRecheckOnReusedEngine pins AC-3 of
// gastownhall/beads#5397: the post-commit blocked recheck runs on the store's
// cached engine. withConn releases the slot when the write's transaction ends,
// the recheck opens its own transaction and reacquires the slot, and that
// second acquisition reuses the engine instead of paying a cold start. The
// recompute settling (still blocked after one blocker closes, unblocked after
// both) is the observable proof the recheck ran at all.
func TestEmbeddedEngineBlockedRecheckOnReusedEngine(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := context.Background()
	store, counter := engineTestStore(t, "engine_recheck", time.Hour)
	if err := store.SetConfig(ctx, "issue_prefix", "ek"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	for _, id := range []string{"ek-a", "ek-b", "ek-c"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, blocker := range []string{"ek-a", "ek-b"} {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "ek-c", DependsOnID: blocker, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("add dependency ek-c -> %s: %v", blocker, err)
		}
	}
	if !embeddedIsBlocked(t, ctx, store, "ek-c") {
		t.Fatal("ek-c not blocked before the unblocking writes")
	}
	opensBefore := counter.opens.Load()

	// Close one blocker: the write commits, then withConn's post-commit
	// recheck recomputes ek-c on a post-commit snapshot. ek-b is still open,
	// so ek-c stays blocked — the recheck ran and left it blocked.
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := issueops.CloseIssueInTx(ctx, tx, "ek-a", "done", "tester", "")
		return err
	}); err != nil {
		t.Fatalf("close ek-a: %v", err)
	}
	if !embeddedIsBlocked(t, ctx, store, "ek-c") {
		t.Fatal("ek-c unblocked in-transaction while ek-b was still open")
	}

	// Close the second blocker: this recheck recomputes ek-c as unblocked.
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := issueops.CloseIssueInTx(ctx, tx, "ek-b", "done", "tester", "")
		return err
	}); err != nil {
		t.Fatalf("close ek-b: %v", err)
	}
	if embeddedIsBlocked(t, ctx, store, "ek-c") {
		t.Fatal("ek-c still blocked after both blockers closed: the recheck did not settle")
	}

	// Two writes, two post-commit rechecks and three verification reads all
	// ran on the one engine the seed opened: no reopen for the recheck.
	if opens := counter.opens.Load(); opens != opensBefore {
		t.Fatalf("engine opens across writes + rechecks = %d, want %d (the recheck added no open)", opens, opensBefore)
	}
}
