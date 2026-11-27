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

// newLinearDescriptionSyncFixture builds a one-issue sync harness whose
// Linear body and local row differ in the description fields, for the bd
// #8506 settle tests.
func newLinearDescriptionSyncFixture(t *testing.T, remoteTitle, body, localDescription, localNotes string) (*fakeLinearIssueAPI, *linearSyncStore, func() *tracker.SyncResult) {
	t.Helper()
	ctx := context.Background()
	t.Setenv("LINEAR_API_KEY", "test-api-key")
	t.Setenv("LINEAR_OAUTH_CLIENT_ID", "")
	t.Setenv("LINEAR_OAUTH_CLIENT_SECRET", "")

	const ref = "https://linear.app/team/issue/TEAM-1"
	now := time.Now().UTC()
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
			Title:       remoteTitle,
			Description: body,
			Priority:    3,
			State:       &linear.State{ID: "state-todo", Name: "Todo", Type: "unstarted"},
			CreatedAt:   now.Add(-time.Hour).Format(time.RFC3339),
			UpdatedAt:   now.Add(-10 * time.Minute).Format(time.RFC3339),
		},
	}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
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
			Description: localDescription,
			Notes:       localNotes,
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
	return api, store, runSync
}

// TestLinearSyncSettlesRemoteSectionEdit asserts the #8506 remote-wins
// rule: an edit made in Linear to a section the body still carries lands in
// the structured field, and the sync neither folds the body into the
// description nor echoes a push back.
func TestLinearSyncSettlesRemoteSectionEdit(t *testing.T) {
	api, store, runSync := newLinearDescriptionSyncFixture(t,
		"Ship it",
		"Body\n\n## Notes\n\nedited in Linear",
		"Body",
		"old notes")

	result := runSync()
	if result.PullStats.Updated != 1 {
		t.Fatalf("PullStats = %+v, want Updated=1 (the notes edit pulled)", result.PullStats)
	}
	if store.issue.Description != "Body" {
		t.Fatalf("local description = %q, want %q (the body must not fold in)", store.issue.Description, "Body")
	}
	if store.issue.Notes != "edited in Linear" {
		t.Fatalf("local notes = %q, want %q (the remote section edit must win)", store.issue.Notes, "edited in Linear")
	}
	// The remote is the authority for the section it carries, so the push
	// has nothing to send.
	if _, updates := api.snapshot(); updates != 0 {
		t.Fatalf("issueUpdate calls = %d, want 0 (nothing to echo back)", updates)
	}
}

// TestLinearSyncKeepsTemplateHeadingDescription asserts the #8506
// unchanged-shape rule: a local description that itself ends in a
// section-shaped heading pushes to exactly the body Linear stores, so a
// remote title change must update the title without reshaping the fields —
// the heading text stays in the description, not in notes.
func TestLinearSyncKeepsTemplateHeadingDescription(t *testing.T) {
	marker := linear.GenerateIdempotencyMarker("bd-template", "a@b.c", 11)
	api, store, runSync := newLinearDescriptionSyncFixture(t,
		"Renamed in Linear",
		"Intro\n\n## Notes\n\ntemplate text\n"+marker+"\n",
		"Intro\n\n## Notes\ntemplate text",
		"")

	result := runSync()
	if result.PullStats.Updated != 1 {
		t.Fatalf("PullStats = %+v, want Updated=1 (the title edit pulled)", result.PullStats)
	}
	if store.issue.Title != "Renamed in Linear" {
		t.Fatalf("local title = %q, want the remote title", store.issue.Title)
	}
	if store.issue.Description != "Intro\n\n## Notes\ntemplate text" {
		t.Fatalf("local description = %q, want the template heading text kept in the description", store.issue.Description)
	}
	if store.issue.Notes != "" {
		t.Fatalf("local notes = %q, want empty (the heading text was never a notes field)", store.issue.Notes)
	}
	if _, updates := api.snapshot(); updates != 0 {
		t.Fatalf("issueUpdate calls = %d, want 0 (nothing local was kept)", updates)
	}
}

// TestLinearSyncKeepsLocallyDeletedSectionText asserts the #8506 keep rule:
// when the remote deletes a section and its text outright, the pull keeps
// the local text and the push restores the section exactly once.
func TestLinearSyncKeepsLocallyDeletedSectionText(t *testing.T) {
	api, store, runSync := newLinearDescriptionSyncFixture(t,
		"Ship it",
		"Body",
		"Body",
		"N1")

	runSync()
	if store.issue.Description != "Body" || store.issue.Notes != "N1" {
		t.Fatalf("after first sync: description=%q notes=%q, want Body/N1 (deleted text kept locally)", store.issue.Description, store.issue.Notes)
	}
	remote, updates := api.snapshot()
	if updates != 1 || !strings.Contains(remote.Description, "## Notes") || !strings.Contains(remote.Description, "N1") {
		t.Fatalf("first sync: issueUpdate calls = %d, remote body %q, want the notes section restored once", updates, remote.Description)
	}

	// The restored section is now what both sides hold: nothing more moves.
	runSync()
	if _, updates := api.snapshot(); updates != 1 {
		t.Fatalf("second sync: issueUpdate calls = %d, want 1 (the restore must not repeat)", updates)
	}
	if store.issue.Description != "Body" || store.issue.Notes != "N1" {
		t.Fatalf("second sync: description=%q notes=%q, want Body/N1 stable", store.issue.Description, store.issue.Notes)
	}
}

// TestLinearSyncClearsSectionMovedIntoBody asserts the #8506 clear rule:
// when the remote deletes the section heading but keeps its text in the
// body, the pull moves the text into the description, clears the field and
// pushes nothing back.
func TestLinearSyncClearsSectionMovedIntoBody(t *testing.T) {
	api, store, runSync := newLinearDescriptionSyncFixture(t,
		"Ship it",
		"Body\n\nN1",
		"Body",
		"N1")

	result := runSync()
	if result.PullStats.Updated != 1 {
		t.Fatalf("PullStats = %+v, want Updated=1 (the field clears)", result.PullStats)
	}
	if store.issue.Description != "Body\n\nN1" {
		t.Fatalf("local description = %q, want the moved text kept in the body", store.issue.Description)
	}
	if store.issue.Notes != "" {
		t.Fatalf("local notes = %q, want empty (the text lives in the body now)", store.issue.Notes)
	}
	if _, updates := api.snapshot(); updates != 0 {
		t.Fatalf("issueUpdate calls = %d, want 0 (the remote's decision stands)", updates)
	}
}
