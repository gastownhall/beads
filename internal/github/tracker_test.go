package github

import (
	"strconv"
	"testing"
	"time"

	"github.com/gastownhall/beads/internal/tracker"
	"github.com/gastownhall/beads/internal/types"
)

func TestRegistered(t *testing.T) {
	factory := tracker.Get("github")
	if factory == nil {
		t.Fatal("github tracker not registered")
	}
	tr := factory()
	if tr.Name() != "github" {
		t.Errorf("Name() = %q, want %q", tr.Name(), "github")
	}
	if tr.DisplayName() != "GitHub" {
		t.Errorf("DisplayName() = %q, want %q", tr.DisplayName(), "GitHub")
	}
	if tr.ConfigPrefix() != "github" {
		t.Errorf("ConfigPrefix() = %q, want %q", tr.ConfigPrefix(), "github")
	}
}

func TestIsExternalRef(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://github.com/org/repo/issues/42", true},
		{"https://github.com/team/project/issues/123", true},
		{"https://gitlab.com/group/project/-/issues/42", false},
		{"https://linear.app/team/issue/PROJ-123", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tr.IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

func TestExtractIdentifier(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want string
	}{
		{"https://github.com/org/repo/issues/42", "42"},
		{"https://github.com/team/project/issues/123", "123"},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestBuildExternalRef(t *testing.T) {
	tr := &Tracker{}
	ti := &tracker.TrackerIssue{
		URL:        "https://github.com/org/repo/issues/42",
		Identifier: "42",
	}
	ref := tr.BuildExternalRef(ti)
	if ref != ti.URL {
		t.Errorf("BuildExternalRef() = %q, want %q", ref, ti.URL)
	}

	// Without URL, should use fallback
	ti2 := &tracker.TrackerIssue{
		Identifier: "42",
	}
	ref2 := tr.BuildExternalRef(ti2)
	if ref2 != "github:42" {
		t.Errorf("BuildExternalRef() = %q, want %q", ref2, "github:42")
	}
}

func TestFieldMapperStatus(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	if got := m.StatusToBeads("open"); got != types.StatusOpen {
		t.Errorf("StatusToBeads(open) = %q, want %q", got, types.StatusOpen)
	}
	if got := m.StatusToBeads("closed"); got != types.StatusClosed {
		t.Errorf("StatusToBeads(closed) = %q, want %q", got, types.StatusClosed)
	}
}

func TestFieldMapperPriority(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	if got := m.PriorityToBeads("critical"); got != 0 {
		t.Errorf("PriorityToBeads(critical) = %d, want 0", got)
	}
	if got := m.PriorityToBeads("high"); got != 1 {
		t.Errorf("PriorityToBeads(high) = %d, want 1", got)
	}
	if got := m.PriorityToBeads("low"); got != 3 {
		t.Errorf("PriorityToBeads(low) = %d, want 3", got)
	}
}

func TestFieldMapperType(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	if got := m.TypeToBeads("bug"); got != types.TypeBug {
		t.Errorf("TypeToBeads(bug) = %q, want %q", got, types.TypeBug)
	}
	if got := m.TypeToBeads("feature"); got != types.TypeFeature {
		t.Errorf("TypeToBeads(feature) = %q, want %q", got, types.TypeFeature)
	}
	if got := m.TypeToBeads("unknown"); got != types.TypeTask {
		t.Errorf("TypeToBeads(unknown) = %q, want %q (default)", got, types.TypeTask)
	}
}

func TestGitHubToTrackerIssue(t *testing.T) {
	now := time.Now()
	gh := &Issue{
		ID:      100,
		Number:  42,
		Title:   "Fix pipeline",
		Body:    "CI is broken",
		State:   "open",
		HTMLURL: "https://github.com/org/repo/issues/42",
		Labels: []Label{
			{Name: "bug"},
			{Name: "priority::high"},
		},
		CreatedAt: &now,
		UpdatedAt: &now,
		Assignee:  &User{ID: 5, Login: "bob"},
	}

	ti := githubToTrackerIssue(gh)

	if ti.ID != "100" {
		t.Errorf("ID = %q, want %q", ti.ID, "100")
	}
	if ti.Identifier != "42" {
		t.Errorf("Identifier = %q, want %q", ti.Identifier, "42")
	}
	if ti.Assignee != "bob" {
		t.Errorf("Assignee = %q, want %q", ti.Assignee, "bob")
	}
	if ti.AssigneeID != strconv.Itoa(5) {
		t.Errorf("AssigneeID = %q, want %q", ti.AssigneeID, "5")
	}
	if ti.Raw != gh {
		t.Error("Raw should reference original github.Issue")
	}
	if len(ti.Labels) != 2 {
		t.Errorf("Labels count = %d, want 2", len(ti.Labels))
	}
	if ti.URL != "https://github.com/org/repo/issues/42" {
		t.Errorf("URL = %q, want GitHub URL", ti.URL)
	}
}

func TestGitHubToTrackerIssue_ClosedWithTimestamp(t *testing.T) {
	now := time.Now()
	closedAt := now.Add(-time.Hour)
	gh := &Issue{
		ID:        200,
		Number:    99,
		Title:     "Closed issue",
		State:     "closed",
		CreatedAt: &now,
		UpdatedAt: &now,
		ClosedAt:  &closedAt,
		HTMLURL:   "https://github.com/org/repo/issues/99",
	}

	ti := githubToTrackerIssue(gh)

	if ti.CompletedAt == nil {
		t.Fatal("CompletedAt is nil for closed issue, want non-nil")
	}
	if !ti.CompletedAt.Equal(closedAt) {
		t.Errorf("CompletedAt = %v, want %v", ti.CompletedAt, closedAt)
	}
}

func TestFieldMapperIssueToBeads(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	now := time.Now()
	gh := &Issue{
		ID:     100,
		Number: 42,
		Title:  "Test issue",
		Body:   "Test body",
		State:  "open",
		Labels: []Label{
			{Name: "type::bug"},
			{Name: "priority::high"},
		},
		CreatedAt: &now,
		UpdatedAt: &now,
		HTMLURL:   "https://github.com/org/repo/issues/42",
	}

	ti := &tracker.TrackerIssue{
		ID:         "100",
		Identifier: "42",
		Title:      "Test issue",
		Raw:        gh,
	}

	conv := m.IssueToBeads(ti)
	if conv == nil {
		t.Fatal("IssueToBeads returned nil")
	}
	if conv.Issue == nil {
		t.Fatal("IssueToBeads returned nil issue")
	}
	if conv.Issue.Title != "Test issue" {
		t.Errorf("Issue.Title = %q, want %q", conv.Issue.Title, "Test issue")
	}
	if conv.Issue.IssueType != types.TypeBug {
		t.Errorf("Issue.IssueType = %q, want %q", conv.Issue.IssueType, types.TypeBug)
	}
}

func TestFieldMapperIssueToBeads_NilRaw(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	ti := &tracker.TrackerIssue{
		ID:  "100",
		Raw: nil, // Not a *github.Issue
	}

	conv := m.IssueToBeads(ti)
	if conv != nil {
		t.Error("IssueToBeads with nil Raw should return nil")
	}
}

func TestValidate(t *testing.T) {
	tr := &Tracker{}
	if err := tr.Validate(); err == nil {
		t.Error("Validate() on uninitialized tracker should return error")
	}

	tr.client = &Client{} // Set client
	if err := tr.Validate(); err != nil {
		t.Errorf("Validate() on initialized tracker returned error: %v", err)
	}
}
