//go:build cgo

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestSingleIssueSnapshot tests that the snapshot captures status and update time.
func TestSingleIssueSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Now()
	issue := &types.Issue{
		ID:        "test-001",
		Status:    types.StatusOpen,
		UpdatedAt: now,
	}

	snap1 := singleIssueSnapshot(issue)
	expected := fmt.Sprintf("test-001:open:%d", now.UnixNano())
	if snap1 != expected {
		t.Errorf("snapshot = %q, want %q", snap1, expected)
	}

	// Changing status changes the snapshot
	issue.Status = types.StatusClosed
	snap2 := singleIssueSnapshot(issue)
	if snap1 == snap2 {
		t.Error("snapshot should change when status changes from open to closed")
	}

	// Changing UpdatedAt changes the snapshot
	issue.UpdatedAt = now.Add(time.Second)
	snap3 := singleIssueSnapshot(issue)
	if snap2 == snap3 {
		t.Error("snapshot should change when UpdatedAt changes")
	}
}

// TestWatchIssueFlags tests that watch flag is properly registered.
func TestWatchIssueFlags(t *testing.T) {
	flag := showCmd.Flags().Lookup("watch")
	if flag == nil {
		t.Fatal("watch flag should be registered in showCmd")
	}
	if flag.DefValue != "false" {
		t.Errorf("watch flag default should be 'false', got '%s'", flag.DefValue)
	}
}

// TestWatchIssueDetectsStatusChange is a regression test for the bug where
// bd show --watch used fsnotify (file watching) instead of polling. Dolt stores
// data in a server-side database, not files, so file watchers never fired and
// the display never updated — even when the underlying bead changed to closed.
//
// This test creates an issue, takes a snapshot, closes the issue, takes another
// snapshot, and verifies the watch loop would detect the change.
func TestWatchIssueDetectsStatusChange(t *testing.T) {
	t.Parallel()
	ensureTestMode(t)

	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "test.db")
	s := newTestStore(t, dbPath)

	// Create an open issue
	issue := &types.Issue{
		ID:        generateUniqueTestID(t, "test", 0),
		Title:     "watch regression test",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Fetch and snapshot the open issue
	got, err := s.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	snapBefore := singleIssueSnapshot(got)

	// Close the issue (simulates another agent finishing work)
	if err := s.CloseIssue(ctx, issue.ID, "done", "test-actor", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	// Fetch and snapshot the closed issue
	got, err = s.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue after close: %v", err)
	}
	snapAfter := singleIssueSnapshot(got)

	// The polling-based watch loop detects changes by comparing snapshots.
	// This must differ — the old fsnotify implementation would never see this
	// because Dolt writes don't produce filesystem events in .beads/.
	if snapBefore == snapAfter {
		t.Errorf("snapshot did not change after closing issue: before=%q after=%q", snapBefore, snapAfter)
	}
	if got.Status != types.StatusClosed {
		t.Errorf("issue status = %q, want %q", got.Status, types.StatusClosed)
	}
}

// TestWatchIssueDetectsFieldUpdate verifies that non-status field updates
// (e.g., title change) are also detected by the polling snapshot.
func TestWatchIssueDetectsFieldUpdate(t *testing.T) {
	t.Parallel()
	ensureTestMode(t)

	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "test.db")
	s := newTestStore(t, dbPath)

	issue := &types.Issue{
		ID:        generateUniqueTestID(t, "test", 0),
		Title:     "original title",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	got, err := s.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	snapBefore := singleIssueSnapshot(got)

	// Update title (which bumps UpdatedAt)
	if err := s.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": "updated title"}, "test-actor"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	got, err = s.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue after update: %v", err)
	}
	snapAfter := singleIssueSnapshot(got)

	if snapBefore == snapAfter {
		t.Errorf("snapshot did not change after title update: before=%q after=%q", snapBefore, snapAfter)
	}
}
