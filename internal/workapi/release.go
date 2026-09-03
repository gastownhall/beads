package workapi

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/issueops"
)

// The shared, DATABASE-FREE half of issueops.Releaser: what a release request
// means before anything is read.
//
// Every implementation runs it, so `bd unclaim` has one definition of a
// malformed request rather than one per backend, and a refused request costs no
// database work anywhere.
//
// What is NOT here is the release. Classifying the refusals needs the row, and
// the row and the release must see one snapshot
// (issueops.Releaser.Release); that body is
// internal/storage/issueops.ReleaseIssueInTx, which all three legs reach.

// ValidateReleaseRequest applies the request rules every Releaser
// implementation shares.
func ValidateReleaseRequest(in issueops.ReleaseRequest) error {
	if strings.TrimSpace(in.Actor) == "" {
		return fmt.Errorf("%w: release requires an actor to attribute it to", issueops.ErrValidation)
	}
	if strings.TrimSpace(in.IssueID) == "" {
		return fmt.Errorf("%w: release requires an issue id", issueops.ErrValidation)
	}
	if in.ExpectedAssignee != nil {
		// A non-nil pointer to "" is NOT "expected unassigned" here, unlike
		// UpdateRequest.ExpectedAssignee: releasing a row nobody holds is not a
		// release, and the raw seam beneath this role refuses the empty
		// expectation in as many words.
		if strings.TrimSpace(*in.ExpectedAssignee) == "" {
			return fmt.Errorf("%w: expected assignee must name a holder; there is no release of an unheld issue",
				issueops.ErrValidation)
		}
		// The two are answers to the same question and they disagree, which is
		// the rule UpdateRequest states for ForceAssigneeTransfer beside its
		// own ExpectedAssignee.
		if in.Force {
			return fmt.Errorf("%w: force releases whoever holds the issue and expected-assignee releases only a named holder; a request cannot ask for both",
				issueops.ErrValidation)
		}
	}
	return nil
}
