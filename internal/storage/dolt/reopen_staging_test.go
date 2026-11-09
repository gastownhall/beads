package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestReopenWispStagesOnlyConcreteIssueChanges(t *testing.T) {
	newStore := func(t *testing.T) (*DoltStore, context.Context) {
		t.Helper()
		store, cleanup := setupTestStore(t)
		t.Cleanup(cleanup)
		ctx, cancel := testContext(t)
		t.Cleanup(cancel)
		return store, ctx
	}

	t.Run("isolated wisp leaves unrelated dirty issues and events uncommitted", func(t *testing.T) {
		store, ctx := newStore(t)
		const (
			wispID  = "ro-stage-isolated-wisp"
			issueID = "ro-stage-unrelated-issue"
			eventID = "ro-stage-unrelated-event"
		)
		createPerm(t, ctx, store, issueID)
		createWisp(t, ctx, store, wispID)
		if err := store.CloseIssue(ctx, wispID, "done", "tester", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		before := reopenDoltHead(t, ctx, store)
		stageReopenDirtyIssue(t, ctx, store, issueID)
		stageReopenDirtyEvent(t, ctx, store, eventID, issueID)

		if err := store.ReopenIssue(ctx, wispID, "", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}
		if after := reopenDoltHead(t, ctx, store); after != before {
			t.Fatalf("isolated wisp reopen changed HEAD from %s to %s", before, after)
		}
		assertReopenDirtyRowsUncommitted(t, ctx, store, issueID, eventID)
	})

	t.Run("affected issue without a SQL flip remains uncommitted", func(t *testing.T) {
		store, ctx := newStore(t)
		const (
			dependerID = "ro-stage-no-flip-depender"
			wispID     = "ro-stage-no-flip-wisp"
			dirtyID    = "ro-stage-no-flip-dirty"
		)
		createPerm(t, ctx, store, dependerID)
		createPerm(t, ctx, store, dirtyID)
		createWisp(t, ctx, store, wispID)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: dependerID, DependsOnID: wispID, Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		if err := store.CloseIssue(ctx, wispID, "done", "tester", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		if _, err := store.db.ExecContext(ctx,
			"UPDATE issues SET is_blocked = 1 WHERE id = ?", dependerID); err != nil {
			t.Fatalf("pre-converge affected issue: %v", err)
		}
		before := reopenDoltHead(t, ctx, store)
		stageReopenDirtyIssue(t, ctx, store, dirtyID)

		if err := store.ReopenIssue(ctx, wispID, "", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}
		if after := reopenDoltHead(t, ctx, store); after != before {
			t.Fatalf("no-flip wisp reopen changed HEAD from %s to %s", before, after)
		}
		var headNotes string
		if err := store.db.QueryRowContext(ctx,
			"SELECT notes FROM issues AS OF 'HEAD' WHERE id = ?", dirtyID).Scan(&headNotes); err != nil {
			t.Fatalf("read dirty issue AS OF HEAD: %v", err)
		}
		if headNotes == "uncommitted reopen sentinel" {
			t.Fatal("no-flip wisp reopen staged an unrelated dirty issue")
		}
	})

	t.Run("durable issue flip commits issues but not unrelated event", func(t *testing.T) {
		store, ctx := newStore(t)
		const (
			dependerID = "ro-stage-flip-depender"
			wispID     = "ro-stage-flip-wisp"
			eventID    = "ro-stage-flip-dirty-event"
		)
		createPerm(t, ctx, store, dependerID)
		createWisp(t, ctx, store, wispID)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: dependerID, DependsOnID: wispID, Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		if err := store.CloseIssue(ctx, wispID, "done", "tester", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		commitReopenIssueWorkingSet(t, ctx, store, "seed closed-wisp projection")
		before := reopenDoltHead(t, ctx, store)
		stageReopenDirtyEvent(t, ctx, store, eventID, dependerID)

		if err := store.ReopenIssue(ctx, wispID, "", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}
		if after := reopenDoltHead(t, ctx, store); after == before {
			t.Fatal("wisp reopen with an actual issues-row flip did not advance HEAD")
		}
		var blocked bool
		if err := store.db.QueryRowContext(ctx,
			"SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", dependerID).Scan(&blocked); err != nil {
			t.Fatalf("read durable depender AS OF HEAD: %v", err)
		}
		if !blocked {
			t.Fatal("recomputed durable issue is not visible AS OF HEAD")
		}
		// events is dolt_ignored since migration 0062 (bd-red8u), so it has no
		// HEAD state of its own: the sentinel row cannot ride the issues commit,
		// but it must survive that commit in the working set. The plane
		// invariant — no events row ever reaches committed history — is what
		// assertEventsNotCommitted still checks here, where HEAD does advance.
		var workingEvent int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events WHERE id = ?", eventID).Scan(&workingEvent); err != nil {
			t.Fatalf("read dirty event in working set: %v", err)
		}
		if workingEvent != 1 {
			t.Fatalf("dirty working-set event count = %d, want 1 (the issues commit dropped it)", workingEvent)
		}
		assertEventsNotCommitted(ctx, t, store.db)
	})
}

// TestReopenCommentIsCommittedWithTheReopen pins GH#5593's durable half: a
// reasoned reopen's comment must reach the SAME Dolt commit as the reopened
// issue on every selective staging route, with no comments-table change left
// pending in the working set afterwards. The comment row exists in the SQL
// working set on every route; only staging decides whether HEAD carries it.
func TestReopenCommentIsCommittedWithTheReopen(t *testing.T) {
	newStore := func(t *testing.T) (*DoltStore, context.Context) {
		t.Helper()
		store, cleanup := setupTestStore(t)
		t.Cleanup(cleanup)
		ctx, cancel := testContext(t)
		t.Cleanup(cancel)
		return store, ctx
	}
	seedClosed := func(t *testing.T, ctx context.Context, store *DoltStore, id string) string {
		t.Helper()
		createPerm(t, ctx, store, id)
		if err := store.CloseIssue(ctx, id, "done", "tester", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
		return reopenDoltHead(t, ctx, store)
	}
	assertReopenCommittedWithComment := func(t *testing.T, ctx context.Context, store *DoltStore, id, reason string, before string) {
		t.Helper()
		if after := reopenDoltHead(t, ctx, store); after == before {
			t.Fatal("reasoned reopen did not advance HEAD")
		}
		var status string
		if err := store.db.QueryRowContext(ctx,
			"SELECT status FROM issues AS OF 'HEAD' WHERE id = ?", id).Scan(&status); err != nil {
			t.Fatalf("read reopened issue AS OF HEAD: %v", err)
		}
		if types.Status(status) != types.StatusOpen {
			t.Fatalf("issue %s at HEAD is %q, want open", id, status)
		}
		var committed int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ? AND author = ? AND text = ?",
			id, "tester", reason).Scan(&committed); err != nil {
			t.Fatalf("read reopen comment AS OF HEAD: %v", err)
		}
		if committed != 1 {
			t.Fatalf("HEAD carries %d reopen comments for %s, want the one from the reopen", committed, id)
		}
		// The comment must not be left as a pending working-set change: it
		// belongs to the reopen's commit, not the next writer's.
		var pending int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'comments'").Scan(&pending); err != nil {
			t.Fatalf("read dolt_status for comments: %v", err)
		}
		if pending != 0 {
			t.Fatal("comments is still a pending working-set change after the reopen commit")
		}
	}

	t.Run("store ReopenIssue", func(t *testing.T) {
		store, ctx := newStore(t)
		const id = "ro-cmt-store"
		before := seedClosed(t, ctx, store, id)
		if err := store.ReopenIssue(ctx, id, "regressed in QA", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}
		assertReopenCommittedWithComment(t, ctx, store, id, "regressed in QA", before)
	})

	t.Run("lifecycle Reopen", func(t *testing.T) {
		store, ctx := newStore(t)
		const id = "ro-cmt-lifecycle"
		before := seedClosed(t, ctx, store, id)
		lifecycle, err := store.IssueLifecycle()
		if err != nil {
			t.Fatalf("IssueLifecycle: %v", err)
		}
		if _, err := lifecycle.Reopen(ctx, issueops.ReopenRequest{Actor: "tester", IssueID: id, Reason: "not fixed yet"}); err != nil {
			t.Fatalf("lifecycle Reopen: %v", err)
		}
		assertReopenCommittedWithComment(t, ctx, store, id, "not fixed yet", before)
	})

	t.Run("transaction ReopenIssueWithResult", func(t *testing.T) {
		store, ctx := newStore(t)
		const id = "ro-cmt-tx"
		before := seedClosed(t, ctx, store, id)
		if err := store.RunInIssueLifecycleTransaction(ctx, "bd: reopen "+id, func(tx storage.IssueLifecycleTransaction) error {
			if _, err := tx.ReopenIssueWithResult(ctx, id, "needs another pass", "tester"); err != nil {
				return err
			}
			return nil
		}); err != nil {
			t.Fatalf("RunInIssueLifecycleTransaction reopen: %v", err)
		}
		assertReopenCommittedWithComment(t, ctx, store, id, "needs another pass", before)
	})
}

func stageReopenDirtyIssue(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx,
		"UPDATE issues SET notes = ? WHERE id = ?", "uncommitted reopen sentinel", id); err != nil {
		t.Fatalf("stage dirty issue: %v", err)
	}
}

func stageReopenDirtyEvent(t *testing.T, ctx context.Context, store *DoltStore, eventID, issueID string) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO events (id, issue_id, event_type, actor, old_value, new_value) VALUES (?, ?, ?, ?, ?, ?)",
		eventID, issueID, string(types.EventUpdated), "other", "", "uncommitted reopen event",
	); err != nil {
		t.Fatalf("stage dirty event: %v", err)
	}
}

func assertReopenDirtyRowsUncommitted(
	t *testing.T, ctx context.Context, store *DoltStore, issueID, eventID string,
) {
	t.Helper()
	var headNotes string
	if err := store.db.QueryRowContext(ctx,
		"SELECT notes FROM issues AS OF 'HEAD' WHERE id = ?", issueID).Scan(&headNotes); err != nil {
		t.Fatalf("read dirty issue AS OF HEAD: %v", err)
	}
	if headNotes == "uncommitted reopen sentinel" {
		t.Fatal("isolated wisp reopen staged an unrelated dirty issue")
	}
	// events is dolt_ignored since migration 0062 (bd-red8u): there is no HEAD
	// state to read. Both callers already assert that HEAD did not move at all,
	// which covers "nothing was published"; what is left to check about the
	// sentinel event is that the reopen did not destroy it in the working set.
	var workingEvent int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE id = ?", eventID).Scan(&workingEvent); err != nil {
		t.Fatalf("read dirty event in working set: %v", err)
	}
	if workingEvent != 1 {
		t.Fatalf("dirty working-set event count = %d, want 1 (the reopen dropped it)", workingEvent)
	}
}

func commitReopenIssueWorkingSet(t *testing.T, ctx context.Context, store *DoltStore, message string) {
	t.Helper()
	if err := store.doltAddAndCommit(ctx, []string{"issues"}, message); err != nil {
		t.Fatalf("doltAddAndCommit issues: %v", err)
	}
}
