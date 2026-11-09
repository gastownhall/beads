//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// engineCounter wraps OpenSQL as a store's engine opener and counts the
// engines it opens and the ones still open.
type engineCounter struct {
	opens  atomic.Int64
	closes atomic.Int64
}

func (c *engineCounter) live() int64 { return c.opens.Load() - c.closes.Load() }

func (c *engineCounter) opener() sqlOpener {
	return func(ctx context.Context, dir, database, branch string) (*sql.DB, func() error, error) {
		db, cleanup, err := OpenSQL(ctx, dir, database, branch)
		if err != nil {
			return nil, nil, err
		}
		c.opens.Add(1)
		return db, func() error {
			c.closes.Add(1)
			return cleanup()
		}, nil
	}
}

// newCountedEngineStore opens a writable store (not through the Open cache)
// and installs a counting opener once bootstrap is done, so the counts cover
// only the store's own operations, starting with setting its issue prefix.
func newCountedEngineStore(t *testing.T, database string) (*EmbeddedDoltStore, *engineCounter) {
	t.Helper()
	ctx := context.Background()
	store, err := newStore(ctx, filepath.Join(t.TempDir(), ".beads"), database, "main", openStrict)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	counter := &engineCounter{}
	store.engine.open = counter.opener()
	if err := store.SetConfig(ctx, "issue_prefix", "en"); err != nil {
		t.Fatal(err)
	}
	return store, counter
}

func engineTestIssue(id string) *types.Issue {
	return &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

// TestEmbeddedEngineReusedAcrossTransactions pins gastownhall/beads#5397: a
// store opens its embedded engine once and reuses it across operations,
// instead of opening and destroying a whole engine per operation.
func TestEmbeddedEngineReusedAcrossTransactions(t *testing.T) {
	ctx := context.Background()
	store, counter := newCountedEngineStore(t, "enginereuse")

	store.SetEventsJournalEnabled(true)
	if err := store.EventsJournalActivationError(); err != nil {
		t.Fatalf("journal activation: %v", err)
	}
	if err := store.RunInTransaction(ctx, "bd: create er-1", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, engineTestIssue("er-1"), "tester")
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.GetIssue(ctx, "er-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "er-1" || got.Title != "er-1" {
		t.Fatalf("GetIssue = %s %q, want er-1", got.ID, got.Title)
	}
	found, err := store.SearchIssues(ctx, "", types.IssueFilter{IDs: []string{"er-1"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 || found[0].ID != "er-1" {
		t.Fatalf("SearchIssues = %v, want [er-1]", issueIDs(found))
	}
	if n := counter.opens.Load(); n != 1 {
		t.Fatalf("setting the prefix, journal activation, create+commit, get and search opened %d engines, want 1", n)
	}

	// The --parent walk node by node (the #5397 reproduction's shape) runs on
	// the same engine, and agrees with the one-transaction SearchDescendants.
	if err := store.RunInTransaction(ctx, "bd: seed tree", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, engineTestIssue("er-root"), "tester"); err != nil {
			return err
		}
		for i := 1; i <= 24; i++ {
			id := fmt.Sprintf("er-n%02d", i)
			parent := "er-root"
			if i > 4 {
				parent = fmt.Sprintf("er-n%02d", (i-1)/4)
			}
			if err := tx.CreateIssue(ctx, engineTestIssue(id), "tester"); err != nil {
				return err
			}
			if err := tx.AddDependency(ctx, &types.Dependency{IssueID: id, DependsOnID: parent, Type: types.DepParentChild}, "tester"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed tree: %v", err)
	}
	walked, err := storage.WalkDescendants(ctx, "er-root", types.IssueFilter{}, func(ctx context.Context, f types.IssueFilter) ([]*types.Issue, error) {
		return store.SearchIssues(ctx, "", f)
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(walked) != 24 {
		t.Fatalf("walk found %d descendants, want 24: %v", len(walked), issueIDs(walked))
	}
	single, err := store.SearchDescendants(ctx, "er-root", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchDescendants: %v", err)
	}
	walkedIDs, singleIDs := issueIDs(walked), issueIDs(single)
	slices.Sort(walkedIDs)
	slices.Sort(singleIDs)
	if !slices.Equal(walkedIDs, singleIDs) {
		t.Fatalf("walk = %v, SearchDescendants = %v", walkedIDs, singleIDs)
	}
	if n := counter.opens.Load(); n != 1 {
		t.Fatalf("after a 25-node walk the store has opened %d engines, want 1", n)
	}
}

func issueIDs(issues []*types.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, iss := range issues {
		ids = append(ids, iss.ID)
	}
	return ids
}

// TestEmbeddedEngineErrorLifecycle pins when the reused engine survives an
// operation: a callback error whose rollback succeeded keeps it, while a
// panic rolls back, releases the engine and still propagates, and a canceled
// operation releases it; the next operation then opens a fresh one.
func TestEmbeddedEngineErrorLifecycle(t *testing.T) {
	ctx := context.Background()
	store, counter := newCountedEngineStore(t, "enginelifecycle")
	if err := store.RunInTransaction(ctx, "bd: create el-1", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, engineTestIssue("el-1"), "tester")
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	errBoom := errors.New("boom")
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE issues SET title = 'rolled back' WHERE id = 'el-1'"); err != nil {
			return err
		}
		return errBoom
	}); !errors.Is(err, errBoom) {
		t.Fatalf("callback error = %v, want errBoom", err)
	}
	if got, err := store.GetIssue(ctx, "el-1"); err != nil || got.Title != "el-1" {
		t.Fatalf("after a rolled-back write: %v, %v", got, err)
	}
	if o, l := counter.opens.Load(), counter.live(); o != 1 || l != 1 {
		t.Fatalf("after a clean rollback: opens=%d live=%d, want the engine reused", o, l)
	}

	func() {
		defer func() {
			if r := recover(); r != "panic in callback" {
				t.Fatalf("recovered %v, want the callback's panic", r)
			}
		}()
		_ = store.withConn(ctx, true, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "UPDATE issues SET title = 'panicked' WHERE id = 'el-1'"); err != nil {
				return err
			}
			panic("panic in callback")
		})
		t.Fatal("withConn returned instead of propagating the panic")
	}()
	if counter.live() != 0 {
		t.Fatal("a panicking callback left the engine open")
	}
	if got, err := store.GetIssue(ctx, "el-1"); err != nil || got.Title != "el-1" {
		t.Fatalf("after a panicking write: %v, %v", got, err)
	}
	if o := counter.opens.Load(); o != 2 {
		t.Fatalf("opens=%d, want a fresh engine after the panic", o)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	if err := store.withConn(cancelCtx, false, func(tx *sql.Tx) error {
		cancel()
		return cancelCtx.Err()
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled operation = %v, want context.Canceled", err)
	}
	if counter.live() != 0 {
		t.Fatal("a canceled operation left the engine open")
	}
	if _, err := store.GetIssue(ctx, "el-1"); err != nil {
		t.Fatalf("after a canceled operation: %v", err)
	}
	if o := counter.opens.Load(); o != 3 {
		t.Fatalf("opens=%d, want a fresh engine after the cancellation", o)
	}
}

// TestEmbeddedEngineSerializesOperations pins that the engine slot, not
// separate engines, keeps this store's operations from overlapping
// (gastownhall/beads#6716): an operation holding a transaction, or pausing
// between statements of a withDBConn callback, keeps every other operation
// out until it finishes, and a waiter whose context is canceled gives up
// with its context error without running any SQL.
func TestEmbeddedEngineSerializesOperations(t *testing.T) {
	ctx := context.Background()
	store, counter := newCountedEngineStore(t, "engineserial")

	var active, overlaps atomic.Int32
	enter := func() {
		if active.Add(1) > 1 {
			overlaps.Add(1)
		}
	}
	leave := func() { active.Add(-1) }

	holders := map[string]func(entered, release chan struct{}) error{
		"transaction": func(entered, release chan struct{}) error {
			return store.withConn(ctx, true, func(tx *sql.Tx) error {
				enter()
				defer leave()
				close(entered)
				<-release
				_, err := tx.ExecContext(ctx, "SELECT 1")
				return err
			})
		},
		"withDBConn statements": func(entered, release chan struct{}) error {
			return store.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
				enter()
				defer leave()
				if _, err := db.ExecContext(ctx, "SELECT 1"); err != nil {
					return err
				}
				close(entered)
				<-release
				_, err := db.ExecContext(ctx, "SELECT 2")
				return err
			})
		},
	}
	for name, hold := range holders {
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			releaseHolder := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseHolder)
			holderDone := make(chan error, 1)
			go func() { holderDone <- hold(entered, release) }()
			<-entered

			// A waiter that gives up returns while the holder still holds,
			// and never reaches SQL.
			waitCtx, cancel := context.WithCancel(ctx)
			var canceledRan atomic.Bool
			canceledDone := make(chan error, 1)
			go func() {
				canceledDone <- store.withConn(waitCtx, false, func(tx *sql.Tx) error {
					canceledRan.Store(true)
					return nil
				})
			}()
			cancel()
			select {
			case err := <-canceledDone:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled waiter returned %v, want context.Canceled", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("a canceled waiter is still blocked behind the holder")
			}
			if canceledRan.Load() {
				t.Fatal("a canceled waiter ran its callback")
			}

			// A patient waiter does not enter until the holder finishes.
			var waiterEntered atomic.Bool
			waiterDone := make(chan error, 1)
			go func() {
				waiterDone <- store.withConn(ctx, false, func(tx *sql.Tx) error {
					enter()
					defer leave()
					waiterEntered.Store(true)
					var one int
					return tx.QueryRowContext(ctx, "SELECT 1").Scan(&one)
				})
			}()
			select {
			case err := <-waiterDone:
				t.Fatalf("waiter finished while the holder held the engine (err=%v)", err)
			case <-time.After(200 * time.Millisecond):
			}
			if waiterEntered.Load() {
				t.Fatal("waiter entered while the holder held the engine")
			}
			releaseHolder()
			if err := <-holderDone; err != nil {
				t.Fatalf("holder: %v", err)
			}
			if err := <-waiterDone; err != nil {
				t.Fatalf("waiter: %v", err)
			}
			if !waiterEntered.Load() {
				t.Fatal("waiter never ran")
			}
		})
	}
	if n := overlaps.Load(); n != 0 {
		t.Fatalf("%d operations overlapped", n)
	}
	if n := counter.opens.Load(); n != 1 {
		t.Fatalf("opened %d engines, want 1", n)
	}
}
