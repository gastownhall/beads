package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestCommentsCommand(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-comments-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s, err := sqlite.New(testDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Create test issue
	issue := &types.Issue{
		Title:       "Test Issue",
		Description: "Test description",
		Priority:    1,
		IssueType:   types.TypeBug,
		Status:      types.StatusOpen,
	}

	if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	t.Run("add comment", func(t *testing.T) {
		comment, err := s.AddIssueComment(ctx, issue.ID, "alice", "This is a test comment")
		if err != nil {
			t.Fatalf("Failed to add comment: %v", err)
		}

		if comment.IssueID != issue.ID {
			t.Errorf("Expected issue ID %s, got %s", issue.ID, comment.IssueID)
		}
		if comment.Author != "alice" {
			t.Errorf("Expected author alice, got %s", comment.Author)
		}
		if comment.Text != "This is a test comment" {
			t.Errorf("Expected text 'This is a test comment', got %s", comment.Text)
		}
	})

	t.Run("list comments", func(t *testing.T) {
		comments, err := s.GetIssueComments(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get comments: %v", err)
		}

		if len(comments) != 1 {
			t.Errorf("Expected 1 comment, got %d", len(comments))
		}

		if comments[0].Text != "This is a test comment" {
			t.Errorf("Expected comment text, got %s", comments[0].Text)
		}
	})

	t.Run("multiple comments", func(t *testing.T) {
		_, err := s.AddIssueComment(ctx, issue.ID, "bob", "Second comment")
		if err != nil {
			t.Fatalf("Failed to add second comment: %v", err)
		}

		comments, err := s.GetIssueComments(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get comments: %v", err)
		}

		if len(comments) != 2 {
			t.Errorf("Expected 2 comments, got %d", len(comments))
		}
	})

	t.Run("comments on non-existent issue", func(t *testing.T) {
		comments, err := s.GetIssueComments(ctx, "bd-nonexistent")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(comments) != 0 {
			t.Errorf("Expected 0 comments for non-existent issue, got %d", len(comments))
		}
	})
}

func TestIsUnknownOperationError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "unknown operation error",
			err:      fmt.Errorf("unknown operation: test"),
			expected: true,
		},
		{
			name:     "other error",
			err:      fmt.Errorf("some other error"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isUnknownOperationError(tt.err)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v for error: %v", tt.expected, result, tt.err)
			}
		})
	}
}
