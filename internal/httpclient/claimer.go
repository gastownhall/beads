// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/claimer.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// httpClaimer serves issueops.Claimer from the claimIssue operation (design D8
// row 3). The role is a single guarded compare-and-set and the wire operation is
// the same one, so this is the thinnest mapping on the whole client: a body
// carrying the actor, and a response whose already_claimed flag is the role's
// Changed inverted.
type httpClaimer struct {
	store *Store
	wire  WriteWire
}

var _ issueops.Claimer = (*httpClaimer)(nil)

// Claim dials claimIssue.
//
// The 409 vocabulary reaches the caller as *issueops.ClaimConflictError with the
// holder and status the refusing transaction read, reconstructed from the
// problem body's assignee/issue_status extension members — the wire's mapper
// owns that, and the issue id it needs comes from the request rather than the
// response because the server does not echo what the caller already said.
func (c *httpClaimer) Claim(ctx context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	if err := requireActor(req.Actor); err != nil {
		return issueops.ClaimResult{}, err
	}
	if err := requireID("issue id", req.IssueID); err != nil {
		return issueops.ClaimResult{}, err
	}

	res, err := c.wire.ClaimIssue(ctx, req.IssueID, apigen.ClaimRequest{Actor: req.Actor})
	if err != nil {
		return issueops.ClaimResult{}, err
	}
	// The role's snapshot is the bare issue ROW — no labels, no edges, no
	// comments — which is exactly what the wire's ClaimResponse carries, so
	// there is nothing to hydrate and nothing to strip.
	issue := res.Issue
	return issueops.ClaimResult{Issue: &issue, Changed: !res.AlreadyClaimed}, nil
}
