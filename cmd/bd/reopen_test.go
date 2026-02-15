//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

type reopenTestHelper struct {
	s   *dolt.DoltStore
	ctx context.Context
	t   *testing.T
}

func (h *reopenTestHelper) createIssue(title string, issueType types.IssueType, priority int) *types.Issue {
	issue := &types.Issue{
		Title:     title,
		Priority:  priority,
		IssueType: issueType,
		Status:    types.StatusOpen,
	}
	if err := h.s.CreateIssue(h.ctx, issue, "test-user"); err != nil {
		h.t.Fatalf("Failed to create issue: %v", err)
	}
	return issue
}

func (h *reopenTestHelper) closeIssue(issueID, reason string) {
	if err := h.s.CloseIssue(h.ctx, issueID, "test-user", reason, ""); err != nil {
		h.t.Fatalf("Failed to close issue: %v", err)
	}
}

func (h *reopenTestHelper) reopenIssue(issueID string) {
	updates := map[string]interface{}{
		"status": string(types.StatusOpen),
	}
	if err := h.s.UpdateIssue(h.ctx, issueID, updates, "test-user"); err != nil {
		h.t.Fatalf("Failed to reopen issue: %v", err)
	}
}

func (h *reopenTestHelper) getIssue(issueID string) *types.Issue {
	issue, err := h.s.GetIssue(h.ctx, issueID)
	if err != nil {
		h.t.Fatalf("Failed to get issue: %v", err)
	}
	return issue
}

func (h *reopenTestHelper) addComment(issueID, comment string) {
	if err := h.s.AddComment(h.ctx, issueID, "test-user", comment); err != nil {
		h.t.Fatalf("Failed to add comment: %v", err)
	}
}

func (h *reopenTestHelper) assertStatus(issueID string, expected types.Status) {
	issue := h.getIssue(issueID)
	if issue.Status != expected {
		h.t.Errorf("Expected status %s, got %s", expected, issue.Status)
	}
}

func (h *reopenTestHelper) assertClosedAtSet(issueID string) {
	issue := h.getIssue(issueID)
	if issue.ClosedAt == nil {
		h.t.Error("Expected ClosedAt to be set")
	}
}

func (h *reopenTestHelper) assertClosedAtNil(issueID string) {
	issue := h.getIssue(issueID)
	if issue.ClosedAt != nil {
		h.t.Errorf("Expected ClosedAt to be nil, got %v", issue.ClosedAt)
	}
}

func (h *reopenTestHelper) assertCommentEvent(issueID, comment string) {
	events, err := h.s.GetEvents(h.ctx, issueID, 100)
	if err != nil {
		h.t.Fatalf("Failed to get events: %v", err)
	}

	for _, e := range events {
		if e.EventType == types.EventCommented && e.Comment != nil && *e.Comment == comment {
			return
		}
	}
	h.t.Errorf("Expected to find comment event with reason '%s'", comment)
}

func TestReopenCommand(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-reopen-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &reopenTestHelper{s: s, ctx: ctx, t: t}

	t.Run("reopen closed issue", func(t *testing.T) {
		issue := h.createIssue("Test Issue", types.TypeBug, 1)
		h.closeIssue(issue.ID, "Closing for test")
		h.assertStatus(issue.ID, types.StatusClosed)
		h.assertClosedAtSet(issue.ID)
		h.reopenIssue(issue.ID)
		h.assertStatus(issue.ID, types.StatusOpen)
		h.assertClosedAtNil(issue.ID)
	})

	t.Run("reopen with reason adds comment", func(t *testing.T) {
		issue := h.createIssue("Test Issue 2", types.TypeTask, 1)
		h.closeIssue(issue.ID, "Done")
		h.reopenIssue(issue.ID)
		reason := "Found a regression"
		h.addComment(issue.ID, reason)
		h.assertCommentEvent(issue.ID, reason)
	})

	t.Run("reopen multiple issues", func(t *testing.T) {
		issue1 := h.createIssue("Multi Test 1", types.TypeBug, 1)
		issue2 := h.createIssue("Multi Test 2", types.TypeBug, 1)
		h.closeIssue(issue1.ID, "Done")
		h.closeIssue(issue2.ID, "Done")
		h.reopenIssue(issue1.ID)
		h.reopenIssue(issue2.ID)
		h.assertStatus(issue1.ID, types.StatusOpen)
		h.assertStatus(issue2.ID, types.StatusOpen)
	})

	t.Run("reopen already open issue is no-op", func(t *testing.T) {
		issue := h.createIssue("Already Open", types.TypeTask, 1)
		h.reopenIssue(issue.ID)
		h.assertStatus(issue.ID, types.StatusOpen)
		h.assertClosedAtNil(issue.ID)
	})
}
