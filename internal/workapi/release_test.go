package workapi

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// The database-free half of issueops.Releaser, pinned in milliseconds. Every
// backend runs this function, so a disagreement about what a malformed request
// means is caught here rather than three times over in conformance.

func TestValidateReleaseRequest(t *testing.T) {
	holder := "agent-a"
	blank := "   "
	empty := ""

	for _, test := range []struct {
		name    string
		request issueops.ReleaseRequest
		wantErr bool
	}{
		{"actor and id", issueops.ReleaseRequest{Actor: "agent-a", IssueID: "bd-1"}, false},
		{"force", issueops.ReleaseRequest{Actor: "reaper", IssueID: "bd-1", Force: true}, false},
		{"expected assignee", issueops.ReleaseRequest{Actor: "reaper", IssueID: "bd-1", ExpectedAssignee: &holder}, false},
		{"no actor", issueops.ReleaseRequest{IssueID: "bd-1"}, true},
		{"blank actor", issueops.ReleaseRequest{Actor: "  ", IssueID: "bd-1"}, true},
		{"no id", issueops.ReleaseRequest{Actor: "agent-a"}, true},
		{"blank id", issueops.ReleaseRequest{Actor: "agent-a", IssueID: "\t "}, true},
		// A non-nil pointer to "" is a real guard on
		// UpdateRequest.ExpectedAssignee and is NOT one here: there is no
		// release of an unheld issue.
		{"empty expected assignee", issueops.ReleaseRequest{Actor: "reaper", IssueID: "bd-1", ExpectedAssignee: &empty}, true},
		{"blank expected assignee", issueops.ReleaseRequest{Actor: "reaper", IssueID: "bd-1", ExpectedAssignee: &blank}, true},
		// The two answer the same question and disagree.
		{"force beside an expected assignee", issueops.ReleaseRequest{
			Actor: "reaper", IssueID: "bd-1", ExpectedAssignee: &holder, Force: true,
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateReleaseRequest(test.request)
			if test.wantErr {
				if !errors.Is(err, issueops.ErrValidation) {
					t.Fatalf("error = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}
