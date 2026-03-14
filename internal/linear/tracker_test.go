package linear

import (
	"testing"

	"github.com/gastownhall/beads/internal/tracker"
	"github.com/gastownhall/beads/internal/types"
)

func TestRegistered(t *testing.T) {
	factory := tracker.Get("linear")
	if factory == nil {
		t.Fatal("linear tracker not registered")
	}
	tr := factory()
	if tr.Name() != "linear" {
		t.Errorf("Name() = %q, want %q", tr.Name(), "linear")
	}
	if tr.DisplayName() != "Linear" {
		t.Errorf("DisplayName() = %q, want %q", tr.DisplayName(), "Linear")
	}
	if tr.ConfigPrefix() != "linear" {
		t.Errorf("ConfigPrefix() = %q, want %q", tr.ConfigPrefix(), "linear")
	}
}

func TestIsExternalRef(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://linear.app/team/issue/PROJ-123", true},
		{"https://linear.app/team/issue/PROJ-123/some-title", true},
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
		{"https://linear.app/team/issue/PROJ-123/some-title", "PROJ-123"},
		{"https://linear.app/team/issue/PROJ-123", "PROJ-123"},
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
		URL:        "https://linear.app/team/issue/PROJ-123/some-title-slug",
		Identifier: "PROJ-123",
	}
	ref := tr.BuildExternalRef(ti)
	want := "https://linear.app/team/issue/PROJ-123"
	if ref != want {
		t.Errorf("BuildExternalRef() = %q, want %q", ref, want)
	}
}

func TestFieldMapperPriority(t *testing.T) {
	m := &linearFieldMapper{config: DefaultMappingConfig()}

	// Linear 1 (urgent) -> Beads 0 (critical)
	if got := m.PriorityToBeads(1); got != 0 {
		t.Errorf("PriorityToBeads(1) = %d, want 0", got)
	}
	// Beads 0 (critical) -> Linear 1 (urgent)
	if got := m.PriorityToTracker(0); got != 1 {
		t.Errorf("PriorityToTracker(0) = %v, want 1", got)
	}
}

func TestFieldMapperStatus(t *testing.T) {
	m := &linearFieldMapper{config: DefaultMappingConfig()}

	// Started -> in_progress
	state := &State{Type: "started", Name: "In Progress"}
	if got := m.StatusToBeads(state); got != types.StatusInProgress {
		t.Errorf("StatusToBeads(started) = %q, want %q", got, types.StatusInProgress)
	}

	// Completed -> closed
	state = &State{Type: "completed", Name: "Done"}
	if got := m.StatusToBeads(state); got != types.StatusClosed {
		t.Errorf("StatusToBeads(completed) = %q, want %q", got, types.StatusClosed)
	}
}

func TestLinearToTrackerIssue(t *testing.T) {
	li := &Issue{
		ID:          "uuid-123",
		Identifier:  "TEAM-42",
		Title:       "Fix the bug",
		Description: "It's broken",
		URL:         "https://linear.app/team/issue/TEAM-42/fix-the-bug",
		Priority:    2,
		CreatedAt:   "2026-01-15T10:00:00Z",
		UpdatedAt:   "2026-01-16T14:30:00Z",
		Assignee:    &User{ID: "user-1", Name: "Alice", Email: "alice@example.com"},
		State:       &State{Type: "started", Name: "In Progress"},
	}

	ti := linearToTrackerIssue(li)

	if ti.ID != "uuid-123" {
		t.Errorf("ID = %q, want %q", ti.ID, "uuid-123")
	}
	if ti.Identifier != "TEAM-42" {
		t.Errorf("Identifier = %q, want %q", ti.Identifier, "TEAM-42")
	}
	if ti.Assignee != "Alice" {
		t.Errorf("Assignee = %q, want %q", ti.Assignee, "Alice")
	}
	if ti.AssigneeEmail != "alice@example.com" {
		t.Errorf("AssigneeEmail = %q, want %q", ti.AssigneeEmail, "alice@example.com")
	}
	if ti.Raw != li {
		t.Error("Raw should reference original linear.Issue")
	}
}
