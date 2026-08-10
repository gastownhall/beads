package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// IssueReaderSource is the capability accessor a unit-of-work provider offers.
// It is named here so a consumer that holds a provider by interface can ask
// for the role the same way a consumer holding a store does, instead of
// reaching for a constructor: the accessor IS the API on both seams, and a
// provider that cannot answer says so with an error rather than being wired
// around.
type IssueReaderSource interface {
	IssueReader() (publicops.Reader, error)
}

// issueReader runs public issue queries through a unit of work.
type issueReader struct {
	provider UnitOfWorkProvider
}

// IssueReader returns the guarded issue-query surface for this provider. A
// unit of work is not a special case: callers reach the queries through the
// same accessor they use on a store.
func (p *doltSQLProvider) IssueReader() (publicops.Reader, error) {
	return NewIssueReader(p)
}

// NewIssueReader constructs public issue queries backed by provider.
func NewIssueReader(provider UnitOfWorkProvider) (publicops.Reader, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new issue reader: unit-of-work provider must not be nil")
	}
	return &issueReader{provider: provider}, nil
}

var _ publicops.Reader = (*issueReader)(nil)

// Ready answers one ready-work query inside one unit of work.
//
// ONE UOW PER CALL is the whole transaction model here, and it is a
// consequence of the role's shape rather than a policy bolted onto it: the
// methods are request-granular, so a request and a transaction are the same
// span. There is no commit — reads never commit — and RunTxRead owns the
// detached rollback, so a caller that hangs up mid-response cannot burn a
// pinned session.
func (r *issueReader) Ready(ctx context.Context, req publicops.ReadyRequest) (publicops.IssuePage, error) {
	// Wake expired dated defers before reading, in a write UOW of its own —
	// this read's span never commits (see the doc comment above), so the
	// sweep cannot live inside it.
	WakeExpiredDefersAdvisory(ctx, r.provider)
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (publicops.IssuePage, error) {
		filter, err := workapi.BuildReadyFilter(req)
		if err != nil {
			return publicops.IssuePage{}, err
		}
		limit := filter.Limit
		// Reach past the rows the epilogue skips, and take the offset off the
		// query. This seam COULD render it, and deliberately is not asked to;
		// see FinishPageAt.
		filter = workapi.WithReadyRowsBeforeThePage(filter, req.Offset)
		page, err := uw.IssueUseCase().GetReadyWorkWithCounts(ctx, filter)
		if err != nil {
			return publicops.IssuePage{}, err
		}
		// The same epilogue the sibling implementation runs, through the same
		// function. Ready has no display order to apply and this seam reports
		// HasMore natively, so only the skip and the trim do any work here —
		// which is exactly why it must be the shared one rather than a local
		// shortcut: the two arms of List below came apart precisely by one of
		// them deciding its epilogue was unnecessary.
		items, hasMore := workapi.FinishPageAt(page.Items, "", false, req.Offset, limit, page.HasMore)
		return publicops.IssuePage{Items: items, HasMore: hasMore}, nil
	})
}

func (r *issueReader) List(ctx context.Context, req publicops.ListRequest) (publicops.IssuePage, error) {
	// A --ready listing is the ready front by another door, so it wakes
	// expired dated defers the same way Ready does — before the read span,
	// which never commits.
	if req.ReadyFlag {
		WakeExpiredDefersAdvisory(ctx, r.provider)
	}
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (publicops.IssuePage, error) {
		// The config source comes from the unit of work this call already
		// holds, so a caller reaching this method through the role has nothing
		// to supply and no step to skip. `bd list --proxied-server` gets its
		// page this way in every mode but --watch and the hierarchical --parent
		// tree; those two still open their own unit of work because they consume
		// the FILTER rather than a page (see issueops.Reader's doc comment).
		cfg, err := workapi.LoadUOWListConfig(ctx, uw)
		if err != nil {
			return publicops.IssuePage{}, err
		}
		filter, err := workapi.BuildListFilter(req, cfg)
		if err != nil {
			return publicops.IssuePage{}, err
		}
		// Reach past the rows the epilogue skips, and take the offset off the
		// query. The MaxRows cap rides the filter untouched from here: the
		// domain/db query path sizes its bound and enforces the cap through the
		// same two functions the store-backed seam uses
		// (issueops.SearchProbeLimit, EnforceMaxRowsCap), so the same request
		// trips the same breaker on either implementation.
		filter = workapi.WithRowsBeforeThePage(filter, req.Offset)
		// WHICH QUERY is the only thing --ready changes. The epilogue below is
		// deliberately outside the branch: when it lived inside the non-ready
		// arm only, the two arms of one contract method answered in different
		// orders, and a --ready page under a sort the database cannot express
		// came back untrimmed. The sibling implementation
		// (workapi.storeReader.List) sorts and trims both arms, so the split
		// was drift between two implementations of one method — seeded inside
		// the seam built to eliminate drift.
		var page domain.SearchCountsPage
		if req.ReadyFlag {
			page, err = uw.IssueUseCase().GetReadyWorkWithCounts(ctx, workapi.ReadyFilterFromIssueFilter(filter))
		} else {
			page, err = uw.IssueUseCase().SearchIssuesWithCounts(ctx, "", filter)
		}
		if err != nil {
			return publicops.IssuePage{}, err
		}

		// This seam reports HasMore natively, so the epilogue's seed is that
		// verdict rather than an over-fetched row — the one thing that differs
		// between this implementation and its store-backed sibling, and it is
		// an argument to the shared function rather than a second copy of it.
		items, hasMore := workapi.FinishPageAt(page.Items, req.SortBy, req.Reverse, req.Offset, workapi.PageLimit(req), page.HasMore)
		return publicops.IssuePage{Items: items, HasMore: hasMore}, nil
	})
}

func (r *issueReader) Get(ctx context.Context, req publicops.GetRequest) (*publicops.IssueDetails, error) {
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (*publicops.IssueDetails, error) {
		src := workapi.NewUOWDetailSource(uw)
		issue, isWisp, err := workapi.GetIssueOrWisp(ctx, src, req.ID)
		if err != nil {
			return nil, err
		}
		return workapi.BuildIssueDetails(ctx, src, issue, isWisp, workapi.DetailOptions{
			IncludeDependents: req.IncludeDependents,
			IncludeComments:   req.IncludeComments,
			BriefDeps:         req.BriefDeps,
		})
	})
}
