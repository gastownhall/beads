package targetprocess

import (
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func TestFieldMapperIssueToBeads(t *testing.T) {
	t.Parallel()

	mapper := newFieldMapper(nil, nil)
	assignable := &Assignable{
		ID:          123,
		Name:        "Fix login",
		Description: "Handle expired sessions",
		Tags:        "auth, backend",
		EntityType:  &EntityRef{Name: "Bug"},
		EntityState: &EntityState{EntityRef: EntityRef{Name: "Open"}, IsFinal: false},
		AssignedUser: &User{
			Login: "alice",
		},
	}

	conversion := mapper.IssueToBeads(&tracker.TrackerIssue{
		Raw: assignable,
		URL: "https://example.tpondemand.com/api/v1/Assignables/123",
	})
	if conversion == nil || conversion.Issue == nil {
		t.Fatal("expected issue conversion")
	}

	issue := conversion.Issue
	if issue.IssueType != types.TypeBug {
		t.Fatalf("expected bug issue type, got %q", issue.IssueType)
	}
	if issue.Status != types.StatusOpen {
		t.Fatalf("expected open status, got %q", issue.Status)
	}
	if issue.Assignee != "alice" {
		t.Fatalf("expected assignee alice, got %q", issue.Assignee)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "auth" || issue.Labels[1] != "backend" {
		t.Fatalf("unexpected labels: %#v", issue.Labels)
	}
}

func TestFieldMapperStatusAndTypeDefaults(t *testing.T) {
	t.Parallel()

	mapper := newFieldMapper(nil, nil)

	if got := mapper.StatusToBeads("Blocked"); got != types.StatusBlocked {
		t.Fatalf("expected blocked, got %q", got)
	}
	if got := mapper.StatusToBeads("Done"); got != types.StatusClosed {
		t.Fatalf("expected closed, got %q", got)
	}
	if got := mapper.TypeToTracker(types.TypeFeature); got != "UserStory" {
		t.Fatalf("expected UserStory, got %#v", got)
	}
	if got := mapper.TypeToTracker(types.TypeBug); got != "Bug" {
		t.Fatalf("expected Bug, got %#v", got)
	}
}
