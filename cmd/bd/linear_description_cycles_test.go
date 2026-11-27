package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// TestLinearSyncDoesNotCompoundDescriptionSections pins the #8506 cycle end
// to end. This test is deliberately self-contained (no fixture helpers from
// linear_description_sync_test.go) so it also builds against the unfixed
// tree, where it fails.
//
// A body Linear stores — the pushed sections with a blank line after each
// heading and the idempotency marker kept — must sync as no change: the
// local fields stay split, the remote body keeps exactly one heading per
// section, and no cycle sends an issueUpdate. Before the fix, each cycle
// copied the stored body into the local description and pushed the folded
// copy back with a fresh set of sections, compounding on every sync.
func TestLinearSyncDoesNotCompoundDescriptionSections(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LINEAR_API_KEY", "test-api-key")
	t.Setenv("LINEAR_OAUTH_CLIENT_ID", "")
	t.Setenv("LINEAR_OAUTH_CLIENT_SECRET", "")

	const ref = "https://linear.app/team/issue/TEAM-1"
	now := time.Now().UTC()
	marker := linear.GenerateIdempotencyMarker("bd-compound", "a@b.c", 7)
	// What Linear stores after the create that linked this issue: the pushed
	// body with a blank line after the heading and the marker kept.
	body := "Body\n\n## Notes\n\nold notes\n" + marker + "\n"
	api := &fakeLinearIssueAPI{
		states: []linear.State{
			{ID: "state-todo", Name: "Todo", Type: "unstarted"},
			{ID: "state-progress", Name: "In Progress", Type: "started"},
			{ID: "state-blocked", Name: "Blocked", Type: "started"},
			{ID: "state-done", Name: "Done", Type: "completed"},
		},
		issue: linear.Issue{
			ID:          "uuid-1",
			Identifier:  "TEAM-1",
			URL:         ref,
			Title:       "Ship it",
			Description: body,
			Priority:    3,
			State:       &linear.State{ID: "state-todo", Name: "Todo", Type: "unstarted"},
			CreatedAt:   now.Add(-time.Hour).Format(time.RFC3339),
			UpdatedAt:   now.Add(-10 * time.Minute).Format(time.RFC3339),
		},
	}
	server := httptest.NewServer(api)
	defer server.Close()
	setupEndpointTrustWorkspaceForTest(t)
	t.Setenv("LINEAR_API_ENDPOINT", server.URL)

	linkedRef := ref
	store := &linearSyncStore{
		config: map[string]string{
			"linear.team_id":               "team-1",
			"linear.api_endpoint":          server.URL,
			"linear.state_map.todo":        "open",
			"linear.state_map.in progress": "in_progress",
			"linear.state_map.blocked":     "blocked",
			"linear.state_map.done":        "closed",
		},
		metadata: map[string]string{"linear.last_sync": now.Add(-30 * time.Minute).Format(time.RFC3339)},
		issue: types.Issue{
			ID:          "bd-1",
			Title:       "Ship it",
			Description: "Body",
			Notes:       "old notes",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			ExternalRef: &linkedRef,
			CreatedAt:   now.Add(-time.Hour),
			UpdatedAt:   now.Add(-40 * time.Minute),
		},
	}

	lt := &linear.Tracker{}
	lt.SetTeamIDs([]string{"team-1"})
	if err := lt.Init(ctx, store); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := lt.ValidatePushStateMappings(ctx); err != nil {
		t.Fatalf("ValidatePushStateMappings: %v", err)
	}
	runSync := func() *tracker.SyncResult {
		t.Helper()
		engine := tracker.NewEngine(lt, store, "test")
		engine.PushHooks = buildLinearPushHooksForStore(ctx, store, lt, false)
		result, err := engine.Sync(ctx, tracker.SyncOptions{Pull: true, Push: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		return result
	}

	for cycle := 1; cycle <= 3; cycle++ {
		runSync()
		if store.issue.Description != "Body" {
			t.Fatalf("cycle %d: local description = %q, want %q (the stored body must not fold in)", cycle, store.issue.Description, "Body")
		}
		if store.issue.Notes != "old notes" {
			t.Fatalf("cycle %d: local notes = %q, want %q", cycle, store.issue.Notes, "old notes")
		}
	}

	remote, updates := api.snapshot()
	if n := strings.Count(remote.Description, "## Notes"); n != 1 {
		t.Fatalf("remote body carries %d \"## Notes\" headings, want 1: %q", n, remote.Description)
	}
	if !strings.Contains(remote.Description, marker) {
		t.Fatalf("remote body lost its idempotency marker: %q", remote.Description)
	}
	if updates != 0 {
		t.Fatalf("issueUpdate calls = %d, want 0 (no cycle had anything to send)", updates)
	}
}
