package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/flatfile"
	"github.com/steveyegge/beads/internal/types"
)

// TestMigrateFlatfileCleanupAdviceCoversEvents reproduces TASKS-ifsm: the
// forward-migration abort advice told the user to remove issues/, comments/,
// and config_kv.json before retrying, but CreateIssue also appends created
// (and label_added) events under events/. Following the old advice and
// re-running duplicated every issue's created event permanently. The advice
// must cover every artifact the migration's data writes actually produce.
//
// Oracle: the flat-file store itself — the test performs the same three
// writes the forward migration performs (CreateIssue with a label,
// ImportComment, SetConfig) and checks each artifact that appeared on disk
// is listed in the cleanup command.
func TestMigrateFlatfileCleanupAdviceCoversEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	s, err := flatfile.NewFlatFileStore(dir)
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.CreateIssue(ctx, &types.Issue{
		ID:     "x-1",
		Title:  "migrated issue",
		Labels: []string{"urgent"},
	}, "migrate"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.ImportComment(ctx, &types.Comment{
		IssueID: "x-1",
		Author:  "migrate",
		Text:    "carried comment",
	}); err != nil {
		t.Fatalf("ImportComment: %v", err)
	}
	if err := s.SetConfig(ctx, "issue_prefix", "x"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	// Every artifact the migration wrote, as observed on disk. events/ is the
	// one the old advice missed.
	artifacts := map[string]string{
		"issues":         filepath.Join(dir, "issues", "*.json"),
		"comments":       filepath.Join(dir, "comments", "*"),
		"events":         filepath.Join(dir, "events", "*.jsonl"),
		"config_kv.json": filepath.Join(dir, "config_kv.json"),
	}
	advice := flatfileDataCleanup(dir)
	for name, pattern := range artifacts {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 0 {
			t.Fatalf("migration writes produced nothing at %s — test setup no longer mirrors the migration", pattern)
		}
		if !strings.Contains(advice, filepath.Join(dir, name)) {
			t.Errorf("cleanup advice omits %s (retry would duplicate its contents):\n%s", name, advice)
		}
	}
}
