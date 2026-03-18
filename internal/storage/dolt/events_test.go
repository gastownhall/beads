package dolt

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetAllEventsSince_UnionBothTables verifies that GetAllEventsSince returns
// events from both the events table (permanent issues) and wisp_events table
// (ephemeral/wisp issues), ordered by created_at.
func TestGetAllEventsSince_UnionBothTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	since := time.Now().UTC().Add(-1 * time.Second)

	// Create a permanent issue (events go to 'events' table)
	perm := &types.Issue{
		ID:        "test-ev-perm",
		Title:     "Permanent Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, perm, "tester"); err != nil {
		t.Fatalf("failed to create permanent issue: %v", err)
	}

	// Create an ephemeral issue (events go to 'wisp_events' table)
	wisp := &types.Issue{
		ID:        "test-ev-wisp",
		Title:     "Wisp Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("failed to create wisp issue: %v", err)
	}

	// Query events since before both were created
	events, err := store.GetAllEventsSince(ctx, since)
	if err != nil {
		t.Fatalf("GetAllEventsSince failed: %v", err)
	}

	// Should have events from both tables (at least one 'created' event each)
	permFound, wispFound := false, false
	for _, e := range events {
		if e.IssueID == perm.ID {
			permFound = true
		}
		if e.IssueID == wisp.ID {
			wispFound = true
		}
	}
	if !permFound {
		t.Error("expected event from permanent issue (events table), not found")
	}
	if !wispFound {
		t.Error("expected event from wisp issue (wisp_events table), not found")
	}

	// Verify chronological ordering
	for i := 1; i < len(events); i++ {
		if events[i].CreatedAt.Before(events[i-1].CreatedAt) {
			t.Errorf("events not in chronological order: [%d] %v > [%d] %v",
				i-1, events[i-1].CreatedAt, i, events[i].CreatedAt)
		}
	}
}

// TestGetAllEventsSince_EmptyStore verifies that GetAllEventsSince returns an
// empty slice (not an error) when no events exist after the given time.
func TestGetAllEventsSince_EmptyStore(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	events, err := store.GetAllEventsSince(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events from empty store, got %d", len(events))
	}
}
