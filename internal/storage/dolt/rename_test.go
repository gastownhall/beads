package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateIssueIDUpdatesWispTables verifies that renaming a regular issue
// also updates cross-references in wisp_* auxiliary tables (wisp_dependencies,
// wisp_events, wisp_labels, wisp_comments). (bd-8ykk)
func TestUpdateIssueIDUpdatesWispTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a permanent issue that we'll rename
	issue := &types.Issue{
		ID:        "test-old1",
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Create a wisp that references the issue via wisp_dependencies
	wisp := &types.Issue{
		ID:        "test-wisp-abc",
		Title:     "Test wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.createWisp(ctx, wisp, "test"); err != nil {
		t.Fatalf("failed to create wisp: %v", err)
	}

	// Add wisp dependency: wisp depends on the permanent issue
	dep := &types.Dependency{
		IssueID:     wisp.ID,
		DependsOnID: "test-old1",
		Type:        types.DepBlocks,
	}
	if err := store.addWispDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("failed to add wisp dependency: %v", err)
	}

	// Add wisp dependency: some other wisp has issue_id = old issue
	wisp2 := &types.Issue{
		ID:        "test-wisp-def",
		Title:     "Another wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.createWisp(ctx, wisp2, "test"); err != nil {
		t.Fatalf("failed to create wisp2: %v", err)
	}

	// Add wisp_dependencies row where issue_id is the old ID
	dep2 := &types.Dependency{
		IssueID:     "test-old1",
		DependsOnID: wisp2.ID,
		Type:        types.DepBlocks,
	}
	if err := store.addWispDependency(ctx, dep2, "test"); err != nil {
		t.Fatalf("failed to add wisp dependency 2: %v", err)
	}

	// Add a wisp label for the old issue ID
	if err := store.addWispLabel(ctx, "test-old1", "bug", "test"); err != nil {
		t.Fatalf("failed to add wisp label: %v", err)
	}

	// Add a wisp event for the old issue ID (via direct SQL since there's no addWispEvent)
	_, err := store.execContext(ctx, `
		INSERT INTO wisp_events (issue_id, event_type, actor) VALUES (?, 'test_event', 'test')
	`, "test-old1")
	if err != nil {
		t.Fatalf("failed to add wisp event: %v", err)
	}

	// Add a wisp comment for the old issue ID
	_, err = store.execContext(ctx, `
		INSERT INTO wisp_comments (issue_id, author, text) VALUES (?, 'test', 'test comment')
	`, "test-old1")
	if err != nil {
		t.Fatalf("failed to add wisp comment: %v", err)
	}

	// Now rename the issue
	newID := "test-new1"
	if err := store.UpdateIssueID(ctx, "test-old1", newID, issue, "test"); err != nil {
		t.Fatalf("UpdateIssueID failed: %v", err)
	}

	// Verify wisp_dependencies.depends_on_id was updated
	var depCount int
	err = store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_dependencies WHERE depends_on_id = ?`, newID).Scan(&depCount)
	if err != nil {
		t.Fatalf("failed to query wisp_dependencies depends_on_id: %v", err)
	}
	if depCount != 1 {
		t.Errorf("expected 1 wisp_dependencies row with depends_on_id=%q, got %d", newID, depCount)
	}

	// Verify wisp_dependencies.issue_id was updated
	err = store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?`, newID).Scan(&depCount)
	if err != nil {
		t.Fatalf("failed to query wisp_dependencies issue_id: %v", err)
	}
	if depCount != 1 {
		t.Errorf("expected 1 wisp_dependencies row with issue_id=%q, got %d", newID, depCount)
	}

	// Verify old ID is gone from wisp_dependencies
	var oldCount int
	err = store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? OR depends_on_id = ?`,
		"test-old1", "test-old1").Scan(&oldCount)
	if err != nil {
		t.Fatalf("failed to query old wisp_dependencies: %v", err)
	}
	if oldCount != 0 {
		t.Errorf("expected 0 wisp_dependencies rows with old ID, got %d", oldCount)
	}

	// Verify wisp_events was updated
	// 2 rows: the manually-inserted event + the label_added event from addWispLabel
	var eventCount int
	err = store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?`, newID).Scan(&eventCount)
	if err != nil {
		t.Fatalf("failed to query wisp_events: %v", err)
	}
	if eventCount != 2 {
		t.Errorf("expected 2 wisp_events rows with issue_id=%q, got %d", newID, eventCount)
	}

	// Verify wisp_labels was updated
	var labelCount int
	err = store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?`, newID).Scan(&labelCount)
	if err != nil {
		t.Fatalf("failed to query wisp_labels: %v", err)
	}
	if labelCount != 1 {
		t.Errorf("expected 1 wisp_labels row with issue_id=%q, got %d", newID, labelCount)
	}

	// Verify wisp_comments was updated
	var commentCount int
	err = store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_comments WHERE issue_id = ?`, newID).Scan(&commentCount)
	if err != nil {
		t.Fatalf("failed to query wisp_comments: %v", err)
	}
	if commentCount != 1 {
		t.Errorf("expected 1 wisp_comments row with issue_id=%q, got %d", newID, commentCount)
	}
}

// TestUpdateIssueIDRenamesWisp verifies that UpdateIssueID correctly renames
// wisps in the wisps table and all wisp_* auxiliary tables when the target
// is itself a wisp (not a regular issue). (bd-8ykk)
func TestUpdateIssueIDRenamesWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a wisp (goes to wisps + wisp_* tables)
	wisp := &types.Issue{
		Title:     "Wisp to rename",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.createWisp(ctx, wisp, "tester"); err != nil {
		t.Fatalf("createWisp failed: %v", err)
	}
	oldID := wisp.ID
	if oldID == "" {
		t.Fatal("wisp got empty ID")
	}

	// Verify wisp is in the wisps table
	if !store.isActiveWisp(ctx, oldID) {
		t.Fatalf("expected %q to be an active wisp", oldID)
	}

	// Verify creation event was recorded in wisp_events
	var eventCount int
	err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?`, oldID).Scan(&eventCount)
	if err != nil {
		t.Fatalf("failed to count wisp_events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 wisp_events row for old ID, got %d", eventCount)
	}

	// Add a label to the wisp
	if err := store.addWispLabel(ctx, oldID, "test-label", "tester"); err != nil {
		t.Fatalf("failed to add wisp label: %v", err)
	}

	// Rename the wisp
	newID := "test-renamed-wisp"
	wisp.ID = newID
	if err := store.UpdateIssueID(ctx, oldID, newID, wisp, "tester"); err != nil {
		t.Fatalf("UpdateIssueID failed: %v", err)
	}

	// Verify the old ID no longer exists in wisps table
	if store.isActiveWisp(ctx, oldID) {
		t.Fatal("old wisp ID should not exist after rename")
	}

	// Verify the new ID exists in wisps table
	if !store.isActiveWisp(ctx, newID) {
		t.Fatal("new wisp ID should exist in wisps table after rename")
	}

	// Verify wisp_events were updated (1 creation + 1 label_added + 1 rename = 3)
	var newEventCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?`, newID).Scan(&newEventCount)
	if err != nil {
		t.Fatalf("failed to count wisp_events for new ID: %v", err)
	}
	if newEventCount != 3 {
		t.Fatalf("expected 3 wisp_events rows for new ID (creation + label_added + rename), got %d", newEventCount)
	}

	// Verify no events left under old ID
	var oldEventCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?`, oldID).Scan(&oldEventCount)
	if err != nil {
		t.Fatalf("failed to count wisp_events for old ID: %v", err)
	}
	if oldEventCount != 0 {
		t.Fatalf("expected 0 wisp_events rows for old ID, got %d", oldEventCount)
	}

	// Verify wisp_labels were updated
	var labelCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?`, newID).Scan(&labelCount)
	if err != nil {
		t.Fatalf("failed to count wisp_labels for new ID: %v", err)
	}
	if labelCount != 1 {
		t.Fatalf("expected 1 wisp_labels row for new ID, got %d", labelCount)
	}

	// Verify no labels left under old ID
	var oldLabelCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?`, oldID).Scan(&oldLabelCount)
	if err != nil {
		t.Fatalf("failed to count wisp_labels for old ID: %v", err)
	}
	if oldLabelCount != 0 {
		t.Fatalf("expected 0 wisp_labels rows for old ID, got %d", oldLabelCount)
	}
}

// TestUpdateIssueIDStillWorksForRegularIssues verifies that the refactored
// UpdateIssueID still correctly handles regular (non-wisp) issues.
func TestUpdateIssueIDStillWorksForRegularIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a regular issue
	issue := &types.Issue{
		ID:        "test-regular-1",
		Title:     "Regular issue to rename",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Verify it's not a wisp
	if store.isActiveWisp(ctx, issue.ID) {
		t.Fatal("regular issue should not be an active wisp")
	}

	// Rename it
	newID := "test-regular-renamed"
	issue.ID = newID
	if err := store.UpdateIssueID(ctx, "test-regular-1", newID, issue, "tester"); err != nil {
		t.Fatalf("UpdateIssueID failed: %v", err)
	}

	// Verify the old ID is gone and new ID exists
	got, err := store.GetIssue(ctx, newID)
	if err != nil {
		t.Fatalf("GetIssue failed for renamed issue: %v", err)
	}
	if got.Title != "Regular issue to rename" {
		t.Fatalf("expected original title, got %q", got.Title)
	}

	// Verify rename event in events table
	var eventCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = 'renamed'`, newID).Scan(&eventCount)
	if err != nil {
		t.Fatalf("failed to count rename events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 rename event for new ID, got %d", eventCount)
	}
}
