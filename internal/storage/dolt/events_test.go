package dolt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func newVersionedTestIssue(id, title string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
}

func stubVersionedWriteFailure(t *testing.T, store *DoltStore) {
	t.Helper()

	store.commitVersionedWriteFn = func(context.Context, []string, string) error {
		return errors.New("history finalize failed")
	}
	t.Cleanup(func() {
		store.commitVersionedWriteFn = nil
	})
}

func requirePartialWriteError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected partial write error")
	}
	var partialErr *PartialWriteError
	if !errors.As(err, &partialErr) {
		t.Fatalf("expected PartialWriteError, got %T", err)
	}
}

func requireHeadChanged(t *testing.T, before, after string, action string) {
	t.Helper()

	if before == after {
		t.Fatalf("expected %s to advance Dolt HEAD", action)
	}
}

func requireHeadUnchanged(t *testing.T, before, after string, action string) {
	t.Helper()

	if before != after {
		t.Fatalf("expected %s to leave Dolt HEAD unchanged", action)
	}
}

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

func TestAddComment_CommitsPermanentIssueHistory(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := newVersionedTestIssue("comment-history-perm", "Permanent comment history")
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	before, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit before add: %v", err)
	}
	if err := store.AddComment(ctx, issue.ID, "tester", "hello history"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	after, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit after add: %v", err)
	}
	requireHeadChanged(t, before, after, "AddComment on permanent issue")
}

func TestAddComment_PartialWriteLeavesCommittedSQLState(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := newVersionedTestIssue("comment-partial", "Comment partial failure")
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	beforeHead, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit before add: %v", err)
	}
	beforeEvents, err := store.GetEvents(ctx, issue.ID, 20)
	if err != nil {
		t.Fatalf("GetEvents before add: %v", err)
	}

	stubVersionedWriteFailure(t, store)

	err = store.AddComment(ctx, issue.ID, "tester", "hello partial")
	requirePartialWriteError(t, err)

	afterHead, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit after add: %v", err)
	}
	requireHeadUnchanged(t, beforeHead, afterHead, "partial AddComment")

	afterEvents, err := store.GetEvents(ctx, issue.ID, 20)
	if err != nil {
		t.Fatalf("GetEvents after add: %v", err)
	}
	if len(afterEvents) != len(beforeEvents)+1 {
		t.Fatalf("expected SQL event row to persist despite partial failure, before=%d after=%d", len(beforeEvents), len(afterEvents))
	}
}

func TestAddComment_RetryAfterPartialWriteDuplicatesLogicalWrite(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := newVersionedTestIssue("comment-partial-retry", "Comment partial retry")
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	beforeEvents, err := store.GetEvents(ctx, issue.ID, 20)
	if err != nil {
		t.Fatalf("GetEvents before retry: %v", err)
	}

	stubVersionedWriteFailure(t, store)

	err = store.AddComment(ctx, issue.ID, "tester", "retry me")
	requirePartialWriteError(t, err)
	store.commitVersionedWriteFn = nil

	if err := store.AddComment(ctx, issue.ID, "tester", "retry me"); err != nil {
		t.Fatalf("second AddComment: %v", err)
	}

	afterEvents, err := store.GetEvents(ctx, issue.ID, 20)
	if err != nil {
		t.Fatalf("GetEvents after retry: %v", err)
	}
	if len(afterEvents) != len(beforeEvents)+2 {
		t.Fatalf("expected blind retry to append a second comment event, before=%d after=%d", len(beforeEvents), len(afterEvents))
	}
}

func TestImportIssueComment_HistoryBehaviorAndPartialFailure(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	permanent := newVersionedTestIssue("import-comment-perm", "Permanent imported comment")
	wisp := &types.Issue{
		ID:        "import-comment-wisp",
		Title:     "Wisp imported comment",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	for _, issue := range []*types.Issue{permanent, wisp} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}

	beforePerm, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit before permanent import: %v", err)
	}
	if _, err := store.ImportIssueComment(ctx, permanent.ID, "tester", "perm", time.Now().UTC()); err != nil {
		t.Fatalf("ImportIssueComment permanent: %v", err)
	}
	afterPerm, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit after permanent import: %v", err)
	}
	requireHeadChanged(t, beforePerm, afterPerm, "permanent ImportIssueComment")

	beforeWisp, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit before wisp import: %v", err)
	}
	if _, err := store.ImportIssueComment(ctx, wisp.ID, "tester", "wisp", time.Now().UTC()); err != nil {
		t.Fatalf("ImportIssueComment wisp: %v", err)
	}
	afterWisp, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit after wisp import: %v", err)
	}
	requireHeadUnchanged(t, beforeWisp, afterWisp, "wisp ImportIssueComment")

	beforePartialHead, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit before partial import: %v", err)
	}
	beforeComments, err := store.GetIssueComments(ctx, permanent.ID)
	if err != nil {
		t.Fatalf("GetIssueComments before partial import: %v", err)
	}

	stubVersionedWriteFailure(t, store)

	_, err = store.ImportIssueComment(ctx, permanent.ID, "tester", "perm-partial", time.Now().UTC())
	requirePartialWriteError(t, err)

	afterPartialHead, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit after partial import: %v", err)
	}
	requireHeadUnchanged(t, beforePartialHead, afterPartialHead, "partial ImportIssueComment")

	afterComments, err := store.GetIssueComments(ctx, permanent.ID)
	if err != nil {
		t.Fatalf("GetIssueComments after partial import: %v", err)
	}
	if len(afterComments) != len(beforeComments)+1 {
		t.Fatalf("expected SQL comment row to persist despite partial failure, before=%d after=%d", len(beforeComments), len(afterComments))
	}
}

func TestImportIssueComment_RetryAfterPartialWriteDuplicatesLogicalWrite(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := newVersionedTestIssue("import-comment-retry", "Import retry")
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	beforeComments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssueComments before retry: %v", err)
	}

	stubVersionedWriteFailure(t, store)

	createdAt := time.Now().UTC()
	if _, err := store.ImportIssueComment(ctx, issue.ID, "tester", "retry me", createdAt); err == nil {
		t.Fatal("expected partial write error")
	} else {
		requirePartialWriteError(t, err)
	}
	store.commitVersionedWriteFn = nil

	if _, err := store.ImportIssueComment(ctx, issue.ID, "tester", "retry me", createdAt); err != nil {
		t.Fatalf("second ImportIssueComment: %v", err)
	}

	afterComments, err := store.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssueComments after retry: %v", err)
	}
	if len(afterComments) != len(beforeComments)+2 {
		t.Fatalf("expected blind retry to append a second imported comment, before=%d after=%d", len(beforeComments), len(afterComments))
	}
}
