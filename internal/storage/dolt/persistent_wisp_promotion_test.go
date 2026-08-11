package dolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestUpdateIssuePromotesPhysicalWispAndKeepsLaterWritesDurable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "persist-wisp",
		Title:     "runtime wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}

	assertPlane := func(label string, wantIssues, wantWisps int) {
		t.Helper()
		var issueRows, wispRows int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM issues WHERE id = ?", issue.ID).Scan(&issueRows); err != nil {
			t.Fatalf("%s: count issues: %v", label, err)
		}
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM wisps WHERE id = ?", issue.ID).Scan(&wispRows); err != nil {
			t.Fatalf("%s: count wisps: %v", label, err)
		}
		if issueRows != wantIssues || wispRows != wantWisps {
			t.Fatalf("%s: issues=%d wisps=%d, want issues=%d wisps=%d",
				label, issueRows, wispRows, wantIssues, wantWisps)
		}
	}

	assertPlane("after create", 0, 1)
	if err := store.UpdateIssue(ctx, issue.ID, map[string]any{
		"wisp": false, "no_history": true,
	}, "tester"); err != nil {
		t.Fatalf("select no-history mode: %v", err)
	}
	assertPlane("after no-history selection", 0, 1)

	if err := store.UpdateIssue(ctx, issue.ID, map[string]any{
		"wisp": false, "no_history": false, "metadata": `{"phase":"durable"}`,
	}, "tester"); err != nil {
		t.Fatalf("select durable mode: %v", err)
	}
	assertPlane("after durable selection", 1, 0)
	var committedIssueRows int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = ?", issue.ID).Scan(&committedIssueRows); err != nil {
		t.Fatalf("count committed durable issue: %v", err)
	}
	if committedIssueRows != 1 {
		t.Fatalf("committed durable issue rows = %d, want 1", committedIssueRows)
	}

	if err := store.ClaimIssue(ctx, issue.ID, "worker"); err != nil {
		t.Fatalf("claim promoted issue: %v", err)
	}
	if err := store.UpdateIssue(ctx, issue.ID,
		map[string]any{"metadata": `{"phase":"durable","circuit":"closed"}`}, "worker"); err != nil {
		t.Fatalf("update promoted issue: %v", err)
	}
	assertPlane("after later claim and metadata update", 1, 0)

	got, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("get promoted issue: %v", err)
	}
	if got.Status != types.StatusInProgress || got.Assignee != "worker" {
		t.Errorf("promoted claim: status=%q assignee=%q, want in_progress/worker", got.Status, got.Assignee)
	}
	if !strings.Contains(string(got.Metadata), `"circuit":"closed"`) {
		t.Errorf("promoted metadata = %s, want closed circuit", got.Metadata)
	}
}
