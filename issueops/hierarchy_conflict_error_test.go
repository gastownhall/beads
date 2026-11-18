package issueops_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestDependencyHierarchyConflictErrorNamesWorkingRemedies pins GH#6817's
// message fix. The descendant refusal used to send users to "bd gate", which
// cannot express "this parent waits on its children" and left the parent in
// bd ready. It now names the two remedies that work: excluding such parents
// from ready work, and a waits-for edge on the parent's children.
func TestDependencyHierarchyConflictErrorNamesWorkingRemedies(t *testing.T) {
	descendant := (&issueops.DependencyHierarchyConflictError{IssueID: "bd-p", BlockerID: "bd-p.1"}).Error()
	for _, want := range []string{
		"bd-p cannot be blocked by its descendant bd-p.1",
		"bd ready --exclude-parents",
		"bd create --waits-for bd-p",
	} {
		if !strings.Contains(descendant, want) {
			t.Errorf("descendant refusal %q does not contain %q", descendant, want)
		}
	}
	if strings.Contains(descendant, "bd gate") {
		t.Errorf("descendant refusal %q still recommends bd gate", descendant)
	}

	ancestor := (&issueops.DependencyHierarchyConflictError{IssueID: "bd-p.1", BlockerID: "bd-p", BlockerIsAncestor: true}).Error()
	const wantAncestor = "bd-p.1 cannot be blocked by its ancestor bd-p: bd-p cannot close until its descendants finish, so the gate would never clear"
	if ancestor != wantAncestor {
		t.Errorf("ancestor refusal = %q, want it unchanged: %q", ancestor, wantAncestor)
	}
}
