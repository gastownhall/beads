package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func updateWarningIssue() *types.Issue {
	return &types.Issue{
		ID:       "bd-gw-1",
		Status:   types.StatusOpen,
		Assignee: "alice",
	}
}

// TestUpdateCloseWarning pins the advisory sentence a successful
// `bd update <id> --status closed` prints when the observed pre-image would
// have made `bd close` refuse (GH#5787 issue 3): the warning names the issue
// and the assignee/actor guard difference, is silent for force, matching
// actors, restatements, templates and guard-passing pre-images, and reports
// the pin ahead of the assignee when both apply.
func TestUpdateCloseWarning(t *testing.T) {
	t.Run("assignee mismatch names the issue, assignee and actor", func(t *testing.T) {
		w := updateCloseGuardWarning("bd-gw-1", updateWarningIssue(), "bob", false)
		if w == "" {
			t.Fatal("expected a warning for a pre-image bd close would refuse")
		}
		for _, want := range []string{
			"update closed bd-gw-1",
			"bd close would refuse the observed pre-update state",
			`assignee is "alice", actor is "bob"`,
		} {
			if !strings.Contains(w, want) {
				t.Fatalf("warning should contain %q; got:\n%s", want, w)
			}
		}
		if strings.Contains(w, "--force") {
			t.Fatalf("the warning must not recommend --force, which also bypasses close policy:\n%s", w)
		}
	})

	t.Run("matching actor is silent", func(t *testing.T) {
		if w := updateCloseGuardWarning("bd-gw-1", updateWarningIssue(), "alice", false); w != "" {
			t.Fatalf("expected silence for the assignee's own close; got:\n%s", w)
		}
	})

	t.Run("unassigned issue is silent", func(t *testing.T) {
		issue := updateWarningIssue()
		issue.Assignee = ""
		if w := updateCloseGuardWarning("bd-gw-1", issue, "bob", false); w != "" {
			t.Fatalf("expected silence for an unassigned issue; got:\n%s", w)
		}
	})

	t.Run("pin warns, either spelling, and ahead of the assignee", func(t *testing.T) {
		pinnedColumn := updateWarningIssue()
		pinnedColumn.Pinned = true
		w := updateCloseGuardWarning("bd-gw-1", pinnedColumn, "bob", false)
		if !strings.Contains(w, "issue is pinned") {
			t.Fatalf("expected the pin sentence; got:\n%s", w)
		}

		pinnedStatus := updateWarningIssue()
		pinnedStatus.Status = types.StatusPinned
		if w := updateCloseGuardWarning("bd-gw-1", pinnedStatus, "bob", false); !strings.Contains(w, "issue is pinned") {
			t.Fatalf("expected the pin sentence for the pinned-status spelling; got:\n%s", w)
		}

		both := updateWarningIssue()
		both.Pinned = true
		w = updateCloseGuardWarning("bd-gw-1", both, "bob", false)
		if !strings.Contains(w, "issue is pinned") || strings.Contains(w, "assignee is") {
			t.Fatalf("the pin is reported first when both fences trip; got:\n%s", w)
		}
	})

	t.Run("force, restatements and templates are silent", func(t *testing.T) {
		if w := updateCloseGuardWarning("bd-gw-1", updateWarningIssue(), "bob", true); w != "" {
			t.Fatalf("--force bypasses the same fences in both commands; got:\n%s", w)
		}

		restated := updateWarningIssue()
		restated.Status = types.StatusClosed
		if w := updateCloseGuardWarning("bd-gw-1", restated, "bob", false); w != "" {
			t.Fatalf("an already-closed pre-image is a restatement; got:\n%s", w)
		}

		if w := updateCloseGuardWarning("bd-gw-1", nil, "bob", false); w != "" {
			t.Fatalf("a nil pre-image has no observed state to warn about; got:\n%s", w)
		}

		template := updateWarningIssue()
		template.IsTemplate = true
		if w := updateCloseGuardWarning("bd-gw-1", template, "bob", false); w != "" {
			t.Fatalf("a template pre-image is refused by the mutation itself; got:\n%s", w)
		}
	})
}
