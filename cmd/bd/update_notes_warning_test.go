package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestWarnNotesReplacement is the D2 guard test: since the notes-overwrite
// fence, an unforced `bd update --notes` over existing notes is refused
// (errNotesOverwriteRefusal), so this warning fires only after a FORCED
// overwrite succeeded. It is the audit trail for a habitual --force that
// never saw the refusal — a statement of what happened, still naming
// --append-notes as the history-preserving alternative. The predicate
// deciding WHEN it fires is covered by TestReplacesExistingNotes.
func TestWarnNotesReplacement(t *testing.T) {
	got := captureStderr(t, func() {
		warnNotesReplacement("tc-dg6")
	})

	want := "warning: tc-dg6: --force replaced existing notes (--append-notes preserves history)\n"
	if got != want {
		t.Fatalf("warnNotesReplacement stderr = %q, want %q", got, want)
	}
}

// TestUpdateCloseWarning pins GH#5787 issue 3's advisory: after `bd update
// --status closed` succeeds over an observed pre-image `bd close` would refuse
// (pin, or another actor's assignment), one stderr line says so. It never
// recommends --force, and it is silent wherever bd close would agree.
func TestUpdateCloseWarning(t *testing.T) {
	closed := issueops.IssuePatch{Status: setField(types.StatusClosed)}
	inProgress := issueops.IssuePatch{Status: setField(types.StatusInProgress)}
	assigned := func(assignee string) *types.Issue {
		return &types.Issue{ID: "tc-cw1", Status: types.StatusOpen, Assignee: assignee}
	}

	t.Run("another actor's assignment", func(t *testing.T) {
		got := updateCloseGuardWarning("tc-cw1", assigned("alice"), closed, "bob", false)
		want := `update closed tc-cw1; bd close would refuse the observed pre-update state: assignee is "alice", actor is "bob"`
		if got != want {
			t.Fatalf("warning = %q, want %q", got, want)
		}
	})

	t.Run("pinned and assigned reports the pin first", func(t *testing.T) {
		before := assigned("alice")
		before.Pinned = true
		got := updateCloseGuardWarning("tc-cw1", before, closed, "bob", false)
		want := `update closed tc-cw1; bd close would refuse the observed pre-update state: the issue is pinned; assignee is "alice", actor is "bob"`
		if got != want {
			t.Fatalf("warning = %q, want %q", got, want)
		}
	})

	t.Run("status pinned, unassigned", func(t *testing.T) {
		before := &types.Issue{ID: "tc-cw1", Status: types.StatusPinned}
		got := updateCloseGuardWarning("tc-cw1", before, closed, "bob", false)
		want := `update closed tc-cw1; bd close would refuse the observed pre-update state: the issue is pinned`
		if got != want {
			t.Fatalf("warning = %q, want %q", got, want)
		}
	})

	silent := []struct {
		name   string
		before *types.Issue
		patch  issueops.IssuePatch
		actor  string
		force  bool
	}{
		{"own assignment", assigned("alice"), closed, "alice", false},
		{"unassigned", assigned(""), closed, "bob", false},
		{"forced", assigned("alice"), closed, "bob", true},
		{"closed restatement", &types.Issue{ID: "tc-cw1", Status: types.StatusClosed, Assignee: "alice"}, closed, "bob", false},
		{"other status", assigned("alice"), inProgress, "bob", false},
		{"no status edit", assigned("alice"), issueops.IssuePatch{}, "bob", false},
		{"template", &types.Issue{ID: "tc-cw1", Status: types.StatusOpen, Assignee: "alice", IsTemplate: true}, closed, "bob", false},
		{"no pre-image", nil, closed, "bob", false},
	}
	for _, tc := range silent {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateCloseGuardWarning("tc-cw1", tc.before, tc.patch, tc.actor, tc.force); got != "" {
				t.Fatalf("warning = %q, want none", got)
			}
		})
	}

	t.Run("printed with the warn glyph, no --force advice", func(t *testing.T) {
		got := captureStderr(t, func() {
			warnUpdateCloseGuard(`update closed tc-cw1; bd close would refuse the observed pre-update state: assignee is "alice", actor is "bob"`)
		})
		if !strings.HasSuffix(got, "update closed tc-cw1; bd close would refuse the observed pre-update state: assignee is \"alice\", actor is \"bob\"\n") || strings.Count(got, "\n") != 1 {
			t.Fatalf("stderr = %q, want one warning line", got)
		}
		if strings.Contains(got, "--force") {
			t.Fatalf("warning recommends --force: %q", got)
		}
	})
}
