package dolt

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// These tests pin the single-session cross-plane transaction contract
// (PR #6015): RunInTransaction runs durable tables and dolt-ignored wisp
// tables in ONE server-side SQL transaction, so a commit-time conflict on
// either plane refuses the whole transaction determinately (nothing
// half-commits), a wisp-driven durable recompute rides the mutation's own
// Dolt commit, and the transaction holds exactly one pool connection. They
// run on the Dolt server lane; the journal is off by default, so the
// journal=off subtest exercises the default shape and journal=on the shape
// that was already pinned to one session.

// TestRunInTransactionWispCommitConflictIsDeterminateAndAtomic (AC-2): a
// commit-time conflict on a wisp-plane row refuses the whole transaction.
// Before the collapse the durable plane and its Dolt commit landed first and
// only the wisp session's SQL commit was refused, so the caller got
// ErrCommitIndeterminate with half its work applied and the durable issue it
// created existed anyway.
func TestRunInTransactionWispCommitConflictIsDeterminateAndAtomic(t *testing.T) {
	for _, journal := range []bool{false, true} {
		t.Run(fmt.Sprintf("journal=%t", journal), func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			if journal {
				store.SetEventsJournalEnabled(true)
			}
			ctx, cancel := testContext(t)
			defer cancel()

			const (
				wispID    = "csc-conflict-wisp"
				durableID = "csc-conflict-durable"
			)
			createWisp(t, ctx, store, wispID)

			// The other client runs on a pool of its own: the store's own
			// transaction may hold every connection its pool has.
			sibling, err := sql.Open("mysql", store.connStr)
			if err != nil {
				t.Fatalf("open sibling pool: %v", err)
			}
			defer func() { _ = sibling.Close() }()

			err = store.RunInTransaction(ctx, "test: wisp commit conflict", func(tx storage.Transaction) error {
				if err := tx.CreateIssue(ctx, crossTierRegularIssue(durableID, "durable created before the conflict"), "tester"); err != nil {
					return err
				}
				if err := tx.UpdateIssue(ctx, wispID, map[string]interface{}{"title": "this transaction's title"}, "tester"); err != nil {
					return err
				}
				// Another client commits a different title for the same wisp
				// before this callback returns.
				if _, err := sibling.ExecContext(ctx,
					"UPDATE wisps SET title = ? WHERE id = ?", "other client's title", wispID); err != nil {
					return err
				}
				return nil
			})
			if err == nil {
				t.Fatal("RunInTransaction succeeded, want a commit-time conflict on the wisp row")
			}
			if errors.Is(err, ErrCommitIndeterminate) {
				t.Fatalf("error = %v, want a determinate refusal: the durable plane must not half-commit", err)
			}
			if !isSerializationError(err) {
				t.Fatalf("error = %v, want a serialization conflict (1213/1205)", err)
			}
			if _, err := store.GetIssue(ctx, durableID); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("durable issue after the refused commit = %v, want ErrNotFound (both planes rolled back together)", err)
			}
			wisp, err := store.GetIssue(ctx, wispID)
			if err != nil {
				t.Fatalf("GetIssue wisp: %v", err)
			}
			if wisp.Title != "other client's title" {
				t.Errorf("wisp title = %q, want the other client's committed title %q", wisp.Title, "other client's title")
			}
		})
	}
}

// TestRunInTransactionWispCloseUnblockRidesOneDoltCommit (AC-3): a wisp close
// that unblocks a durable dependent publishes the recompute in the callback's
// own Dolt commit. Before the collapse the wisp close ran on the separate
// ignored session and the durable is_blocked fix was published by a second,
// fail-soft Dolt commit after it, so the callback's message named two commits
// and the first still showed the dependent blocked.
func TestRunInTransactionWispCloseUnblockRidesOneDoltCommit(t *testing.T) {
	for _, journal := range []bool{false, true} {
		t.Run(fmt.Sprintf("journal=%t", journal), func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			if journal {
				store.SetEventsJournalEnabled(true)
			}
			ctx, cancel := testContext(t)
			defer cancel()

			const (
				dependerID = "csc-unblock-depender"
				wispID     = "csc-unblock-blocker"
				createdID  = "csc-unblock-created"
			)
			seedTransactionWispBlocker(t, ctx, store, dependerID, wispID)

			const commitMsg = "test: wisp close unblock rides one commit"
			if err := store.RunInTransaction(ctx, commitMsg, func(tx storage.Transaction) error {
				if err := tx.CreateIssue(ctx, crossTierRegularIssue(createdID, "created alongside the wisp close"), "tester"); err != nil {
					return err
				}
				return tx.CloseIssue(ctx, wispID, "done", "tester", "")
			}); err != nil {
				t.Fatalf("RunInTransaction: %v", err)
			}

			var commits int
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM dolt_log WHERE message = ?", commitMsg).Scan(&commits); err != nil {
				t.Fatalf("count dolt_log: %v", err)
			}
			if commits != 1 {
				t.Errorf("dolt_log holds %d commit(s) named %q, want 1 (the wisp-driven unblock rides the mutation's own commit)", commits, commitMsg)
			}

			var hash string
			if err := store.db.QueryRowContext(ctx,
				"SELECT commit_hash FROM dolt_log WHERE message = ?", commitMsg).Scan(&hash); err != nil {
				t.Fatalf("read the commit's hash: %v", err)
			}
			//nolint:gosec // G201: hash comes from this database's own dolt_log
			var created int
			if err := store.db.QueryRowContext(ctx, fmt.Sprintf(
				"SELECT COUNT(*) FROM issues AS OF '%s' WHERE id = ?", hash), createdID).Scan(&created); err != nil {
				t.Fatalf("read created issue AS OF the commit: %v", err)
			}
			if created != 1 {
				t.Errorf("issues AS OF %s holds %d row(s) for %s, want 1", hash, created, createdID)
			}
			//nolint:gosec // G201: hash comes from this database's own dolt_log
			var blocked bool
			if err := store.db.QueryRowContext(ctx, fmt.Sprintf(
				"SELECT is_blocked FROM issues AS OF '%s' WHERE id = ?", hash), dependerID).Scan(&blocked); err != nil {
				t.Fatalf("read depender AS OF the commit: %v", err)
			}
			if blocked {
				t.Errorf("depender %s is_blocked AS OF %s = true, want false (the unblock is in the same commit)", dependerID, hash)
			}

			var dirty int
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM dolt_status WHERE table_name IN ('issues','dependencies')").Scan(&dirty); err != nil {
				t.Fatalf("read dolt_status: %v", err)
			}
			if dirty != 0 {
				t.Errorf("dolt_status lists %d durable table row(s) after the commit, want 0", dirty)
			}
		})
	}
}

// TestRunInTransactionHoldsOneConnection (AC-4): a callback that has written
// to both planes holds exactly one pool connection. Before the collapse the
// journal-off shape borrowed (or dialed) a second connection for the wisp
// plane, so InUse inside the callback was two. The pool is warmed first so
// the split shape cannot take the fresh-dial fallback (a separate pool whose
// connection this store's Stats would not count) and pass vacuously.
func TestRunInTransactionHoldsOneConnection(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	store.db.SetMaxIdleConns(2)
	for i := 0; i < 2; i++ {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("warm pool connection %d: %v", i, err)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("release warm connection %d: %v", i, err)
		}
	}
	inUseBefore := store.db.Stats().InUse

	var inUseDuring int
	if err := store.RunInTransaction(ctx, "test: one connection", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, crossTierRegularIssue("csc-oneconn-durable", "durable write"), "tester"); err != nil {
			return err
		}
		if err := tx.CreateIssue(ctx, crossTierWispIssue("csc-oneconn-wisp", "wisp write"), "tester"); err != nil {
			return err
		}
		inUseDuring = store.db.Stats().InUse
		return nil
	}); err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	if inUseDuring != inUseBefore+1 {
		t.Errorf("db.Stats().InUse inside the callback = %d, want %d (exactly one connection: no second session borrowed or dialed)", inUseDuring, inUseBefore+1)
	}
}

// TestRunInTransactionKeepsWispsUnstaged (AC-5): the collapse must leave
// dolt_ignore semantics alone. A callback that creates, updates and closes
// wisps alongside a durable write still produces a Dolt commit that stages no
// wisps/wisp_* table, and afterwards dolt_status still lists the wisp tables
// only as ignored/unstaged (wisps remain clone-local). This is a regression
// guard: it pins behavior that already holds at the base.
func TestRunInTransactionKeepsWispsUnstaged(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const (
		wispID    = "csc-unstaged-wisp"
		durableID = "csc-unstaged-durable"
	)
	const commitMsg = "test: wisps stay unstaged"
	if err := store.RunInTransaction(ctx, commitMsg, func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, crossTierWispIssue(wispID, "wisp"), "tester"); err != nil {
			return err
		}
		if err := tx.UpdateIssue(ctx, wispID, map[string]interface{}{"title": "wisp updated"}, "tester"); err != nil {
			return err
		}
		if err := tx.CloseIssue(ctx, wispID, "done", "tester", ""); err != nil {
			return err
		}
		return tx.CreateIssue(ctx, crossTierRegularIssue(durableID, "durable"), "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}

	// The durable write reached HEAD...
	requireCleanTables(ctx, t, store, "issues", "labels")
	var commits int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE message = ?", commitMsg).Scan(&commits); err != nil {
		t.Fatalf("count dolt_log: %v", err)
	}
	if commits != 1 {
		t.Errorf("dolt_log holds %d commit(s) named %q, want 1", commits, commitMsg)
	}
	// ...and the wisp tables stayed clone-local: still listed in dolt_status as
	// ignored/unstaged, never staged into the Dolt commit.
	for _, table := range []string{"wisps", "wisp_events"} {
		if !tableDirty(ctx, t, store, table) {
			t.Errorf("%s is not listed in dolt_status after the transaction; the wisp plane must remain ignored/unstaged", table)
		}
	}
	if _, err := store.GetIssue(ctx, wispID); err != nil {
		t.Fatalf("GetIssue closed wisp after the commit: %v", err)
	}
}
