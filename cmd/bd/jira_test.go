package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
)

func TestJiraSyncStats(t *testing.T) {
	stats := tracker.SyncStats{}

	if stats.Pulled != 0 {
		t.Errorf("expected Pulled to be 0, got %d", stats.Pulled)
	}
	if stats.Pushed != 0 {
		t.Errorf("expected Pushed to be 0, got %d", stats.Pushed)
	}
	if stats.Created != 0 {
		t.Errorf("expected Created to be 0, got %d", stats.Created)
	}
	if stats.Updated != 0 {
		t.Errorf("expected Updated to be 0, got %d", stats.Updated)
	}
	if stats.Skipped != 0 {
		t.Errorf("expected Skipped to be 0, got %d", stats.Skipped)
	}
	if stats.Errors != 0 {
		t.Errorf("expected Errors to be 0, got %d", stats.Errors)
	}
	if stats.Conflicts != 0 {
		t.Errorf("expected Conflicts to be 0, got %d", stats.Conflicts)
	}
}

func TestJiraSyncResult(t *testing.T) {
	result := tracker.SyncResult{
		Success: true,
		Stats: tracker.SyncStats{
			Created: 5,
			Updated: 3,
		},
		LastSync: "2025-01-15T10:30:00Z",
	}

	if !result.Success {
		t.Error("expected Success to be true")
	}
	if result.Stats.Created != 5 {
		t.Errorf("expected Created to be 5, got %d", result.Stats.Created)
	}
	if result.Stats.Updated != 3 {
		t.Errorf("expected Updated to be 3, got %d", result.Stats.Updated)
	}
	if result.LastSync != "2025-01-15T10:30:00Z" {
		t.Errorf("unexpected LastSync value: %s", result.LastSync)
	}
	if result.Error != "" {
		t.Errorf("expected Error to be empty, got %s", result.Error)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected Warnings to be empty, got %v", result.Warnings)
	}
}
