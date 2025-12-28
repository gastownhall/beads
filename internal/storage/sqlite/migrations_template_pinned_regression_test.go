package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestRunMigrations_DoesNotResetPinnedOrTemplate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beads.db")

	s, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	issue := &types.Issue{
		Title:      "Pinned template",
		Status:     types.StatusOpen,
		Priority:   2,
		IssueType:  types.TypeTask,
		Pinned:     true,
		IsTemplate: true,
	}
	if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	_ = s.Close()

	s2, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("New(reopen): %v", err)
	}
	defer func() { _ = s2.Close() }()

	got, err := s2.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got == nil {
		t.Fatalf("expected issue to exist")
	}
	if !got.Pinned {
		t.Fatalf("expected issue to remain pinned")
	}
	if !got.IsTemplate {
		t.Fatalf("expected issue to remain template")
	}
}
