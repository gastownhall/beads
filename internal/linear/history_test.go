package linear

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
)

func TestBuildSyncRunFromResult(t *testing.T) {
	started := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	result := &tracker.SyncResult{
		Success: true,
		Stats: tracker.SyncStats{
			Created: 3,
			Updated: 2,
			Skipped: 5,
			Errors:  1,
		},
	}

	run := BuildSyncRunFromResult("run-123", started, "push", false, "timestamp", result)

	if run.SyncRunID != "run-123" {
		t.Errorf("SyncRunID = %q, want %q", run.SyncRunID, "run-123")
	}
	if run.Direction != "push" {
		t.Errorf("Direction = %q, want %q", run.Direction, "push")
	}
	if run.DryRun {
		t.Error("DryRun = true, want false")
	}
	if run.IssuesCreated != 3 {
		t.Errorf("IssuesCreated = %d, want 3", run.IssuesCreated)
	}
	if run.IssuesUpdated != 2 {
		t.Errorf("IssuesUpdated = %d, want 2", run.IssuesUpdated)
	}
	if run.IssuesSkipped != 5 {
		t.Errorf("IssuesSkipped = %d, want 5", run.IssuesSkipped)
	}
	if run.IssuesFailed != 1 {
		t.Errorf("IssuesFailed = %d, want 1", run.IssuesFailed)
	}
	if run.ConflictResolution != "timestamp" {
		t.Errorf("ConflictResolution = %q, want %q", run.ConflictResolution, "timestamp")
	}
	if !run.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", run.StartedAt, started)
	}
	if run.CompletedAt.Before(started) {
		t.Error("CompletedAt should be after StartedAt")
	}
}

func TestBuildSyncRunFromResult_WithError(t *testing.T) {
	result := &tracker.SyncResult{
		Success: false,
		Error:   "rate limit exceeded",
	}
	run := BuildSyncRunFromResult("run-err", time.Now().UTC(), "pull", false, "", result)
	if run.ErrorMessage != "rate limit exceeded" {
		t.Errorf("ErrorMessage = %q, want %q", run.ErrorMessage, "rate limit exceeded")
	}
}

func TestBuildSyncRunFromResult_NilResult(t *testing.T) {
	run := BuildSyncRunFromResult("run-nil", time.Now().UTC(), "both", true, "local", nil)
	if run.IssuesCreated != 0 {
		t.Errorf("IssuesCreated = %d, want 0", run.IssuesCreated)
	}
	if !run.DryRun {
		t.Error("DryRun = false, want true")
	}
}

func TestBuildSyncItemsFromResult(t *testing.T) {
	result := &tracker.SyncResult{
		PullStats: tracker.PullStats{
			Items: []tracker.SyncItemDetail{
				{BeadID: "bd-001", ExternalID: "TEAM-1", Direction: "pull", Outcome: "created", DurationMs: 100},
				{BeadID: "bd-002", ExternalID: "TEAM-2", Direction: "pull", Outcome: "updated", DurationMs: 50,
					BeforeValues: map[string]string{"title": "Old"}, AfterValues: map[string]string{"title": "New"}},
			},
		},
		PushStats: tracker.PushStats{
			Items: []tracker.SyncItemDetail{
				{BeadID: "bd-003", ExternalID: "TEAM-3", Direction: "push", Outcome: "created", DurationMs: 200},
				{BeadID: "bd-004", Direction: "push", Outcome: "failed", ErrorMsg: "timeout"},
			},
		},
	}

	items := BuildSyncItemsFromResult(result)
	if len(items) != 4 {
		t.Fatalf("len(items) = %d, want 4", len(items))
	}

	// Pull items
	if items[0].BeadID != "bd-001" || items[0].Outcome != "created" {
		t.Errorf("items[0] = %+v, want bead_id=bd-001 outcome=created", items[0])
	}
	if items[1].BeforeValues["title"] != "Old" || items[1].AfterValues["title"] != "New" {
		t.Errorf("items[1] before/after mismatch: before=%v after=%v", items[1].BeforeValues, items[1].AfterValues)
	}

	// Push items
	if items[2].BeadID != "bd-003" || items[2].DurationMs != 200 {
		t.Errorf("items[2] = %+v, want bead_id=bd-003 duration=200", items[2])
	}
	if items[3].ErrorMessage != "timeout" {
		t.Errorf("items[3].ErrorMessage = %q, want %q", items[3].ErrorMessage, "timeout")
	}
}

func TestBuildSyncItemsFromResult_Nil(t *testing.T) {
	items := BuildSyncItemsFromResult(nil)
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(items))
	}
}

func TestMarshalFieldValues(t *testing.T) {
	tests := []struct {
		name   string
		input  map[string]string
		wantOK bool
	}{
		{"nil map", nil, true},
		{"empty map", map[string]string{}, true},
		{"with values", map[string]string{"title": "Test", "status": "open"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := marshalFieldValues(tt.input)
			if (err == nil) != tt.wantOK {
				t.Errorf("marshalFieldValues() error = %v, wantOK = %v", err, tt.wantOK)
			}
			if result == "" {
				t.Error("marshalFieldValues() returned empty string")
			}
		})
	}
}

func TestUnmarshalFieldValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty string", "", 0},
		{"empty object", "{}", 0},
		{"with values", `{"title":"Test","status":"open"}`, 2},
		{"invalid json", "not json", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := unmarshalFieldValues(tt.input)
			if len(result) != tt.want {
				t.Errorf("unmarshalFieldValues(%q) len = %d, want %d", tt.input, len(result), tt.want)
			}
		})
	}
}
