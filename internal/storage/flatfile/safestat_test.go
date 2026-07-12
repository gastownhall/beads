package flatfile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// These tests pin the TASKS-i8tx fix: the CreateIssue and UpdateIssueID
// existence pre-checks must validate caller-supplied IDs BEFORE Stat, so a
// traversal-shaped ID gets the ID-validation error instead of probing a
// sibling path outside the issues dir and reporting "already exists" for an
// issue that does not exist.

func TestCreateIssueTraversalIDValidatedBeforeStat(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Plant a sibling file exactly where the raw Stat would probe.
	sibling := filepath.Join(s.beadsDir, "config_kv.json")
	if err := os.WriteFile(sibling, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("plant sibling: %v", err)
	}

	err := s.CreateIssue(ctx, &types.Issue{ID: "../config_kv", Title: "evil"}, "tester")
	if err == nil {
		t.Fatal("CreateIssue accepted a traversal ID")
	}
	if strings.Contains(err.Error(), "already exists") {
		t.Errorf("got existence-probe error %q, want ID-validation error", err)
	}
}

func TestUpdateIssueIDTraversalNewIDValidatedBeforeStat(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "safe-1", Title: "ok"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	sibling := filepath.Join(s.beadsDir, "config_kv.json")
	if err := os.WriteFile(sibling, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("plant sibling: %v", err)
	}

	err := s.UpdateIssueID(ctx, "safe-1", "../config_kv", &types.Issue{Title: "ok"}, "tester")
	if err == nil {
		t.Fatal("UpdateIssueID accepted a traversal newID")
	}
	if strings.Contains(err.Error(), "already exists") {
		t.Errorf("got existence-probe error %q, want ID-validation error", err)
	}
}
