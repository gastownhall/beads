package issueops

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
)

// TestDependencyHierarchyConflictAdviceNamesItsTools pins the DESCENDANT
// refusal's ADVICE (GH#6817, AC-6): the hierarchy conflict still refuses a
// blocks edge onto an issue's own descendant, but the way out it names is the
// new vocabulary rather than a gate verb — `bd ready --exclude-parents` (an
// issue is hidden from ready work while it has non-closed children) and
// `bd create --waits-for <issue>` (the follow-up that waits for the subtree is
// its OWN issue, never the subtree's parent blocked from below).
//
// The ANCESTOR branch is asserted byte-for-byte UNCHANGED: its advice ("the
// gate would never clear") is about the deadlock shape itself, not about how
// to spell a follow-up, and this case must not quietly reword it.
func TestDependencyHierarchyConflictAdviceNamesItsTools(t *testing.T) {
	descendant := (&domain.DependencyHierarchyConflictError{IssueID: "bd-a", BlockerID: "bd-b"}).Error()
	for _, needle := range []string{
		// The refusal and its reasoning stand as they were (#6506's framing).
		"bd-a cannot be blocked by its descendant bd-b",
		"waiting on your own subtree is a close gate, not a blocks edge",
		// The new advice, both halves.
		"bd create --waits-for bd-a",
		"bd ready --exclude-parents",
	} {
		if !strings.Contains(descendant, needle) {
			t.Errorf("the descendant advice is missing %q:\n%s", needle, descendant)
		}
	}
	if strings.Contains(descendant, "bd gate") {
		t.Errorf("the descendant advice still steers to bd gate; the waits-for creation and the ready filter replaced it:\n%s", descendant)
	}

	ancestor := (&domain.DependencyHierarchyConflictError{IssueID: "bd-a", BlockerID: "bd-b", BlockerIsAncestor: true}).Error()
	if want := "bd-a cannot be blocked by its ancestor bd-b: bd-b cannot close until its descendants finish, so the gate would never clear"; ancestor != want {
		t.Errorf("the ancestor advice changed:\n got: %q\nwant: %q", ancestor, want)
	}
}
