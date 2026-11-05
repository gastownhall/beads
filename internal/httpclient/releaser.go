// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/releaser.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// httpReleaser is issueops.Releaser over releaseIssue (POST
// /v0/beads/issues/{id}:release) — the TWENTY-THIRD wire-backed accessor, and
// the claim's inverse.
//
// THE MAPPING IS TOTAL on the request: all four members of ReleaseRequest have a
// wire member or the path, so nothing here refuses on shape and the ledger
// carries no W- row for this operation. What the port had to decide is three
// facts about the ANSWER and one about the refusals.
//
// THE POST-STATE IS ANONYMOUS, which is the Changed ruling and the reason this
// role cannot be idempotent the way Claimer is. A claim leaves its own signature
// on the row — assignee == actor, status in_progress — so a re-claim can be told
// from a foreign one; a release leaves assignee cleared, status open and
// started_at gone, which is the same row no matter who emptied it or whether it
// was ever full. So `changed` is true on every 200 this client will ever see,
// and the three situations an idempotent release would collapse — "I already
// released this", "a reaper beat me to it", "nothing ever claimed it" — arrive
// as refusals instead. This client transcribes them rather than deciding between
// them, which is what keeps a downstream adapter able to map the ones it can
// live with onto its own quiet answer.
//
// THE REVISION RIDES ON Issue.RowVersion AND NOWHERE ELSE. types.Issue.RowVersion
// is `json:"-"`, so the row the server sends carries no token at all and the
// operation publishes it as a sibling member; this role stitches the two back
// together, because the role's own leaf says the post-release token rides on
// Issue.RowVersion and that two spellings of one token is how they come to
// disagree. It is int64 on the Go contract and a decimal STRING on the wire
// (upstream #6053, types.RevisionToken): live tokens run past 5e17, where an
// IEEE-754 double's ulp is already 64, so a JSON number would hand a lossy
// consumer a value NEAR the token that is not it — and the corruption would
// only show up as a precondition_failed on the NEXT request. parseRevision
// reads the string back, and a token it cannot read refuses the answer.
//
// ONE REFUSAL IS FLATTENED and it is ledgered rather than hidden: the role
// splits an unheld row (ErrNotClaimed) from a status that will not accept a
// release (ErrNotReleasable), and the wire spells both `not_releasable` with no
// member telling them apart. See L-release-notclaimed; the wire's own code doc
// says the flattening is deliberate, and the client answers the WIDER of the two
// rather than asserting a fact the wire never sent.
type httpReleaser struct {
	store *Store
	wire  WriteWire
}

var _ issueops.Releaser = (*httpReleaser)(nil)

// Release gives up the claim on one issue and reports the row it left behind.
//
// THE FOUR REFUSALS A CALLER ACTS ON reach it unchanged from the shared problem
// mapper, and naming them here is the point rather than documentation: an id on
// neither plane is issueops.ErrNotFound, a row that will not produce a release is
// issueops.ErrNotReleasable (the flattened pair above), a guard that missed is
// issueops.ErrAssigneeMismatch, and a foreign holder on the unconditional path is
// issueops.ErrAlreadyClaimed carrying *issueops.ClaimConflictError — the wire's
// spelling of the role's ErrNotOwner, which is the fifth refusal and the one
// this operation renames. A conditional caller never earns it: naming the holder
// REPLACES the fence, so a supervisor's release either matches or answers
// ErrAssigneeMismatch. Everything a caller downstream chooses to treat as "the
// claim is already gone" it chooses from those sentinels, so they travel whole.
func (r *httpReleaser) Release(ctx context.Context, req issueops.ReleaseRequest) (issueops.ReleaseResult, error) {
	if err := validateReleaseRequest(req); err != nil {
		return issueops.ReleaseResult{}, err
	}

	body := apigen.ReleaseIssueRequest{Actor: req.Actor}
	if req.ExpectedAssignee != nil {
		// COPIED, never aliased. The role promises implementations never write
		// through a caller's request, and handing this pointer to a marshaler is
		// the kind of borrow that becomes a write when a helper is added. It is
		// also sent UNTRIMMED: the comparison forgives separators and nothing
		// else, so a padded expectation has to lose every time rather than
		// intermittently.
		expected := *req.ExpectedAssignee
		body.ExpectedAssignee = &expected
	}
	if req.Force {
		// Sent only when TRUE, because absent and `false` are the same request
		// and the operation refuses `force` beside `expected_assignee`. A client
		// that always sent the member would put both on the wire for a request
		// that asked for neither conflict — legal today, since the server tests
		// the value rather than the presence, and one server release away from
		// not being.
		force := true
		body.Force = &force
	}

	res, err := r.wire.ReleaseIssue(ctx, req.IssueID, body)
	if err != nil {
		return issueops.ReleaseResult{}, err
	}

	// A COPY of the decoded row, so the result does not alias a response body,
	// and the token stitched onto it rather than beside it.
	issue := res.Issue
	if issue.RowVersion, err = parseRevision("releaseIssue", res.Revision); err != nil {
		return issueops.ReleaseResult{}, err
	}
	return issueops.ReleaseResult{Issue: &issue, Changed: res.Changed}, nil
}

// validateReleaseRequest is the role's own request rules, restated.
//
// It is the client-side twin of internal/workapi.ValidateReleaseRequest, and it
// is redeclared rather than imported for counter.go's reason: internal/workapi
// is denied to this package by depguard, because a client that could build a
// filter is a client whose narrowing no server-side gate can observe.
// TestReleaseValidationMatchesTheSharedValidator pins the two against each other
// from a test file, where the rule does not apply — the same shape
// TestDefaultListLimitMatchesTheSharedDefault uses, and the answer to
// readyclaimer.go's warning that a restated rule is how a leg drifts.
//
// EVERY ONE OF THESE IS RAISED BEFORE THE DIAL, which is this seam's standing
// rule and sharper here than usual: the server refuses all three at the edge
// with a 400 the mapper turns into the same ErrValidation, so a client that
// skipped the check would still pass the contract — while spending a round trip
// to be told what the role's own contract already calls invalid, and binding the
// classification to a problem body a future server release might spell
// differently.
func validateReleaseRequest(req issueops.ReleaseRequest) error {
	if strings.TrimSpace(req.Actor) == "" {
		return fmt.Errorf("%w: release requires an actor to attribute it to", issueops.ErrValidation)
	}
	if strings.TrimSpace(req.IssueID) == "" {
		return fmt.Errorf("%w: release requires an issue id", issueops.ErrValidation)
	}
	if req.ExpectedAssignee != nil {
		// A non-nil pointer to "" is NOT "expected unassigned" here, unlike
		// UpdateRequest.ExpectedAssignee: releasing a row nobody holds is not a
		// release at all, and a caller that wants to assert a row is unheld is
		// asking a READER a question.
		if strings.TrimSpace(*req.ExpectedAssignee) == "" {
			return fmt.Errorf("%w: expected assignee must name a holder; there is no release of an unheld issue",
				issueops.ErrValidation)
		}
		if req.Force {
			return fmt.Errorf("%w: force releases whoever holds the issue and expected-assignee releases only a named holder; a request cannot ask for both",
				issueops.ErrValidation)
		}
	}
	return nil
}
