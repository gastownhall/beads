package dolt

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestUpdateIssuePromotesPhysicalWispAndKeepsLaterWritesDurable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:              "persist-wisp",
		Title:           "runtime wisp",
		Status:          types.StatusOpen,
		Priority:        2,
		IssueType:       types.TypeTask,
		Ephemeral:       true,
		ClosedBySession: "session-direct",
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
	firstChild, err := store.GetNextChildID(ctx, issue.ID)
	if err != nil {
		t.Fatalf("reserve first wisp child ID: %v", err)
	}
	if firstChild != issue.ID+".1" {
		t.Fatalf("first child ID = %q, want %q", firstChild, issue.ID+".1")
	}
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
	var lastChild, committedLastChild, wispCounterRows int
	if err := store.db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", issue.ID).Scan(&lastChild); err != nil {
		t.Fatalf("read promoted child counter: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters AS OF 'HEAD' WHERE parent_id = ?", issue.ID).Scan(&committedLastChild); err != nil {
		t.Fatalf("read committed promoted child counter: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisp_child_counters WHERE parent_id = ?", issue.ID).Scan(&wispCounterRows); err != nil {
		t.Fatalf("count source child counters: %v", err)
	}
	if lastChild != 1 || committedLastChild != 1 || wispCounterRows != 0 {
		t.Fatalf("promoted counters: working=%d committed=%d source_rows=%d, want 1/1/0",
			lastChild, committedLastChild, wispCounterRows)
	}
	secondChild, err := store.GetNextChildID(ctx, issue.ID)
	if err != nil {
		t.Fatalf("reserve next durable child ID: %v", err)
	}
	if secondChild != issue.ID+".2" {
		t.Fatalf("next child ID = %q, want %q", secondChild, issue.ID+".2")
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
	if got.ClosedBySession != issue.ClosedBySession {
		t.Errorf("promoted closed_by_session = %q, want %q", got.ClosedBySession, issue.ClosedBySession)
	}
	if !strings.Contains(string(got.Metadata), `"circuit":"closed"`) {
		t.Errorf("promoted metadata = %s, want closed circuit", got.Metadata)
	}
}

func TestUpdateIssueCheckedPromotesPhysicalWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID: "persist-checked-wisp", Title: "checked runtime wisp", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, NoHistory: true,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create checked wisp: %v", err)
	}
	current, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("get checked wisp: %v", err)
	}
	expectedStatus := string(types.StatusOpen)
	if err := store.UpdateIssueChecked(ctx, issue.ID, map[string]any{
		"wisp": false, "no_history": false,
	}, "tester", storage.UpdateIssueOptions{
		ExpectedVersion: &current.RowVersion,
		ExpectedStatus:  &expectedStatus,
	}); err != nil {
		t.Fatalf("checked durable update: %v", err)
	}

	var issueRows, wispRows int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", issue.ID).Scan(&issueRows); err != nil {
		t.Fatalf("count checked durable issue: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", issue.ID).Scan(&wispRows); err != nil {
		t.Fatalf("count checked source wisp: %v", err)
	}
	if issueRows != 1 || wispRows != 0 {
		t.Fatalf("checked promotion plane: issues=%d wisps=%d, want 1/0", issueRows, wispRows)
	}
}

func TestAutomaticPromotionLateCollisionRollsBack(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID: "persist-collision-wisp", Title: "collision runtime wisp", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, NoHistory: true,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create collision wisp: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "must-roll-back", "tester"); err != nil {
		t.Fatalf("add source label: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, issue.ID, "tester", "source comment"); err != nil {
		t.Fatalf("add source comment: %v", err)
	}
	var collisionID string
	if err := store.db.QueryRowContext(ctx,
		"SELECT id FROM wisp_comments WHERE issue_id = ? LIMIT 1", issue.ID).Scan(&collisionID); err != nil {
		t.Fatalf("read source comment ID: %v", err)
	}
	createPerm(t, ctx, store, "persist-collision-holder")
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO comments (id, issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, collisionID, "persist-collision-holder", "tester", "destination collision", time.Now().UTC()); err != nil {
		t.Fatalf("seed late comment collision: %v", err)
	}

	err := store.UpdateIssue(ctx, issue.ID, map[string]any{"wisp": false, "no_history": false}, "tester")
	if err == nil || !strings.Contains(err.Error(), "copy comments for promoted wisp") {
		t.Fatalf("automatic promotion error = %v, want strict comment collision", err)
	}

	var issueRows, wispRows, sourceLabels, destinationLabels int
	for table, dest := range map[string]*int{
		"issues": &issueRows, "wisps": &wispRows,
	} {
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+table+" WHERE id = ?", issue.ID).Scan(dest); err != nil {
			t.Fatalf("count %s after failed automatic promotion: %v", table, err)
		}
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", issue.ID).Scan(&sourceLabels); err != nil {
		t.Fatalf("count source labels after rollback: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ?", issue.ID).Scan(&destinationLabels); err != nil {
		t.Fatalf("count destination labels after rollback: %v", err)
	}
	if issueRows != 0 || wispRows != 1 || sourceLabels != 1 || destinationLabels != 0 {
		t.Fatalf("failed automatic promotion changed state: issues=%d wisps=%d source_labels=%d destination_labels=%d",
			issueRows, wispRows, sourceLabels, destinationLabels)
	}
	got, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("get source after rollback: %v", err)
	}
	if got.Ephemeral || !got.NoHistory {
		t.Fatalf("source flags after rollback: ephemeral=%v no_history=%v, want false/true",
			got.Ephemeral, got.NoHistory)
	}
}

func TestPromotionRefusesDualPlaneWithoutOverwritingEitherRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "persist-dual-plane"
	wisp := &types.Issue{
		ID: id, Title: "source wisp title", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create source wisp: %v", err)
	}
	if err := store.AddLabel(ctx, id, "source-only", "tester"); err != nil {
		t.Fatalf("add source wisp label: %v", err)
	}
	destination := *wisp
	destination.Title = "existing durable title"
	destination.Ephemeral = false
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin dual-plane seed: %v", err)
	}
	if err := insertIssueTxIntoTable(ctx, tx, "issues", &destination); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed durable destination: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit durable destination: %v", err)
	}

	err = store.PromoteFromEphemeral(ctx, id, "tester")
	if err == nil || !strings.Contains(err.Error(), "destination already exists in issues") {
		t.Fatalf("dual-plane promotion error = %v, want destination collision", err)
	}

	var durableTitle, wispTitle string
	var sourceLabels int
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", id).Scan(&durableTitle); err != nil {
		t.Fatalf("read durable row after refusal: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM wisps WHERE id = ?", id).Scan(&wispTitle); err != nil {
		t.Fatalf("read wisp row after refusal: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label = ?", id, "source-only").Scan(&sourceLabels); err != nil {
		t.Fatalf("read wisp labels after refusal: %v", err)
	}
	if durableTitle != destination.Title || wispTitle != wisp.Title || sourceLabels != 1 {
		t.Fatalf("dual-plane refusal changed state: issues=%q wisps=%q source_labels=%d",
			durableTitle, wispTitle, sourceLabels)
	}
}
