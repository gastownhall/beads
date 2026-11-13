package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// These tests pin that DoltStore.RunInTransaction runs the durable plane and
// the dolt-ignored wisp plane (wisps, wisp_*, local_metadata, repo_mtimes) on
// one SQL session and in one SQL transaction, with the events journal off
// (the default) as well as on: a mixed callback commits or rolls back as a
// whole, and its durable side effects ride its own Dolt commit.

// forEachJournalMode runs body once with the events journal off and once with
// it on, each on a fresh store of its own.
func forEachJournalMode(t *testing.T, body func(t *testing.T, ctx context.Context, store *DoltStore)) {
	t.Helper()
	for _, mode := range []struct {
		name    string
		enabled bool
	}{{"journal=off", false}, {"journal=on", true}} {
		t.Run(mode.name, func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			t.Cleanup(cleanup)
			if mode.enabled {
				store.SetEventsJournalEnabled(true)
			}
			ctx, cancel := testContext(t)
			t.Cleanup(cancel)
			body(t, ctx, store)
		})
	}
}

// commitsWithMessage returns the Dolt commits on the current branch whose
// message is msg, newest first.
func commitsWithMessage(t *testing.T, ctx context.Context, store *DoltStore, msg string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, "SELECT commit_hash FROM dolt_log WHERE message = ?", msg)
	if err != nil {
		t.Fatalf("query dolt_log: %v", err)
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		hashes = append(hashes, h)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_log: %v", err)
	}
	return hashes
}

func isWispPlaneTable(table string) bool {
	return table == "wisps" || strings.HasPrefix(table, "wisp_")
}

// TestRunInTransactionWispCommitConflictIsDeterminateAndAtomic: when another
// client commits a conflicting change to a wisp row the callback also wrote,
// the whole transaction is refused before anything lands. The durable issue
// the same callback created must not exist, and the refusal must not be
// reported as indeterminate, since nothing was committed.
func TestRunInTransactionWispCommitConflictIsDeterminateAndAtomic(t *testing.T) {
	forEachJournalMode(t, func(t *testing.T, ctx context.Context, store *DoltStore) {
		const (
			wispID    = "test-conflict-wisp"
			durableID = "test-conflict-durable"
		)
		if err := store.CreateIssue(ctx, crossTierWispIssue(wispID, "original"), "tester"); err != nil {
			t.Fatalf("seed wisp: %v", err)
		}

		other, err := sql.Open("mysql", store.connStr)
		if err != nil {
			t.Fatalf("open other client: %v", err)
		}
		defer func() { _ = other.Close() }()

		err = store.RunInTransaction(ctx, "test: conflicting wisp write", func(tx storage.Transaction) error {
			if err := tx.CreateIssue(ctx, crossTierRegularIssue(durableID, "durable in tx"), "tester"); err != nil {
				return err
			}
			if err := tx.UpdateIssue(ctx, wispID, map[string]interface{}{"title": "mine"}, "tester"); err != nil {
				return err
			}
			// Return the sibling client's failure instead of t.Fatalf: FailNow
			// inside the callback skips RunInTransaction's rollback, and the
			// deferred close of the pinned connection then waits forever.
			if _, err := other.ExecContext(ctx, "UPDATE wisps SET title = ? WHERE id = ?", "theirs", wispID); err != nil {
				return fmt.Errorf("other client's wisp update: %w", err)
			}
			return nil
		})
		if err == nil {
			t.Fatal("RunInTransaction succeeded despite a conflicting committed wisp write")
		}
		if errors.Is(err, ErrCommitIndeterminate) {
			t.Fatalf("RunInTransaction error is ErrCommitIndeterminate, want a determinate refusal: %v", err)
		}
		if !isSerializationError(err) {
			t.Errorf("RunInTransaction error is not a serialization failure: %v", err)
		}

		if _, err := store.GetIssue(ctx, durableID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("GetIssue(%s) err = %v, want ErrNotFound (durable half must roll back with the wisp half)", durableID, err)
		}
		w, err := store.GetIssue(ctx, wispID)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", wispID, err)
		}
		if w.Title != "theirs" {
			t.Errorf("wisp title = %q, want the other client's %q", w.Title, "theirs")
		}
	})
}

// TestRunInTransactionWispCloseUnblockRidesOneDoltCommit: closing a durable
// issue's only (wisp) blocker recomputes the durable issue's is_blocked, and
// that recompute rides the callback's single Dolt commit together with the
// callback's other durable write.
func TestRunInTransactionWispCloseUnblockRidesOneDoltCommit(t *testing.T) {
	forEachJournalMode(t, func(t *testing.T, ctx context.Context, store *DoltStore) {
		const (
			dependerID = "test-unblock-depender"
			wispID     = "test-unblock-wisp"
			newID      = "test-unblock-new"
			msg        = "test: close wisp blocker with a durable create"
		)
		seedTransactionWispBlocker(t, ctx, store, dependerID, wispID)

		if err := store.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
			if err := tx.CreateIssue(ctx, crossTierRegularIssue(newID, "durable in tx"), "tester"); err != nil {
				return err
			}
			return tx.CloseIssue(ctx, wispID, "done", "tester", "session")
		}); err != nil {
			t.Fatalf("RunInTransaction: %v", err)
		}

		hashes := commitsWithMessage(t, ctx, store, msg)
		if len(hashes) != 1 {
			t.Fatalf("Dolt commits with message %q = %d (%v), want exactly 1", msg, len(hashes), hashes)
		}
		var n int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM issues AS OF '"+hashes[0]+"' WHERE id = ?", newID).Scan(&n); err != nil {
			t.Fatalf("read %s as of the commit: %v", newID, err)
		}
		if n != 1 {
			t.Errorf("%s as of the callback's commit: %d rows, want 1", newID, n)
		}
		var blocked bool
		if err := store.db.QueryRowContext(ctx,
			"SELECT is_blocked FROM issues AS OF '"+hashes[0]+"' WHERE id = ?", dependerID).Scan(&blocked); err != nil {
			t.Fatalf("read %s as of the commit: %v", dependerID, err)
		}
		if blocked {
			t.Errorf("%s.is_blocked as of the callback's commit = true, want false (unblock must ride the same commit)", dependerID)
		}
		requireCleanTables(ctx, t, store, "issues", "dependencies")
	})
}

// TestRunInTransactionHoldsOneConnection: a callback that writes both planes
// holds exactly one pool connection, even when the pool has a spare warm one
// it could borrow.
func TestRunInTransactionHoldsOneConnection(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Warm the pool: keep two idle connections around so a second session
	// would be borrowed rather than dialed.
	store.db.SetMaxIdleConns(2)
	store.db.SetConnMaxIdleTime(0)
	var warm []*sql.Conn
	for i := 0; i < 2; i++ {
		c, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("warm conn %d: %v", i, err)
		}
		warm = append(warm, c)
	}
	for _, c := range warm {
		_ = c.Close()
	}
	if idle := store.db.Stats().Idle; idle < 2 {
		t.Fatalf("pool has %d idle connections after warming, want 2", idle)
	}

	before := store.db.Stats().InUse
	var inside int
	if err := store.RunInTransaction(ctx, "test: one connection", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, crossTierRegularIssue("test-onec-durable", "durable"), "tester"); err != nil {
			return err
		}
		if err := tx.CreateIssue(ctx, crossTierWispIssue("test-onec-wisp", "wisp"), "tester"); err != nil {
			return err
		}
		inside = store.db.Stats().InUse
		return nil
	}); err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	if delta := inside - before; delta != 1 {
		t.Errorf("connections in use inside the callback = %d more than before, want exactly 1 (no second session)", delta)
	}
}

// TestRunInTransactionKeepsWispsUnstaged: running both planes in one SQL
// transaction does not version wisps. The callback's Dolt commit carries its
// durable write and no wisp-plane table, and the wisp tables stay out of the
// staged set afterwards (they remain clone-local).
func TestRunInTransactionKeepsWispsUnstaged(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const msg = "test: wisp lifecycle beside a durable write"
	if err := store.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, crossTierRegularIssue("test-unstaged-durable", "durable"), "tester"); err != nil {
			return err
		}
		if err := tx.CreateIssue(ctx, crossTierWispIssue("test-unstaged-wisp", "wisp"), "tester"); err != nil {
			return err
		}
		if err := tx.UpdateIssue(ctx, "test-unstaged-wisp", map[string]interface{}{"title": "wisp renamed"}, "tester"); err != nil {
			return err
		}
		if err := tx.AddLabel(ctx, "test-unstaged-wisp", "scratch", "tester"); err != nil {
			return err
		}
		return tx.CloseIssue(ctx, "test-unstaged-wisp", "done", "tester", "session")
	}); err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}

	hashes := commitsWithMessage(t, ctx, store, msg)
	if len(hashes) != 1 {
		t.Fatalf("Dolt commits with message %q = %d (%v), want exactly 1", msg, len(hashes), hashes)
	}
	rows, err := store.db.QueryContext(ctx, "SELECT table_name FROM dolt_diff WHERE commit_hash = ?", hashes[0])
	if err != nil {
		t.Fatalf("query dolt_diff: %v", err)
	}
	var committed []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan dolt_diff: %v", err)
		}
		committed = append(committed, table)
	}
	_ = rows.Close()
	sawIssues := false
	for _, table := range committed {
		if isWispPlaneTable(table) {
			t.Errorf("Dolt commit %s carries wisp-plane table %s; wisps must stay unversioned", hashes[0], table)
		}
		if table == "issues" {
			sawIssues = true
		}
	}
	if !sawIssues {
		t.Errorf("Dolt commit %s tables = %v, want the durable issues write", hashes[0], committed)
	}

	statusRows, err := store.db.QueryContext(ctx, "SELECT table_name, staged FROM dolt_status")
	if err != nil {
		t.Fatalf("query dolt_status: %v", err)
	}
	defer statusRows.Close()
	for statusRows.Next() {
		var table string
		var staged bool
		if err := statusRows.Scan(&table, &staged); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		if isWispPlaneTable(table) && staged {
			t.Errorf("wisp-plane table %s is staged after the commit; wisps must stay clone-local", table)
		}
		if !isWispPlaneTable(table) && (table == "issues" || table == "dependencies") {
			t.Errorf("durable table %s is still dirty after the commit", table)
		}
	}

	w, err := store.GetIssue(ctx, "test-unstaged-wisp")
	if err != nil {
		t.Fatalf("GetIssue(test-unstaged-wisp): %v", err)
	}
	if w.Status != types.StatusClosed || w.Title != "wisp renamed" {
		t.Errorf("wisp after commit = status %s title %q, want closed %q", w.Status, w.Title, "wisp renamed")
	}
}
