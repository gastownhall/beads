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
	// Reopen through the lifecycle operation so the fixture carries the full
	// closure teardown, mirroring closeIssue above.
	if err := h.s.ReopenIssue(h.ctx, issueID, "", "test-user"); err != nil {
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
		reason := "Found a regression"
		if err := h.s.ReopenIssue(h.ctx, issue.ID, reason, "test-user"); err != nil {
			h.t.Fatalf("Failed to reopen issue: %v", err)
		}
		comments, err := h.s.GetIssueComments(h.ctx, issue.ID)
		if err != nil {
			h.t.Fatalf("Failed to get comments: %v", err)
		}
		if len(comments) != 1 {
			h.t.Fatalf("Expected exactly 1 comment after a reasoned reopen, got %d", len(comments))
		}
		if comments[0].Text != reason || comments[0].Author != "test-user" {
			h.t.Errorf("Reopen comment = {author: %q, text: %q}, want {author: %q, text: %q}",
				comments[0].Author, comments[0].Text, "test-user", reason)
		}
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
