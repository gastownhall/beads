//go:build cgo

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestIssueContentSize(t *testing.T) {
	tests := []struct {
		name  string
		issue *types.Issue
		want  int
	}{
		{
			name:  "empty issue",
			issue: &types.Issue{},
			want:  0,
		},
		{
			name: "issue with content",
			issue: &types.Issue{
				Description:        "hello",
				Design:             "world",
				AcceptanceCriteria: "foo",
				Notes:              "bar",
			},
			want: 5 + 5 + 3 + 3,
		},
		{
			name: "only description",
			issue: &types.Issue{
				Description: "some long description text",
			},
			want: 26,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := issueContentSize(tt.issue)
			if got != tt.want {
				t.Errorf("issueContentSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestRestoreWithInvalidIssueID verifies that restore handles non-existent issues
// gracefully without panicking.
func TestRestoreWithInvalidIssueID(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	testStore := newTestStore(t, dbPath)
	defer testStore.Close()

	ctx := context.Background()
	issue, err := testStore.GetIssue(ctx, "nonexistent-issue-12345")

	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetIssue expected ErrNotFound, got: %v", err)
	}
	if issue != nil {
		t.Fatalf("GetIssue returned issue for non-existent ID: %v", issue)
	}
}
