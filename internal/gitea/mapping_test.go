package gitea

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestGiteaIssueToBeads_RoundTripAndPRMerged(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	closed := now.Add(time.Hour)
	gt := &Issue{
		ID:        501,
		Number:    42,
		Title:     "Fix Forgejo auth",
		Body:      "Token header format",
		State:     "closed",
		CreatedAt: &now,
		UpdatedAt: &now,
		ClosedAt:  &closed,
		HTMLURL:   "https://codeberg.org/acme/widgets/issues/42",
		Labels: []Label{
			{ID: 1, Name: "type::bug"},
			{ID: 2, Name: "priority::high"},
			{ID: 3, Name: "backend"},
		},
		Assignee:   &User{ID: 9, Username: "alice", Login: "alice"},
		Milestone:  &Milestone{ID: 3, Title: "v1.2"},
		Repository: &RepositoryMeta{ID: 99, Owner: "acme", Name: "widgets", FullName: "acme/widgets"},
		PullRequest: &PullRequestMeta{
			Merged:   true,
			MergedAt: &closed,
		},
		BlockedBy: []Issue{
			{ID: 500, Number: 41, HTMLURL: "https://codeberg.org/acme/widgets/issues/41", Repository: &RepositoryMeta{ID: 99}},
		},
	}

	conv := GiteaIssueToBeads(gt, DefaultMappingConfig())
	if conv == nil || conv.Issue == nil {
		t.Fatal("GiteaIssueToBeads returned nil")
	}
	if conv.Issue.Status != types.StatusClosed {
		t.Errorf("Status = %q, want closed", conv.Issue.Status)
	}
	if conv.Issue.IssueType != types.TypeBug {
		t.Errorf("IssueType = %q, want bug", conv.Issue.IssueType)
	}
	if conv.Issue.Priority != 1 {
		t.Errorf("Priority = %d, want 1", conv.Issue.Priority)
	}
	if conv.Issue.Assignee != "alice" {
		t.Errorf("Assignee = %q, want alice", conv.Issue.Assignee)
	}
	if conv.Issue.SourceSystem != "gitea:99:42" {
		t.Errorf("SourceSystem = %q, want gitea:99:42", conv.Issue.SourceSystem)
	}
	if len(conv.Issue.Labels) != 1 || conv.Issue.Labels[0] != "backend" {
		t.Errorf("Labels = %v, want [backend]", conv.Issue.Labels)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(conv.Issue.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta["gitea.pr_merged"] != true || meta["gitea.milestone"] != "v1.2" {
		t.Errorf("metadata = %v, want gitea.pr_merged=true & gitea.milestone=v1.2", meta)
	}
	if len(conv.Dependencies) != 1 || conv.Dependencies[0].ToNumber != 41 || conv.Dependencies[0].Type != string(types.DepBlocks) {
		t.Errorf("Dependencies = %+v", conv.Dependencies)
	}
}

func TestClosedStateMapOverride(t *testing.T) {
	cfg := DefaultMappingConfig()
	gt := &Issue{
		Number: 10,
		State:  "closed",
		Labels: []Label{{Name: "wontfix"}},
	}

	// Default behavior: closed -> closed.
	conv := GiteaIssueToBeads(gt, cfg)
	if conv.Issue.Status != types.StatusClosed {
		t.Errorf("default closed status = %q, want closed", conv.Issue.Status)
	}

	// Explicit override via gitea.closed_state_map.wontfix=deferred.
	cfg.ClosedStateMap["wontfix"] = "deferred"
	conv = GiteaIssueToBeads(gt, cfg)
	if conv.Issue.Status != types.StatusDeferred {
		t.Errorf("overridden closed status = %q, want deferred", conv.Issue.Status)
	}
}

func TestFilterNonScopedLabelsIsNonNil(t *testing.T) {
	got := filterNonScopedLabels([]string{"type::task", "priority::medium", "status::in_progress"})
	if got == nil || len(got) != 0 {
		t.Fatalf("filterNonScopedLabels = %#v, want non-nil empty slice", got)
	}
}

func TestPushFieldsEqualAndContentHash(t *testing.T) {
	cfg := DefaultMappingConfig()
	local := &types.Issue{
		Title:       "Implement Gitea sync",
		Description: "Add REST v1 adapter",
		Status:      types.StatusInProgress,
		IssueType:   types.TypeFeature,
		Priority:    1,
		Labels:      []string{"backend", "api"},
	}
	remote := &Issue{
		Title: "Implement Gitea sync",
		Body:  "Add REST v1 adapter",
		State: "open",
		Labels: []Label{
			{Name: "api"},
			{Name: "backend"},
			{Name: "status::in_progress"},
			{Name: "priority::high"},
			{Name: "type::feature"},
		},
	}

	if !PushFieldsEqual(local, remote, cfg) {
		t.Fatal("PushFieldsEqual = false for matching local and remote issue")
	}

	h1 := PushContentHash(local, cfg)
	if h1 == "" {
		t.Fatal("PushContentHash returned empty string")
	}
	localReordered := *local
	localReordered.Labels = []string{"api", "backend"}
	if h2 := PushContentHash(&localReordered, cfg); h1 != h2 {
		t.Fatalf("PushContentHash not order-independent: %q != %q", h1, h2)
	}

	localModified := *local
	localModified.Status = types.StatusClosed
	if PushFieldsEqual(&localModified, remote, cfg) {
		t.Fatal("PushFieldsEqual = true after local status changed to closed")
	}
	if h3 := PushContentHash(&localModified, cfg); h3 == h1 {
		t.Fatal("PushContentHash did not change when status changed")
	}
}
