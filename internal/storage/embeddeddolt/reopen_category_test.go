//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestEmbeddedReopenIssueCategorySemantics(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rct")
	ctx := t.Context()

	create := func(id string, ephemeral bool) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	setStatus := func(table, id, status string) {
		t.Helper()
		te.exec(t, ctx, `
			UPDATE `+table+`
			SET status = ?, closed_at = UTC_TIMESTAMP(), close_reason = ?, closed_by_session = ?,
				defer_until = DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY)
			WHERE id = ?
		`, status, "completed", "session-1", id)
	}
	assertReopened := func(id string) {
		t.Helper()
		issue, err := te.store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		if issue.Status != types.StatusOpen || issue.ClosedAt != nil || issue.CloseReason != "" || issue.ClosedBySession != "" || issue.DeferUntil != nil {
			t.Fatalf("reopened issue = %+v", issue)
		}
	}
	assertReopenEvents := func(id string) {
		t.Helper()
		events, err := te.store.GetEvents(ctx, id, 0)
		if err != nil {
			t.Fatalf("GetEvents(%s): %v", id, err)
		}
		var reopened, commented, statusChanged int
		for _, event := range events {
			switch event.EventType {
			case types.EventReopened:
				reopened++
			case types.EventCommented:
				commented++
			case types.EventStatusChanged:
				statusChanged++
			}
		}
		if reopened != 1 || commented != 1 || statusChanged != 0 {
			t.Fatalf(
				"reopen events for %s = reopened:%d commented:%d status_changed:%d, want 1, 1, 0",
				id, reopened, commented, statusChanged,
			)
		}
	}

	te.exec(t, ctx, "INSERT INTO custom_statuses (name, category) VALUES (?, ?)", "archived", string(types.CategoryDone))
	create("rct-permanent", false)
	create("rct-wisp", true)
	setStatus("issues", "rct-permanent", "archived")
	setStatus("wisps", "rct-wisp", "archived")

	for _, id := range []string{"rct-permanent", "rct-wisp"} {
		if err := te.store.ReopenIssue(ctx, id, "needs work", "tester"); err != nil {
			t.Fatalf("ReopenIssue(%s): %v", id, err)
		}
		assertReopened(id)
		assertReopenEvents(id)
	}

	for _, status := range []struct {
		id       string
		name     string
		category types.StatusCategory
	}{
		{id: "rct-active", name: "triaged", category: types.CategoryActive},
		{id: "rct-wip", name: "testing", category: types.CategoryWIP},
		{id: "rct-frozen", name: "on-ice", category: types.CategoryFrozen},
		{id: "rct-unknown", name: "unknown-status", category: types.CategoryUnspecified},
	} {
		if status.category != types.CategoryUnspecified {
			te.exec(t, ctx, "INSERT INTO custom_statuses (name, category) VALUES (?, ?)", status.name, string(status.category))
		}
		create(status.id, false)
		setStatus("issues", status.id, status.name)
		if err := te.store.ReopenIssue(ctx, status.id, "ignored", "tester"); err != nil {
			t.Fatalf("ReopenIssue(%s): %v", status.name, err)
		}
		issue, err := te.store.GetIssue(ctx, status.id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", status.name, err)
		}
		if issue.Status != types.Status(status.name) || issue.CloseReason != "completed" {
			t.Fatalf("non-done issue %s changed: %+v", status.name, issue)
		}
	}

	create("rct-open", false)
	beforeEvents, err := te.store.GetEvents(ctx, "rct-open", 0)
	if err != nil {
		t.Fatalf("GetEvents(open) before reopen: %v", err)
	}
	if err := te.store.ReopenIssue(ctx, "rct-open", "ignored", "tester"); err != nil {
		t.Fatalf("ReopenIssue(open): %v", err)
	}
	afterEvents, err := te.store.GetEvents(ctx, "rct-open", 0)
	if err != nil {
		t.Fatalf("GetEvents(open) after reopen: %v", err)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("literal-open no-op events = %+v, want no new events", afterEvents)
	}

	err = te.store.ReopenIssue(ctx, "rct-missing", "", "tester")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing reopen err = %v, want ErrNotFound", err)
	}
}

// TestReopenCommentIsCommittedWithTheReopen is the embedded twin of the dolt
// staging test of the same name (GH#5593): a reasoned reopen's comment must
// reach the SAME Dolt commit as the reopened issue on every committing route,
// and the deferred store route must flush it with the working set the CLI's
// auto-commit publishes. No comments-table change may be left pending after.
func TestReopenCommentIsCommittedWithTheReopen(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()

	seedClosed := func(t *testing.T, te *testEnv, id string) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
		if err := te.store.CloseIssue(ctx, id, "done", "tester", ""); err != nil {
			t.Fatalf("CloseIssue(%s): %v", id, err)
		}
	}
	assertReopenCommittedWithComment := func(t *testing.T, te *testEnv, id, reason string) {
		t.Helper()
		var status string
		te.queryScalar(t, ctx, "SELECT status FROM issues AS OF 'HEAD' WHERE id = ?", []any{id}, &status)
		if types.Status(status) != types.StatusOpen {
			t.Fatalf("issue %s at HEAD is %q, want open", id, status)
		}
		var committed int
		te.queryScalar(t, ctx,
			"SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ? AND author = ? AND text = ?",
			[]any{id, "tester", reason}, &committed)
		if committed != 1 {
			t.Fatalf("HEAD carries %d reopen comments for %s, want the one from the reopen", committed, id)
		}
		var pending int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'comments'", []any{}, &pending)
		if pending != 0 {
			t.Fatal("comments is still a pending working-set change after the reopen commit")
		}
	}

	t.Run("lifecycle Reopen", func(t *testing.T) {
		te := newTestEnv(t, "rcs")
		const id = "rcs-lifecycle"
		seedClosed(t, te, id)
		lifecycle, err := te.store.IssueLifecycle()
		if err != nil {
			t.Fatalf("IssueLifecycle: %v", err)
		}
		if _, err := lifecycle.Reopen(ctx, issueops.ReopenRequest{Actor: "tester", IssueID: id, Reason: "not fixed yet"}); err != nil {
			t.Fatalf("lifecycle Reopen: %v", err)
		}
		assertReopenCommittedWithComment(t, te, id, "not fixed yet")
	})

	t.Run("transaction ReopenIssueWithResult", func(t *testing.T) {
		te := newTestEnv(t, "rct")
		const id = "rct-transaction"
		seedClosed(t, te, id)
		if err := te.store.RunInIssueLifecycleTransaction(ctx, "bd: reopen "+id, func(tx storage.IssueLifecycleTransaction) error {
			if _, err := tx.ReopenIssueWithResult(ctx, id, "needs another pass", "tester"); err != nil {
				return err
			}
			return nil
		}); err != nil {
			t.Fatalf("RunInIssueLifecycleTransaction reopen: %v", err)
		}
		assertReopenCommittedWithComment(t, te, id, "needs another pass")
	})

	t.Run("store ReopenIssue flushes with CommitPending", func(t *testing.T) {
		te := newTestEnv(t, "rcd")
		const id = "rcd-deferred"
		seedClosed(t, te, id)
		if err := te.store.ReopenIssue(ctx, id, "regressed in QA", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}
		// The store route leaves the SQL-committed working set uncommitted,
		// the state the embedded CLI's deferred auto-commit publishes.
		if _, err := te.store.CommitPending(ctx, "tester"); err != nil {
			t.Fatalf("CommitPending: %v", err)
		}
		assertReopenCommittedWithComment(t, te, id, "regressed in QA")
	})
}

func TestEmbeddedReopenCustomDoneFallsBackWhenNormalizedTableMissing(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rcf")
	ctx := t.Context()
	const id = "rcf-legacy-done"

	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := te.store.SetConfig(ctx, "status.custom", "archived:done"); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}
	te.exec(t, ctx, `
		UPDATE issues
		SET status = ?, closed_at = UTC_TIMESTAMP(), close_reason = ?
		WHERE id = ?
	`, "archived", "completed", id)
	te.exec(t, ctx, "DROP TABLE custom_statuses")

	if err := te.store.ReopenIssue(ctx, id, "", "tester"); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}
	issue, err := te.store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != types.StatusOpen || issue.ClosedAt != nil || issue.CloseReason != "" {
		t.Fatalf("reopened issue = %+v", issue)
	}
}
