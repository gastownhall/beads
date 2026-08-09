//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
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
