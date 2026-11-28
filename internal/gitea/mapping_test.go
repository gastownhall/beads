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
	created := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	closed := created.Add(time.Hour)
	cfg := DefaultMappingConfig()
	gt := &Issue{
		Number:    10,
		Title:     "Won't fix item",
		State:     "closed",
		CreatedAt: &created,
		UpdatedAt: &closed,
		ClosedAt:  &closed,
		Labels:    []Label{{Name: "wontfix"}},
	}

	// Default behavior: closed -> closed with ClosedAt preserved and valid.
	conv := GiteaIssueToBeads(gt, cfg)
	if conv.Issue.Status != types.StatusClosed {
		t.Errorf("default closed status = %q, want closed", conv.Issue.Status)
	}
	if conv.Issue.ClosedAt == nil {
		t.Fatal("default closed issue missing ClosedAt")
	}
	if err := conv.Issue.ValidateWithCustom(nil, nil); err != nil {
		t.Fatalf("default closed issue failed ValidateWithCustom: %v", err)
	}

	// Explicit override via gitea.closed_state_map.wontfix=deferred:
	// ClosedAt must be cleared so ValidateWithCustom succeeds on pull.
	cfg.ClosedStateMap["wontfix"] = "deferred"
	conv = GiteaIssueToBeads(gt, cfg)
	if conv.Issue.Status != types.StatusDeferred {
		t.Errorf("overridden closed status = %q, want deferred", conv.Issue.Status)
	}
	if conv.Issue.ClosedAt != nil {
		t.Errorf("overridden non-closed issue ClosedAt = %v, want nil", conv.Issue.ClosedAt)
	}
	if err := conv.Issue.ValidateWithCustom(nil, nil); err != nil {
		t.Fatalf("overridden non-closed issue failed ValidateWithCustom: %v", err)
	}

	// Pushing the mapped bead back must keep remote state closed without adding status::deferred.
	fields := BeadsIssueToGiteaFields(conv.Issue, cfg)
	if fields["state"] != "closed" {
		t.Errorf("BeadsIssueToGiteaFields state = %v, want closed", fields["state"])
	}
	pushedLabels, _ := fields["labels"].([]string)
	for _, l := range pushedLabels {
		if l == "status::deferred" {
			t.Errorf("BeadsIssueToGiteaFields unexpectedly included status::deferred in %v", pushedLabels)
		}
	}
	remoteMatched := &Issue{
		Title: conv.Issue.Title,
		Body:  conv.Issue.Description,
		State: "closed",
		Labels: []Label{
			{Name: "type::task"},
			{Name: "priority::medium"},
			{Name: "wontfix"},
		},
	}
	if !PushFieldsEqual(conv.Issue, remoteMatched, cfg) {
		t.Fatal("PushFieldsEqual = false for closed_state_map round-trip")
	}
	remoteOpen := *remoteMatched
	remoteOpen.State = "open"
	if PushFieldsEqual(conv.Issue, &remoteOpen, cfg) {
		t.Fatal("PushFieldsEqual = true when remote was reopened")
	}
	if PushContentHash(conv.Issue, cfg) == PushContentHash(conv.Issue, DefaultMappingConfig()) {
		t.Fatal("PushContentHash did not distinguish closed_state_map state")
	}

	// Reopening the bead locally must push state=open even while wontfix remains.
	reopened := *conv.Issue
	reopened.Status = types.StatusOpen
	if got := BeadsIssueToGiteaFields(&reopened, cfg)["state"]; got != "open" {
		t.Errorf("reopened bead state = %v, want open", got)
	}

	// A deferred bead without the mapped label stays open with status::deferred.
	plainDeferred := &types.Issue{
		Title:     "Deferred normally",
		Status:    types.StatusDeferred,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	plainFields := BeadsIssueToGiteaFields(plainDeferred, cfg)
	if plainFields["state"] != "open" {
		t.Errorf("plain deferred bead state = %v, want open", plainFields["state"])
	}
	plainLabels, _ := plainFields["labels"].([]string)
	foundStatusDeferred := false
	for _, l := range plainLabels {
		if l == "status::deferred" {
			foundStatusDeferred = true
		}
	}
	if !foundStatusDeferred {
		t.Errorf("plain deferred bead missing status::deferred in %v", plainLabels)
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
