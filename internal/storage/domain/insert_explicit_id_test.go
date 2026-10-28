package domain

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCreateIDNamedByCaller pins which creates hand Insert an explicit id:
// one named by ExplicitID or preset on the issue. A child-counter id is not
// explicit even when the issue arrived with an id the counter replaces, and
// neither is a minted one. Insert guards only explicit ids against
// retirement; the generators already skip retired ids.
func TestCreateIDNamedByCaller(t *testing.T) {
	for _, test := range []struct {
		name   string
		params CreateIssueParams
		want   bool
	}{
		{"ExplicitID", CreateIssueParams{Issue: &types.Issue{}, ExplicitID: "bd-1"}, true},
		{"preset id", CreateIssueParams{Issue: &types.Issue{ID: "bd-2"}}, true},
		{"ExplicitID beside a parent", CreateIssueParams{Issue: &types.Issue{}, ExplicitID: "bd-3", ParentID: "bd-p"}, true},
		{"child counter replaces a preset id", CreateIssueParams{Issue: &types.Issue{ID: "bd-4"}, ParentID: "bd-p"}, false},
		{"child counter", CreateIssueParams{Issue: &types.Issue{}, ParentID: "bd-p"}, false},
		{"minted", CreateIssueParams{Issue: &types.Issue{}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := createIDNamedByCaller(test.params); got != test.want {
				t.Fatalf("createIDNamedByCaller() = %v, want %v", got, test.want)
			}
		})
	}
}
