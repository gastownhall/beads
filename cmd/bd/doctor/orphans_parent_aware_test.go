package doctor

import (
	"context"
	"os/exec"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// mockOrphanProvider implements types.IssueProvider for orphan-detection tests.
type mockOrphanProvider struct {
	issues   []*types.Issue
	parented map[string]bool
}

func (m *mockOrphanProvider) GetOpenIssues(ctx context.Context) ([]*types.Issue, error) {
	return m.issues, nil
}

func (m *mockOrphanProvider) GetParentedOpenIssueIDs(ctx context.Context) (map[string]bool, error) {
	return m.parented, nil
}

func (m *mockOrphanProvider) GetIssuePrefix() string {
	return "bd"
}

// TestFindOrphanedIssues_ExcludesParentedIssues proves the parent-aware
// contract: an open issue referenced in a commit message is only an orphan
// candidate when it has no parent — a parent-child edge means the issue is
// governed by its parent, never orphaned.
func TestFindOrphanedIssues_ExcludesParentedIssues(t *testing.T) {
	dir := setupGitRepo(t)

	commit := exec.Command(
		"git",
		"-c", "user.email=bd@test.local",
		"-c", "user.name=bd",
		"commit", "--allow-empty", "-m",
		"(bd-1) unparented work still open, (bd-2) governed by its parent",
	)
	commit.Dir = dir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	provider := &mockOrphanProvider{
		issues: []*types.Issue{
			{ID: "bd-1", Title: "Unparented open work", Status: types.StatusOpen},
			{ID: "bd-2", Title: "Governed by its parent", Status: types.StatusOpen},
		},
		parented: map[string]bool{"bd-2": true},
	}

	orphans, err := FindOrphanedIssues(dir, provider)
	if err != nil {
		t.Fatalf("FindOrphanedIssues returned error: %v", err)
	}

	for _, orphan := range orphans {
		if orphan.IssueID == "bd-2" {
			t.Fatalf("parented issue bd-2 must not be reported as orphaned; got %+v", orphans)
		}
	}
	found := false
	for _, orphan := range orphans {
		if orphan.IssueID == "bd-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unparented issue bd-1 missing from orphan report: %+v", orphans)
	}
}
