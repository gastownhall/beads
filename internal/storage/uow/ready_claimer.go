package uow

import (
	"context"
	"fmt"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// ReadyClaimerSource is the capability accessor a unit-of-work provider offers
// for the claim role, named here for the same reason IssueReaderSource is: a
// consumer holding a provider by interface asks for the role instead of
// reaching for a constructor.
type ReadyClaimerSource interface {
	ReadyClaimer() (publicops.ReadyClaimer, error)
}

// readyClaimer takes ready work through a unit of work.
type readyClaimer struct {
	provider UnitOfWorkProvider
}

// ReadyClaimer returns the guarded take-ready-work surface for this provider.
func (p *doltSQLProvider) ReadyClaimer() (publicops.ReadyClaimer, error) {
	return NewReadyClaimer(p)
}

// NewReadyClaimer constructs a public ready claimer backed by provider.
func NewReadyClaimer(provider UnitOfWorkProvider) (publicops.ReadyClaimer, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new ready claimer: unit-of-work provider must not be nil")
	}
	return &readyClaimer{provider: provider}, nil
}

var _ publicops.ReadyClaimer = (*readyClaimer)(nil)

// ClaimNext selects, claims and hydrates inside one unit of work.
//
// ONE UOW PER CALL, as it is for the reader: the method is request-granular,
// so a request and a transaction are the same span. A claim that found nothing
// names no commit message, which is how RunTxResult is told to commit nothing.
func (c *readyClaimer) ClaimNext(ctx context.Context, request publicops.ClaimNextRequest) (publicops.ClaimNextResult, error) {
	if err := storageissueops.ValidateClaimNextRequest(request); err != nil {
		return publicops.ClaimNextResult{}, err
	}
	// The same builder the sibling implementation and Reader.Ready run.
	filter, err := workapi.BuildReadyFilter(request.Filter)
	if err != nil {
		return publicops.ClaimNextResult{}, err
	}
	// Wake expired dated defers before selecting, so a bead whose snooze just
	// ended is claimable the moment its date passes rather than after some
	// other session lists the front. Advisory, in a UOW of its own: a failed
	// sweep must not cost the claim.
	WakeExpiredDefersAdvisory(ctx, c.provider)
	return RunTxResult(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.ClaimNextResult, string, error) {
		claimed, err := ClaimNextInUOW(ctx, uw, request.Actor, filter)
		if err != nil {
			return publicops.ClaimNextResult{}, "", err
		}
		if claimed == nil {
			return publicops.ClaimNextResult{}, "", nil
		}
		// The same id-bearing message the store-backed sibling writes. The
		// proxied route used to spell it "bd: ready --claim <id>" while the
		// direct route wrote "bd: claim ready <id>"; one operation gets one
		// entry, so the role settles it on the spelling both other backends
		// already used.
		return publicops.ClaimNextResult{Claimed: claimed},
			storageissueops.ClaimNextCommitMessage(claimed.ID), nil
	})
}

// ClaimNextInUOW claims the first ready issue matching filter in uw and
// hydrates it there. It is shared with the batch closer, whose claim runs in
// the closes' transaction rather than one of its own — the same selection, the
// same hydration, reached the one way.
//
// A nil issue with a nil error is the ordinary empty-front outcome.
func ClaimNextInUOW(ctx context.Context, uw UnitOfWork, actor string, filter types.WorkFilter) (*types.IssueWithCounts, error) {
	claimed, err := uw.IssueUseCase().ClaimReadyIssue(ctx, filter, actor)
	if err != nil {
		return nil, err
	}
	if !claimed.Claimed || claimed.Issue == nil {
		return nil, nil
	}
	return hydrateReadyRow(ctx, uw, claimed.Issue)
}

// hydrateReadyRow fills in the relationship cardinalities a ready row carries,
// reading them in the caller's unit of work so the counts describe the state
// that transaction is about to commit. A failed count read is an error rather
// than a zero, matching the store-backed sibling: a result nobody can hydrate
// is not a result.
func hydrateReadyRow(ctx context.Context, uw UnitOfWork, issue *types.Issue) (*types.IssueWithCounts, error) {
	ids := []string{issue.ID}
	depCounts, err := uw.DependencyUseCase().CountsByIssueIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate ready row %s: dependency counts: %w", issue.ID, err)
	}
	records, err := uw.DependencyUseCase().GetForIssueIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate ready row %s: dependency records: %w", issue.ID, err)
	}
	commentCounts, err := uw.CommentUseCase().GetCommentCounts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate ready row %s: comment counts: %w", issue.ID, err)
	}

	issue.Dependencies = records[issue.ID]
	counts := depCounts[issue.ID]
	if counts == nil {
		counts = &types.DependencyCounts{}
	}
	var parent *string
	for _, dep := range records[issue.ID] {
		if dep.Type == types.DepParentChild {
			parent = &dep.DependsOnID
			break
		}
	}
	return &types.IssueWithCounts{
		Issue:           issue,
		DependencyCount: counts.DependencyCount,
		DependentCount:  counts.DependentCount,
		CommentCount:    commentCounts[issue.ID],
		Parent:          parent,
	}, nil
}
