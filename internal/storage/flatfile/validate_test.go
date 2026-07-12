package flatfile

import (
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return ts
}

// The SQL backends validate every issue via issueops.PrepareIssueForInsert
// (issue.ValidateWithCustom) before insert; these tests assert flatfile
// enforces the same rules instead of persisting garbage rows.

func TestCreateIssueRejectsInvalidIssue(t *testing.T) {
	closedAt := mustTime(t, "2024-01-02T00:00:00Z")
	cases := []struct {
		name  string
		issue *types.Issue
	}{
		{"empty title", &types.Issue{ID: "test-1", Priority: 2}},
		{"priority out of range", &types.Issue{ID: "test-1", Title: "t", Priority: 9}},
		{"invalid status", &types.Issue{ID: "test-1", Title: "t", Priority: 2, Status: "wibble"}},
		{"invalid issue type", &types.Issue{ID: "test-1", Title: "t", Priority: 2, IssueType: "tsak"}},
		{"invalid metadata JSON", &types.Issue{ID: "test-1", Title: "t", Priority: 2, Metadata: []byte("{not json")}},
		{"ephemeral and no_history both set", &types.Issue{ID: "test-1", Title: "t", Priority: 2, Ephemeral: true, NoHistory: true}},
		{"closed_at on non-closed issue", &types.Issue{ID: "test-1", Title: "t", Priority: 2, Status: types.StatusOpen, ClosedAt: &closedAt}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateIssue(ctx, tc.issue, "tester"); err == nil {
				t.Fatal("CreateIssue accepted an issue that SQL backends reject")
			}
			if _, err := os.Stat(s.issueFilename("test-1")); !os.IsNotExist(err) {
				t.Errorf("issue file persisted despite validation failure (stat err = %v)", err)
			}
		})
	}
}

func TestBatchCreateRejectsInvalidIssueAtomically(t *testing.T) {
	s := newTestStore(t)
	valid := &types.Issue{ID: "test-ok", Title: "fine", Priority: 2}
	invalid := &types.Issue{ID: "test-bad", Title: "broken", Priority: 9}
	err := s.CreateIssuesWithFullOptions(ctx, []*types.Issue{valid, invalid}, "tester", storage.BatchCreateOptions{
		OrphanHandling: storage.OrphanAllow,
	})
	if err == nil {
		t.Fatal("batch with invalid issue succeeded; SQL backends reject the whole batch")
	}
	// SQL transaction rollback persists nothing; flatfile's plan-then-write
	// batch must match.
	for _, id := range []string{"test-ok", "test-bad"} {
		if _, statErr := os.Stat(s.issueFilename(id)); !os.IsNotExist(statErr) {
			t.Errorf("issue %s persisted despite batch validation failure", id)
		}
	}
}

func TestCreateIssueAcceptsCustomStatusAndType(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "status.custom", "triage"); err != nil {
		t.Fatalf("SetConfig status.custom: %v", err)
	}
	if err := s.SetConfig(ctx, "types.custom", "incident"); err != nil {
		t.Fatalf("SetConfig types.custom: %v", err)
	}
	issue := &types.Issue{ID: "test-2", Title: "t", Priority: 2, Status: "triage", IssueType: "incident"}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue with configured custom status/type: %v", err)
	}
}

func TestCreateIssueDefaultsIssueType(t *testing.T) {
	s := newTestStore(t)
	issue := &types.Issue{ID: "test-3", Title: "t", Priority: 2}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	got, err := s.GetIssue(ctx, "test-3")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.IssueType != types.TypeTask {
		t.Errorf("IssueType = %q, want %q (types.SetDefaults)", got.IssueType, types.TypeTask)
	}
}
