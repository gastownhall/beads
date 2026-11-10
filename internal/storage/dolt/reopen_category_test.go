package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestReopenIssueCategorySemantics(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	setStatus := func(t *testing.T, table, id, status string) int64 {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `
			UPDATE `+table+`
			SET status = ?, closed_at = UTC_TIMESTAMP(), close_reason = ?, closed_by_session = ?,
				defer_until = DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY)
			WHERE id = ?
		`, status, "completed", "session-1", id); err != nil {
			t.Fatalf("set %s status: %v", table, err)
		}
		var rowLock int64
		if err := store.db.QueryRowContext(ctx,
			"SELECT row_lock FROM "+table+" WHERE id = ?", id).Scan(&rowLock); err != nil {
			t.Fatalf("read %s row_lock: %v", table, err)
		}
		return rowLock
	}

	insertCustomStatus := func(t *testing.T, name string, category types.StatusCategory) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO custom_statuses (name, category) VALUES (?, ?)", name, string(category)); err != nil {
			t.Fatalf("insert custom status: %v", err)
		}
	}

	assertLifecycleCleared := func(t *testing.T, table, id string, priorRowLock int64) {
		t.Helper()
		var status, reason, session string
		var closedAt, deferred sql.NullTime
		var rowLock int64
		if err := store.db.QueryRowContext(ctx, `
			SELECT status, closed_at, close_reason, closed_by_session, defer_until, row_lock
			FROM `+table+` WHERE id = ?
		`, id).Scan(&status, &closedAt, &reason, &session, &deferred, &rowLock); err != nil {
			t.Fatalf("read reopened %s: %v", table, err)
		}
		if status != string(types.StatusOpen) || closedAt.Valid || reason != "" || session != "" || deferred.Valid {
			t.Fatalf("reopened lifecycle = status=%q closed_at=%v reason=%q session=%q defer=%v",
				status, closedAt.Valid, reason, session, deferred.Valid)
		}
		if rowLock == 0 || rowLock == priorRowLock {
			t.Fatalf("reopened row_lock = %d, want fresh non-zero token different from %d", rowLock, priorRowLock)
		}
	}

	t.Run("custom done permanent clears lifecycle and records events", func(t *testing.T) {
		const id = "ro-category-permanent"
		createPerm(t, ctx, store, id)
		insertCustomStatus(t, "archived", types.CategoryDone)
		priorRowLock := setStatus(t, "issues", id, "archived")
		before := reopenDoltHead(t, ctx, store)

		if err := store.ReopenIssue(ctx, id, "needs review", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}
		assertLifecycleCleared(t, "issues", id, priorRowLock)
		if after := reopenDoltHead(t, ctx, store); after == before {
			t.Fatal("permanent reopen did not create a Dolt commit")
		}

		var reopened, comments int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", id, string(types.EventReopened)).Scan(&reopened); err != nil {
			t.Fatalf("count reopened events: %v", err)
		}
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", id, string(types.EventCommented)).Scan(&comments); err != nil {
			t.Fatalf("count comment events: %v", err)
		}
		if reopened != 1 || comments != 1 {
			t.Fatalf("events = reopened:%d comments:%d, want 1 each", reopened, comments)
		}
	})

	t.Run("custom done wisp mutates without a Dolt commit", func(t *testing.T) {
		const id = "ro-category-wisp"
		createWisp(t, ctx, store, id)
		insertCustomStatus(t, "retired", types.CategoryDone)
		priorRowLock := setStatus(t, "wisps", id, "retired")
		before := reopenDoltHead(t, ctx, store)

		if err := store.ReopenIssue(ctx, id, "", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}
		assertLifecycleCleared(t, "wisps", id, priorRowLock)
		if after := reopenDoltHead(t, ctx, store); after != before {
			t.Fatalf("wisp reopen changed Dolt HEAD from %s to %s", before, after)
		}
	})

	t.Run("wisp reopen commits durable depender recomputation", func(t *testing.T) {
		const (
			dependerID = "ro-category-wisp-depender"
			targetID   = "ro-category-wisp-target"
		)
		createPerm(t, ctx, store, dependerID)
		createWisp(t, ctx, store, targetID)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: dependerID, DependsOnID: targetID, Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		if err := store.CloseIssue(ctx, targetID, "done", "tester", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		before := reopenDoltHead(t, ctx, store)

		if err := store.ReopenIssue(ctx, targetID, "", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}
		var blocked bool
		if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", dependerID).Scan(&blocked); err != nil {
			t.Fatalf("read durable depender: %v", err)
		}
		if !blocked {
			t.Fatal("permanent depender was not re-blocked by reopened wisp")
		}
		if after := reopenDoltHead(t, ctx, store); after == before {
			t.Fatal("wisp reopen with durable recomputation did not create a Dolt commit")
		}
	})

	t.Run("custom non done and unknown statuses stay untouched", func(t *testing.T) {
		const activeID = "ro-category-active"
		const unknownID = "ro-category-unknown"
		createPerm(t, ctx, store, activeID)
		createPerm(t, ctx, store, unknownID)
		insertCustomStatus(t, "triaged", types.CategoryActive)
		setStatus(t, "issues", activeID, "triaged")
		setStatus(t, "issues", unknownID, "unknown-status")

		for _, check := range []struct {
			id     string
			status string
		}{{activeID, "triaged"}, {unknownID, "unknown-status"}} {
			before := reopenDoltHead(t, ctx, store)
			var beforeEvents int
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM events WHERE issue_id = ?", check.id).Scan(&beforeEvents); err != nil {
				t.Fatalf("count events before reopen for %s: %v", check.id, err)
			}
			if err := store.ReopenIssue(ctx, check.id, "ignored", "tester"); err != nil {
				t.Fatalf("ReopenIssue(%s): %v", check.id, err)
			}
			if after := reopenDoltHead(t, ctx, store); after != before {
				t.Fatalf("ineligible reopen for %s changed Dolt HEAD", check.id)
			}
			var status, reason string
			var afterEvents int
			if err := store.db.QueryRowContext(ctx,
				"SELECT status, close_reason FROM issues WHERE id = ?", check.id).Scan(&status, &reason); err != nil {
				t.Fatalf("read unchanged issue %s: %v", check.id, err)
			}
			if status != check.status || reason != "completed" {
				t.Fatalf("unchanged issue %s = status:%q reason:%q", check.id, status, reason)
			}
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM events WHERE issue_id = ?", check.id).Scan(&afterEvents); err != nil {
				t.Fatalf("count events for %s: %v", check.id, err)
			}
			if afterEvents != beforeEvents {
				t.Fatalf("ineligible reopen for %s changed event count from %d to %d", check.id, beforeEvents, afterEvents)
			}
		}
	})

	t.Run("literal open is a strict no-op", func(t *testing.T) {
		const id = "ro-category-open"
		createPerm(t, ctx, store, id)
		before := reopenDoltHead(t, ctx, store)
		beforeEvents, err := store.GetEvents(ctx, id, 0)
		if err != nil {
			t.Fatalf("GetEvents before reopen: %v", err)
		}

		if err := store.ReopenIssue(ctx, id, "ignored", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}
		if after := reopenDoltHead(t, ctx, store); after != before {
			t.Fatalf("literal-open no-op changed Dolt HEAD from %s to %s", before, after)
		}
		afterEvents, err := store.GetEvents(ctx, id, 0)
		if err != nil {
			t.Fatalf("GetEvents after reopen: %v", err)
		}
		if len(afterEvents) != len(beforeEvents) {
			t.Fatalf("literal-open no-op events = %+v, want no new events", afterEvents)
		}
	})

	t.Run("missing target wraps not found", func(t *testing.T) {
		err := store.ReopenIssue(ctx, "ro-category-missing", "", "tester")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func reopenDoltHead(t *testing.T, ctx context.Context, store *DoltStore) string {
	t.Helper()
	var head string
	if err := store.db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
		t.Fatalf("read Dolt HEAD: %v", err)
	}
	return head
}
