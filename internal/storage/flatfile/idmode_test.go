package flatfile

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// SQL-reference oracle: issueops.GenerateIssueIDInTable routes to
// NextCounterIDTx when issue_id_mode=counter (issues table only), producing
// prefix-1, prefix-2, ... and seeding the counter from the highest existing
// numeric suffix.

func newCounterModeStore(t *testing.T) *FlatFileStore {
	t.Helper()
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "proj"); err != nil {
		t.Fatalf("SetConfig issue_prefix: %v", err)
	}
	if err := s.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("SetConfig issue_id_mode: %v", err)
	}
	return s
}

func TestCounterModeGeneratesSequentialIDs(t *testing.T) {
	s := newCounterModeStore(t)
	for i, want := range []string{"proj-1", "proj-2", "proj-3"} {
		issue := &types.Issue{Title: "counter issue", Priority: 2}
		if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %d: %v", i, err)
		}
		if issue.ID != want {
			t.Errorf("issue %d ID = %q, want %q", i, issue.ID, want)
		}
	}
}

func TestCounterModeAppliesToBatchCreate(t *testing.T) {
	s := newCounterModeStore(t)
	issues := []*types.Issue{
		{Title: "first", Priority: 2},
		{Title: "second", Priority: 2},
	}
	if err := s.CreateIssues(ctx, issues, "tester"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}
	if issues[0].ID != "proj-1" || issues[1].ID != "proj-2" {
		t.Errorf("batch IDs = %q, %q, want proj-1, proj-2", issues[0].ID, issues[1].ID)
	}
}

func TestCounterModeSeedsFromExistingIssues(t *testing.T) {
	s := newCounterModeStore(t)
	// Imported issues with explicit sequential and hash IDs already on disk.
	for _, id := range []string{"proj-7", "proj-3", "proj-a1b2"} {
		if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: "existing", Priority: 2}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	issue := &types.Issue{Title: "new one", Priority: 2}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.ID != "proj-8" {
		t.Errorf("ID = %q, want proj-8 (seeded past highest numeric suffix)", issue.ID)
	}
}

func TestCounterModeSkipsWisps(t *testing.T) {
	s := newCounterModeStore(t)
	wisp := &types.Issue{Title: "a wisp", Priority: 2, Ephemeral: true}
	if err := s.CreateIssues(ctx, []*types.Issue{wisp}, "tester"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}
	if !strings.HasPrefix(wisp.ID, "proj-wisp-") {
		t.Fatalf("wisp ID = %q, want proj-wisp- hash prefix", wisp.ID)
	}
	if wisp.ID == "proj-wisp-1" {
		t.Errorf("wisp got a sequential ID; counter mode must not apply to wisps")
	}
	if n, err := s.peekSequentialID("proj-wisp"); err != nil || n != 0 {
		t.Errorf("peekSequentialID(proj-wisp) = %d, %v; want 0 (no counter allocated)", n, err)
	}
}
