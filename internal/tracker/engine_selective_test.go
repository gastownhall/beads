//go:build cgo

package tracker

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestEnginePushWithIssueIDsFilter(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	issues := []*types.Issue{
		{ID: "bd-sel-1", Title: "Push me", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
		{ID: "bd-sel-2", Title: "Also push me", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
		{ID: "bd-sel-3", Title: "Skip me", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue(%s) error: %v", issue.ID, err)
		}
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{
		Push:     true,
		IssueIDs: []string{"bd-sel-1", "bd-sel-2"},
	})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.PushStats.Created != 2 {
		t.Errorf("PushStats.Created = %d, want 2", result.PushStats.Created)
	}
	if len(tracker.created) != 2 {
		t.Errorf("tracker.created = %d, want 2", len(tracker.created))
	}
	// Verify only the selected issues were pushed
	createdIDs := make(map[string]bool)
	for _, c := range tracker.created {
		createdIDs[c.ID] = true
	}
	if !createdIDs["bd-sel-1"] || !createdIDs["bd-sel-2"] {
		t.Errorf("expected bd-sel-1 and bd-sel-2 to be pushed, got %v", createdIDs)
	}
	if createdIDs["bd-sel-3"] {
		t.Errorf("bd-sel-3 should not have been pushed")
	}
}

func TestEnginePullWithIssueIDsSelectiveByExternalRef(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{ID: "ext-1", Identifier: "EXT-1", URL: "https://test.test/EXT-1", Title: "Issue 1", Description: "Desc 1"},
		{ID: "ext-2", Identifier: "EXT-2", URL: "https://test.test/EXT-2", Title: "Issue 2", Description: "Desc 2"},
		{ID: "ext-3", Identifier: "EXT-3", URL: "https://test.test/EXT-3", Title: "Issue 3", Description: "Desc 3"},
	}

	engine := NewEngine(tracker, store, "test-actor")

	// Pull only EXT-1 using external ref identifier
	result, err := engine.Sync(ctx, SyncOptions{
		Pull:     true,
		IssueIDs: []string{"EXT-1"},
	})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.PullStats.Created != 1 {
		t.Errorf("PullStats.Created = %d, want 1", result.PullStats.Created)
	}
	if result.PullStats.Candidates != 1 {
		t.Errorf("PullStats.Candidates = %d, want 1", result.PullStats.Candidates)
	}
}

func TestEnginePullWithIssueIDsSelectiveByBeadID(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	// Set up the issue_prefix config
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("SetConfig error: %v", err)
	}

	// Create a local issue with an external ref
	extRef := "https://test.test/EXT-1"
	issue := &types.Issue{
		ID:          "bd-pullsel",
		Title:       "Old title",
		Description: "Old description",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: &extRef,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{ID: "ext-1", Identifier: "EXT-1", URL: "https://test.test/EXT-1", Title: "New title", Description: "New description"},
		{ID: "ext-2", Identifier: "EXT-2", URL: "https://test.test/EXT-2", Title: "Other issue", Description: "Other desc"},
	}

	engine := NewEngine(tracker, store, "test-actor")

	// Pull by bead ID — should resolve to EXT-1 via external_ref
	result, err := engine.Sync(ctx, SyncOptions{
		Pull:     true,
		IssueIDs: []string{"bd-pullsel"},
	})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.PullStats.Updated != 1 {
		t.Errorf("PullStats.Updated = %d, want 1", result.PullStats.Updated)
	}
	if result.PullStats.Candidates != 1 {
		t.Errorf("PullStats.Candidates = %d, want 1 (only the requested issue)", result.PullStats.Candidates)
	}

	// Verify the issue was updated
	updated, err := store.GetIssue(ctx, "bd-pullsel")
	if err != nil {
		t.Fatalf("GetIssue error: %v", err)
	}
	if updated.Title != "New title" {
		t.Errorf("Title = %q, want %q", updated.Title, "New title")
	}
}

func TestEngineSyncEmptyIssueIDsBehavesAsNormal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	issues := []*types.Issue{
		{ID: "bd-all-1", Title: "Issue 1", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
		{ID: "bd-all-2", Title: "Issue 2", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue(%s) error: %v", issue.ID, err)
		}
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")

	// Empty IssueIDs should push all issues
	result, err := engine.Sync(ctx, SyncOptions{
		Push:     true,
		IssueIDs: nil,
	})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.PushStats.Created != 2 {
		t.Errorf("PushStats.Created = %d, want 2 (all issues)", result.PushStats.Created)
	}
}

func TestEnginePushWithInvalidIssueIDs(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	issue := &types.Issue{
		ID:        "bd-valid",
		Title:     "Valid issue",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")

	// Mix of valid and invalid IDs — only bd-valid exists
	result, err := engine.Sync(ctx, SyncOptions{
		Push:     true,
		IssueIDs: []string{"bd-valid", "bd-nonexistent"},
	})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.PushStats.Created != 1 {
		t.Errorf("PushStats.Created = %d, want 1 (only bd-valid)", result.PushStats.Created)
	}
	if len(tracker.created) != 1 {
		t.Errorf("tracker.created = %d, want 1", len(tracker.created))
	}
}

func TestEnginePullWithIssueIDsNotFound(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	tracker := newMockTracker("test")
	// No issues in tracker
	tracker.issues = nil

	engine := NewEngine(tracker, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{
		Pull:     true,
		IssueIDs: []string{"NONEXISTENT-1"},
	})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.PullStats.Created != 0 {
		t.Errorf("PullStats.Created = %d, want 0", result.PullStats.Created)
	}
	if result.PullStats.Skipped != 1 {
		t.Errorf("PullStats.Skipped = %d, want 1 (not found)", result.PullStats.Skipped)
	}
}

func TestEnginePullWithBeadIDNoExternalRef(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("SetConfig error: %v", err)
	}

	// Create a local issue WITHOUT external ref
	issue := &types.Issue{
		ID:        "bd-noref",
		Title:     "No external ref",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{
		Pull:     true,
		IssueIDs: []string{"bd-noref"},
	})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.PullStats.Skipped != 1 {
		t.Errorf("PullStats.Skipped = %d, want 1 (no external ref)", result.PullStats.Skipped)
	}
}

func TestIsBeadID(t *testing.T) {
	tests := []struct {
		id     string
		prefix string
		want   bool
	}{
		{"bd-123", "bd", true},
		{"bd-abc-def", "bd", true},
		{"EXT-1", "bd", false},
		{"https://test.test/EXT-1", "bd", false},
		{"", "bd", false},
		{"bd-123", "", false},
		{"proj-42", "proj", true},
		{"bd123", "bd", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s", tt.id, tt.prefix), func(t *testing.T) {
			if got := isBeadID(tt.id, tt.prefix); got != tt.want {
				t.Errorf("isBeadID(%q, %q) = %v, want %v", tt.id, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestBuildIssueIDSet(t *testing.T) {
	// nil input
	if got := buildIssueIDSet(nil); got != nil {
		t.Errorf("buildIssueIDSet(nil) = %v, want nil", got)
	}

	// empty input
	if got := buildIssueIDSet([]string{}); got != nil {
		t.Errorf("buildIssueIDSet([]) = %v, want nil", got)
	}

	// normal input
	set := buildIssueIDSet([]string{"a", "b", "c"})
	if len(set) != 3 || !set["a"] || !set["b"] || !set["c"] {
		t.Errorf("buildIssueIDSet([a,b,c]) = %v", set)
	}
}
