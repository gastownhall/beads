package github

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// A hydrated thread lands on the beads issue with empty IDs so create and
// update converge on the same content-derived id; dedup keys on content.
func TestGitHubIssueToBeads_ImportsHydratedComments(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c1 := created.Add(2 * time.Hour)
	gh := &Issue{
		Number:    5,
		Title:     "T",
		Body:      "B",
		State:     "open",
		CreatedAt: &created,
		UpdatedAt: &created,
		HydratedComments: []IssueComment{
			{ID: 9001, User: &User{Login: "alice"}, Body: "hello", CreatedAt: &c1},
			{ID: 9002, Body: "no user", CreatedAt: nil},
		},
	}

	conv := GitHubIssueToBeads(gh, DefaultMappingConfig())
	if conv == nil || conv.Issue == nil {
		t.Fatal("GitHubIssueToBeads() returned nil conversion")
	}

	got := conv.Issue.Comments
	if len(got) != 2 {
		t.Fatalf("Comments = %d entries, want 2", len(got))
	}
	if got[0].ID != "" {
		t.Errorf("comment[0].ID = %q, want empty (content-derived at persist)", got[0].ID)
	}
	if got[0].Author != "alice" || got[0].Text != "hello" {
		t.Errorf("comment[0] = %q/%q, want alice/hello", got[0].Author, got[0].Text)
	}
	if !got[0].CreatedAt.Equal(c1) {
		t.Errorf("comment[0].CreatedAt = %v, want %v", got[0].CreatedAt, c1)
	}
	if got[1].Author != "github-unknown" {
		t.Errorf("missing-user author = %q, want github-unknown", got[1].Author)
	}
	if !got[1].CreatedAt.Equal(created) {
		t.Errorf("nil CreatedAt should fall back to issue creation, got %v want %v", got[1].CreatedAt, created)
	}
}

func TestGitHubIssueToBeads_NoComments(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	gh := &Issue{Number: 6, Title: "T", Body: "B", State: "open", CreatedAt: &created, UpdatedAt: &created}
	conv := GitHubIssueToBeads(gh, DefaultMappingConfig())
	if len(conv.Issue.Comments) != 0 {
		t.Errorf("Comments = %d entries, want 0", len(conv.Issue.Comments))
	}
}

func TestPushFieldDiff(t *testing.T) {
	base := func() (*types.Issue, *Issue) {
		local := &types.Issue{
			Title:       "same",
			Description: "same body",
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			Priority:    2,
		}
		// A round-tripped issue carries its derived scoped labels remotely.
		remote := &Issue{
			Title: "same", Body: "same body", State: "open",
			Labels: []Label{{Name: "type::task"}, {Name: "priority::medium"}},
		}
		return local, remote
	}

	t.Run("no difference", func(t *testing.T) {
		local, remote := base()
		if diff := PushFieldDiff(local, remote, DefaultMappingConfig()); len(diff) != 0 {
			t.Errorf("PushFieldDiff() = %v, want empty", diff)
		}
	})

	t.Run("title and state differ", func(t *testing.T) {
		local, remote := base()
		local.Title = "changed"
		remote.State = "closed"
		diff := PushFieldDiff(local, remote, DefaultMappingConfig())
		if len(diff) != 2 || diff[0] != "title" || diff[1] != "state" {
			t.Errorf("PushFieldDiff() = %v, want [title state]", diff)
		}
	})

	t.Run("label removal is disclosed with names", func(t *testing.T) {
		local, remote := base()
		remote.Labels = []Label{{Name: "bug"}, {Name: "question"}, {Name: "type::task"}}
		diff := PushFieldDiff(local, remote, DefaultMappingConfig())
		if len(diff) != 1 {
			t.Fatalf("PushFieldDiff() = %v, want one labels entry", diff)
		}
		if !strings.Contains(diff[0], "-2") || !strings.Contains(diff[0], "bug") || !strings.Contains(diff[0], "question") {
			t.Errorf("labels entry %q must disclose removal count and names", diff[0])
		}
	})

	t.Run("label addition counted without removal names", func(t *testing.T) {
		local, remote := base()
		remote.Labels = []Label{{Name: "type::task"}, {Name: "priority::medium"}}
		local.Labels = []string{"enhancement"}
		diff := PushFieldDiff(local, remote, DefaultMappingConfig())
		if len(diff) != 1 || !strings.Contains(diff[0], "+1/-0") {
			t.Errorf("PushFieldDiff() = %v, want labels (+1/-0)", diff)
		}
	})
}
