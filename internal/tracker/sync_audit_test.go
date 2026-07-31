package tracker

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestSnapshotIssueFields(t *testing.T) {
	extRef := "https://linear.app/team/issue/TEAM-123"
	issue := &types.Issue{
		Title:       "Fix bug",
		Description: "Something is broken",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		Assignee:    "alice",
		ExternalRef: &extRef,
	}

	snapshot := snapshotIssueFields(issue)
	if snapshot["title"] != "Fix bug" {
		t.Errorf("title = %q, want %q", snapshot["title"], "Fix bug")
	}
	if snapshot["status"] != "open" {
		t.Errorf("status = %q, want %q", snapshot["status"], "open")
	}
	if snapshot["priority"] != "1" {
		t.Errorf("priority = %q, want %q", snapshot["priority"], "1")
	}
	if snapshot["assignee"] != "alice" {
		t.Errorf("assignee = %q, want %q", snapshot["assignee"], "alice")
	}
	if snapshot["external_ref"] != extRef {
		t.Errorf("external_ref = %q, want %q", snapshot["external_ref"], extRef)
	}
}

func TestSnapshotIssueFields_Nil(t *testing.T) {
	snapshot := snapshotIssueFields(nil)
	if snapshot != nil {
		t.Errorf("expected nil snapshot for nil issue, got %v", snapshot)
	}
}

func TestSnapshotIssueFields_LongDescription(t *testing.T) {
	longDesc := ""
	for i := 0; i < 300; i++ {
		longDesc += "x"
	}
	issue := &types.Issue{
		Title:       "Test",
		Description: longDesc,
		Status:      types.StatusOpen,
	}
	snapshot := snapshotIssueFields(issue)
	if len(snapshot["description"]) > 204 {
		t.Errorf("description should be truncated, got len=%d", len(snapshot["description"]))
	}
}

func TestBatchPushResultToItems(t *testing.T) {
	result := &BatchPushResult{
		Created: []BatchPushItem{
			{LocalID: "bd-001", ExternalRef: "https://linear.app/team/issue/TEAM-1"},
		},
		Updated: []BatchPushItem{
			{LocalID: "bd-002", ExternalRef: "https://linear.app/team/issue/TEAM-2"},
		},
		Skipped: []string{"bd-003"},
		Errors: []BatchPushError{
			{LocalID: "bd-004", Message: "API error"},
		},
	}

	items := batchPushResultToItems(result)
	if len(items) != 4 {
		t.Fatalf("len(items) = %d, want 4", len(items))
	}

	if items[0].Outcome != OutcomeCreated || items[0].BeadID != "bd-001" {
		t.Errorf("items[0] = %+v, want created/bd-001", items[0])
	}
	if items[1].Outcome != OutcomeUpdated || items[1].BeadID != "bd-002" {
		t.Errorf("items[1] = %+v, want updated/bd-002", items[1])
	}
	if items[2].Outcome != OutcomeSkipped || items[2].BeadID != "bd-003" {
		t.Errorf("items[2] = %+v, want skipped/bd-003", items[2])
	}
	if items[3].Outcome != OutcomeFailed || items[3].ErrorMsg != "API error" {
		t.Errorf("items[3] = %+v, want failed/API error", items[3])
	}

	for _, item := range items {
		if item.Direction != "push" {
			t.Errorf("item %s direction = %q, want %q", item.BeadID, item.Direction, "push")
		}
	}
}

func TestBatchPushResultToItems_Nil(t *testing.T) {
	items := batchPushResultToItems(nil)
	if items != nil {
		t.Errorf("expected nil for nil result, got %v", items)
	}
}

func TestBatchPushResultToItems_Empty(t *testing.T) {
	items := batchPushResultToItems(&BatchPushResult{})
	if len(items) != 0 {
		t.Errorf("expected empty items for empty result, got %d", len(items))
	}
}
