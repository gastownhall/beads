//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestResolveCurrentIssueID_InProgress(t *testing.T) {
	ctx := context.Background()
	st := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	// Create an in-progress issue assigned to "tester"
	issue := &types.Issue{
		Title:     "In-progress task",
		Status:    types.StatusInProgress,
		Priority:  2,
		IssueType: types.TypeTask,
		Assignee:  "tester",
	}
	if err := st.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Set globals for the test
	oldStore := store
	oldActor := actor
	store = st
	actor = "tester"
	defer func() {
		store = oldStore
		actor = oldActor
	}()

	got := resolveCurrentIssueID(ctx)
	if got != issue.ID {
		t.Errorf("resolveCurrentIssueID() = %q, want %q", got, issue.ID)
	}
}

func TestResolveCurrentIssueID_Hooked(t *testing.T) {
	ctx := context.Background()
	st := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	// Create a hooked issue assigned to "tester"
	issue := &types.Issue{
		Title:     "Hooked task",
		Status:    types.StatusHooked,
		Priority:  2,
		IssueType: types.TypeTask,
		Assignee:  "tester",
	}
	if err := st.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	oldStore := store
	oldActor := actor
	store = st
	actor = "tester"
	defer func() {
		store = oldStore
		actor = oldActor
	}()

	got := resolveCurrentIssueID(ctx)
	if got != issue.ID {
		t.Errorf("resolveCurrentIssueID() = %q, want %q", got, issue.ID)
	}
}

func TestResolveCurrentIssueID_FallsBackToLastTouched(t *testing.T) {
	ctx := context.Background()
	st := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	// No in-progress or hooked issues — should fall back to last touched
	oldStore := store
	oldActor := actor
	store = st
	actor = "tester"
	defer func() {
		store = oldStore
		actor = oldActor
	}()

	// Point BEADS_DIR at a temp dir so GetLastTouchedID doesn't find the real file.
	// Create metadata.json so FindBeadsDir accepts this as a valid beads dir.
	tmpBeads := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(tmpBeads, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpBeads, "metadata.json"), []byte("{}"), 0600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	t.Setenv("BEADS_DIR", tmpBeads)

	// With no last-touched file, should return empty
	got := resolveCurrentIssueID(ctx)
	if got != "" {
		t.Errorf("resolveCurrentIssueID() = %q, want empty string", got)
	}
}

func TestResolveCurrentIssueID_InProgressBeforeHooked(t *testing.T) {
	ctx := context.Background()
	st := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	// Create both hooked and in-progress issues
	hooked := &types.Issue{
		Title:     "Hooked task",
		Status:    types.StatusHooked,
		Priority:  2,
		IssueType: types.TypeTask,
		Assignee:  "tester",
	}
	if err := st.CreateIssue(ctx, hooked, "tester"); err != nil {
		t.Fatalf("CreateIssue(hooked): %v", err)
	}

	inProgress := &types.Issue{
		Title:     "In-progress task",
		Status:    types.StatusInProgress,
		Priority:  2,
		IssueType: types.TypeTask,
		Assignee:  "tester",
	}
	if err := st.CreateIssue(ctx, inProgress, "tester"); err != nil {
		t.Fatalf("CreateIssue(in_progress): %v", err)
	}

	oldStore := store
	oldActor := actor
	store = st
	actor = "tester"
	defer func() {
		store = oldStore
		actor = oldActor
	}()

	// Should prefer in-progress over hooked
	got := resolveCurrentIssueID(ctx)
	if got != inProgress.ID {
		t.Errorf("resolveCurrentIssueID() = %q, want %q (in-progress should take priority)", got, inProgress.ID)
	}
}

func TestResolveCurrentIssueID_NilStore(t *testing.T) {
	// When store is nil, should fall back to last-touched
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	// Point BEADS_DIR at a temp dir so GetLastTouchedID doesn't find the real file.
	// Create metadata.json so FindBeadsDir accepts this as a valid beads dir.
	tmpBeads := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(tmpBeads, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpBeads, "metadata.json"), []byte("{}"), 0600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	t.Setenv("BEADS_DIR", tmpBeads)

	// With no last-touched file, last-touched returns ""
	got := resolveCurrentIssueID(context.Background())
	if got != "" {
		t.Errorf("resolveCurrentIssueID() with nil store = %q, want empty", got)
	}
}
