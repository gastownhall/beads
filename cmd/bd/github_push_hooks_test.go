package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/github"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// TestBuildGitHubPushHooksFieldDiff covers the FieldDiff closure installed by
// buildGitHubPushHooks -- the only glue between the pure github.PushFieldDiff
// and the engine's dry-run preview. Each half is tested in its own package,
// which is precisely what makes a fault in the wiring silent: the closure's
// type assertion returns nil for an absent or mistyped Raw, degrading every
// preview back to the detail-less "would update" line with nothing failing.
//
// A zero-value Tracker suffices: MappingConfig() returns nil, so the hooks fall
// back to github.DefaultMappingConfig(), and neither a client nor a store is
// touched.
func TestBuildGitHubPushHooksFieldDiff(t *testing.T) {
	hooks := buildGitHubPushHooks(&github.Tracker{})
	if hooks == nil || hooks.FieldDiff == nil {
		t.Fatal("buildGitHubPushHooks must install a FieldDiff hook")
	}

	// A round-tripped issue carries its derived scoped labels remotely, so the
	// pair below is field-identical and yields no diff.
	local := func() *types.Issue {
		return &types.Issue{
			Title:       "same",
			Description: "same body",
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			Priority:    2,
		}
	}
	remote := func() *github.Issue {
		return &github.Issue{
			Title: "same", Body: "same body", State: "open",
			Labels: []github.Label{{Name: "type::task"}, {Name: "priority::medium"}},
		}
	}

	t.Run("identical content yields no diff", func(t *testing.T) {
		got := hooks.FieldDiff(local(), &tracker.TrackerIssue{Raw: remote()})
		if len(got) != 0 {
			t.Errorf("FieldDiff() = %v, want empty", got)
		}
	})

	t.Run("populated raw names the changed fields", func(t *testing.T) {
		l := local()
		l.Title = "changed"
		gh := remote()
		gh.State = "closed"
		got := hooks.FieldDiff(l, &tracker.TrackerIssue{Raw: gh})
		if len(got) != 2 || got[0] != "title" || got[1] != "state" {
			t.Errorf("FieldDiff() = %v, want [title state]", got)
		}
	})

	t.Run("populated raw discloses removed label names", func(t *testing.T) {
		gh := remote()
		gh.Labels = append(gh.Labels, github.Label{Name: "needs-triage"})
		got := hooks.FieldDiff(local(), &tracker.TrackerIssue{Raw: gh})
		if len(got) != 1 {
			t.Fatalf("FieldDiff() = %v, want one labels entry", got)
		}
		if !strings.Contains(got[0], "needs-triage") {
			t.Errorf("labels entry %q must name the removed label", got[0])
		}
	})

	// The degradations below are deliberate rather than accidental: the closure
	// returns nil instead of guessing, and the preview falls back to its opaque
	// line. Both halves of the `!ok || gh == nil` guard are pinned so a future
	// change to the Raw contract cannot quietly disable every dry-run detail.
	degraded := []struct {
		name string
		raw  interface{}
	}{
		{"absent raw", nil},
		{"typed-nil raw", (*github.Issue)(nil)},
		{"mistyped raw", "not-a-github-issue"},
	}
	for _, tc := range degraded {
		t.Run(tc.name+" degrades to no detail", func(t *testing.T) {
			if got := hooks.FieldDiff(local(), &tracker.TrackerIssue{Raw: tc.raw}); got != nil {
				t.Errorf("FieldDiff() = %v, want nil", got)
			}
		})
	}

	t.Run("nil remote degrades to no detail", func(t *testing.T) {
		if got := hooks.FieldDiff(local(), nil); got != nil {
			t.Errorf("FieldDiff() = %v, want nil", got)
		}
	})
}
