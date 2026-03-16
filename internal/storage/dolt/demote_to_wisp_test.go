package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDemoteToWisp_NoHistory verifies that UpdateIssue with no_history=true
// migrates the issue from the issues table to the wisps table. (be-x4l)
func TestDemoteToWisp_NoHistory(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a regular (versioned) issue.
	issue := &types.Issue{
		Title:     "will become no-history",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	id := issue.ID

	// Verify it lives in the issues table before demotion.
	if store.isActiveWisp(ctx, id) {
		t.Fatalf("issue %s should NOT be in wisps table before demotion", id)
	}

	// Apply --no-history via UpdateIssue.
	if err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"no_history": true,
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue with no_history=true: %v", err)
	}

	// The issue must now live in the wisps table.
	if !store.isActiveWisp(ctx, id) {
		t.Errorf("issue %s should be in wisps table after no_history update, but is not", id)
	}

	// Verify it is gone from the issues table.
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&count); err != nil {
		t.Fatalf("query issues table: %v", err)
	}
	if count != 0 {
		t.Errorf("issue %s still present in issues table after demotion (count=%d)", id, count)
	}

	// Verify the wisp has the NoHistory flag set.
	wisp, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue after demotion: %v", err)
	}
	if !wisp.NoHistory {
		t.Errorf("wisp %s: NoHistory should be true after demotion", id)
	}

	// Verify title is preserved.
	if wisp.Title != issue.Title {
		t.Errorf("wisp %s: title mismatch: got %q, want %q", id, wisp.Title, issue.Title)
	}
}

// TestDemoteToWisp_Ephemeral verifies that UpdateIssue with wisp=true
// migrates the issue from the issues table to the wisps table. (be-x4l)
func TestDemoteToWisp_Ephemeral(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a regular (versioned) issue.
	issue := &types.Issue{
		Title:     "will become ephemeral",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	id := issue.ID

	// Apply --ephemeral via UpdateIssue (wisp=true).
	if err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"wisp": true,
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue with wisp=true: %v", err)
	}

	// The issue must now live in the wisps table.
	if !store.isActiveWisp(ctx, id) {
		t.Errorf("issue %s should be in wisps table after wisp update, but is not", id)
	}

	// Verify the wisp has Ephemeral set.
	wisp, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue after demotion: %v", err)
	}
	if !wisp.Ephemeral {
		t.Errorf("wisp %s: Ephemeral should be true after demotion", id)
	}
}

// TestDemoteToWisp_FieldUpdatesApplied verifies that other field updates
// (e.g., title, status) are applied alongside the no_history migration. (be-x4l)
func TestDemoteToWisp_FieldUpdatesApplied(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		Title:     "original title",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	id := issue.ID

	// Demote with simultaneous field update.
	if err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"no_history": true,
		"title":      "updated title",
		"status":     "in_progress",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	wisp, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if wisp.Title != "updated title" {
		t.Errorf("title: got %q, want %q", wisp.Title, "updated title")
	}
	if wisp.Status != "in_progress" {
		t.Errorf("status: got %q, want %q", wisp.Status, "in_progress")
	}
	if !wisp.NoHistory {
		t.Error("NoHistory should be true")
	}
}

// TestDemoteToWisp_LabelsPreserved verifies that labels are migrated
// from the permanent labels table to wisp_labels during demotion. (be-x4l)
func TestDemoteToWisp_LabelsPreserved(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		Title:     "labeled issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	id := issue.ID

	// Add labels before demotion.
	if err := store.AddLabel(ctx, id, "priority-hot", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if err := store.AddLabel(ctx, id, "review", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}

	// Demote to wisp.
	if err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"no_history": true,
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	// Verify labels appear on the wisp.
	wisp, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	labelSet := make(map[string]bool, len(wisp.Labels))
	for _, l := range wisp.Labels {
		labelSet[l] = true
	}
	for _, want := range []string{"priority-hot", "review"} {
		if !labelSet[want] {
			t.Errorf("label %q missing from wisp after demotion; labels=%v", want, wisp.Labels)
		}
	}
}

// TestDemoteToWisp_AlreadyInWisps verifies that calling UpdateIssue with
// no_history=true on a wisp (already in wisps table) does not error and
// behaves as a normal wisp update. (be-x4l)
func TestDemoteToWisp_AlreadyInWisps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create an already-ephemeral issue.
	issue := &types.Issue{
		Title:     "already a wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	id := issue.ID

	// Calling UpdateIssue with wisp=true on an existing wisp should not error.
	if err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"wisp": true,
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue on existing wisp: %v", err)
	}

	// Should still be a wisp.
	if !store.isActiveWisp(ctx, id) {
		t.Errorf("issue %s should still be a wisp after redundant wisp=true update", id)
	}
}
