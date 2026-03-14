package gitlab

import (
	"strconv"
	"testing"
	"time"

	"github.com/gastownhall/beads/internal/tracker"
	"github.com/gastownhall/beads/internal/types"
)

func TestRegistered(t *testing.T) {
	factory := tracker.Get("gitlab")
	if factory == nil {
		t.Fatal("gitlab tracker not registered")
	}
	tr := factory()
	if tr.Name() != "gitlab" {
		t.Errorf("Name() = %q, want %q", tr.Name(), "gitlab")
	}
	if tr.DisplayName() != "GitLab" {
		t.Errorf("DisplayName() = %q, want %q", tr.DisplayName(), "GitLab")
	}
	if tr.ConfigPrefix() != "gitlab" {
		t.Errorf("ConfigPrefix() = %q, want %q", tr.ConfigPrefix(), "gitlab")
	}
}

func TestIsExternalRef(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://gitlab.com/group/project/-/issues/42", true},
		{"https://my-gitlab.example.com/team/repo/-/issues/123", true},
		{"https://linear.app/team/issue/PROJ-123", false},
		{"https://github.com/org/repo/issues/1", false},
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
		{"https://gitlab.com/group/project/-/issues/42", "42"},
		{"https://gitlab.example.com/team/repo/-/issues/123", "123"},
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
		URL:        "https://gitlab.com/group/project/-/issues/42",
		Identifier: "42",
	}
	ref := tr.BuildExternalRef(ti)
	if ref != ti.URL {
		t.Errorf("BuildExternalRef() = %q, want %q", ref, ti.URL)
	}
}

func TestFieldMapperStatus(t *testing.T) {
	m := &gitlabFieldMapper{config: DefaultMappingConfig()}

	if got := m.StatusToBeads("opened"); got != types.StatusOpen {
		t.Errorf("StatusToBeads(opened) = %q, want %q", got, types.StatusOpen)
	}
	if got := m.StatusToBeads("closed"); got != types.StatusClosed {
		t.Errorf("StatusToBeads(closed) = %q, want %q", got, types.StatusClosed)
	}
	if got := m.StatusToBeads("reopened"); got != types.StatusOpen {
		t.Errorf("StatusToBeads(reopened) = %q, want %q", got, types.StatusOpen)
	}
}

func TestFieldMapperPriority(t *testing.T) {
	m := &gitlabFieldMapper{config: DefaultMappingConfig()}

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

func TestGitLabToTrackerIssue(t *testing.T) {
	now := time.Now()
	gl := &Issue{
		ID:          100,
		IID:         42,
		Title:       "Fix pipeline",
		Description: "CI is broken",
		State:       "opened",
		WebURL:      "https://gitlab.com/group/project/-/issues/42",
		Labels:      []string{"bug", "priority::high"},
		CreatedAt:   &now,
		UpdatedAt:   &now,
		Assignee:    &User{ID: 5, Username: "bob"},
	}

	ti := gitlabToTrackerIssue(gl)

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
	if ti.Raw != gl {
		t.Error("Raw should reference original gitlab.Issue")
	}
	if len(ti.Labels) != 2 {
		t.Errorf("Labels count = %d, want 2", len(ti.Labels))
	}
}
