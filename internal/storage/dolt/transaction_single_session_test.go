package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
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
			if _, err := other.ExecContext(ctx, "UPDATE wisps SET title = ? WHERE id = ?", "theirs", wispID); err != nil {
				t.Fatalf("other client's wisp update: %v", err)
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
