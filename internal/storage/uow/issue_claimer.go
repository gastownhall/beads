package uow

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// IssueClaimerSource is the capability accessor a unit-of-work provider offers
// for the guarded atomic claim. It is named here for the same reason
// IssueReaderSource is: a consumer holding a provider by interface asks it for
// the role instead of reaching for a constructor, and a provider that cannot
// answer says so with an error rather than being wired around.
type IssueClaimerSource interface {
	IssueClaimer() (publicops.Claimer, error)
}

// issueClaimer runs the public claim through a unit of work.
type issueClaimer struct {
	provider UnitOfWorkProvider
}

// IssueClaimer returns the guarded atomic-claim surface for this provider. A
// unit of work is not a special case: callers reach the claim through the same
// accessor they use on a store.
func (p *doltSQLProvider) IssueClaimer() (publicops.Claimer, error) {
	return NewIssueClaimer(p)
}

// NewIssueClaimer constructs the public claim backed by provider.
func NewIssueClaimer(provider UnitOfWorkProvider) (publicops.Claimer, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new issue claimer: unit-of-work provider must not be nil")
	}
	return &issueClaimer{provider: provider}, nil
}

var _ publicops.Claimer = (*issueClaimer)(nil)

// Claim runs the compare-and-set in a retried unit-of-work transaction.
//
// RETRY LIVES HERE, not in the caller. RunTxResult redoes the WHOLE attempt in
// a FRESH unit of work when one loses Dolt's commit-time merge, because
// re-committing a session the server already rolled back is a lost write. That
// is the same place every other verb on this seam keeps it, and it is what
// lets the role promise that a lost merge is retried rather than surfaced.
func (c *issueClaimer) Claim(ctx context.Context, request publicops.ClaimRequest) (publicops.ClaimResult, error) {
	if request.Actor == "" || request.IssueID == "" {
		return publicops.ClaimResult{}, validationError(fmt.Errorf("claim: actor and issue ID must not be empty"))
	}
	return RunTxResult(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.ClaimResult, string, error) {
		uc := uw.IssueUseCase()
		claimed, err := uc.ClaimIssue(ctx, request.IssueID, request.Actor)
		if err != nil {
			return publicops.ClaimResult{}, "", classifyClaimError(ctx, uc, request.IssueID, err)
		}
		// Read back INSIDE this transaction, so the result describes the row
		// this CAS wrote and not a later writer's.
		issue, err := uc.GetIssue(ctx, request.IssueID)
		if err != nil {
			return publicops.ClaimResult{}, "", err
		}
		if issue == nil {
			// A miss with a nil error is the other shape a not-found takes at
			// this seam; normalize it rather than dereferencing nil.
			return publicops.ClaimResult{}, "", fmt.Errorf("%w: issue %s", publicops.ErrNotFound, request.IssueID)
		}
		if claimed.AlreadyClaimed {
			// The idempotent re-claim: the CAS matched no row because there
			// was nothing to change. An empty commit message tells
			// RunTxResult to skip the commit, so a polling caller cannot mint
			// an empty storage commit per call.
			return publicops.ClaimResult{Issue: issue}, "", nil
		}
		return publicops.ClaimResult{Issue: issue, Changed: true}, storageissueops.ClaimCommitMessage(request.IssueID, request.Actor), nil
	})
}

// classifyClaimError normalizes what a lost or impossible claim reports.
//
// A refusal gains the assignee and status read in THIS transaction, so a
// caller classifies the conflict from typed fields instead of matching
// substrings in the message; when that read fails the refusal stands
// unadorned, because reporting the read's failure would replace a precise
// conflict with an opaque one. A missing row arrives here as a wrapped
// sql.ErrNoRows and leaves as ErrNotFound, which is what the role promises and
// what its store-backed sibling already answers with — a wisp id takes exactly
// that route, since the CAS only ever addresses the issues table.
func classifyClaimError(ctx context.Context, uc domain.IssueUseCase, id string, err error) error {
	switch {
	case errors.Is(err, publicops.ErrAlreadyClaimed), errors.Is(err, publicops.ErrNotClaimable):
		issue, readErr := uc.GetIssue(ctx, id)
		if readErr != nil || issue == nil {
			return err
		}
		return &publicops.ClaimConflictError{IssueID: id, Assignee: issue.Assignee, Status: issue.Status, Err: err}
	case errors.Is(err, publicops.ErrNotFound):
		return err
	case dberrors.IsNoRows(err):
		return fmt.Errorf("%w: issue %s", publicops.ErrNotFound, id)
	default:
		return err
	}
}
