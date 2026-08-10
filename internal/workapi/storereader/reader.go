// Package storereader holds the store-backed implementation of
// issueops.Reader: one shared body that every store-shaped backend's
// IssueReader accessor hands back.
//
// It is a package of its own rather than a file in internal/workapi for one
// reason. internal/workapi is the builders' home and 22 cmd/bd files import
// it, so a constructor living there was a one-line drop-in replacement for
// store.IssueReader() from any front door — and one that silently skips the
// telemetry decorator's reader-level spans, because a decorator adds its layer
// in its own accessor. Down here the only importers are the two Dolt store
// packages, and the cmd-bd-role-constructors depguard rule in .golangci.yml
// makes a front door importing it a lint failure rather than a review comment.
//
// The accessor is the door. This is the thing behind it.
package storereader

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// New returns the issue-query surface backed by a store handle. *DoltStore and
// *EmbeddedDoltStore answer identically because the difference between them is
// below storage.DoltStorage, not above it.
//
// The reader supplies its own ConfigSource from the store it already holds, so
// a caller ON THE ROLE cannot half-perform the "load config, build filter,
// execute" ritual: it has no way to reach the pieces.
//
// THE BOUNDARY, stated once here because this is where the constructor lives:
// the store-backed role answers `bd show --json` and `bd list` on the DIRECT
// route, and the HTTP surface and both commands' proxied routes reach the
// uow-backed one. List below is therefore a traveled path on the most-run
// command in the tree, which it was not before: `bd list` used to build the
// filter and run this body's steps longhand.
//
// Ready below is still reached only by tests and by the HTTP surface. `bd
// ready` calls the workapi builders directly because it consumes the FILTER
// for --claim, --gated, --explain and --mol; see issueops.Reader's doc comment
// for why routing only its JSON path through the role would be worse.
func New(store storage.DoltStorage) (issueops.Reader, error) {
	if store == nil {
		return nil, &issueops.ErrUnsupported{Op: "storereader.New", Backend: "nil"}
	}
	return &storeReader{store: store}, nil
}

type storeReader struct{ store storage.DoltStorage }

var _ issueops.Reader = (*storeReader)(nil)

// OFFSET IS PAGED HERE, NOT IN THE QUERY. The store seam renders LIMIT without
// OFFSET (internal/storage/issueops/ready_work.go, search_counts.go), so this
// body reaches past the skipped rows and drops them after the display order —
// workapi.WithRowsBeforeThePage onto the filter, workapi.FinishPageAt at the
// end. Its unit-of-work sibling does exactly the same thing, and FinishPageAt
// says why it does it there too rather than pushing a skip its seam could
// render: the rows have to be dropped in the caller's order, and a MaxRows cap
// has to count them.

func (r *storeReader) Ready(ctx context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	filter, err := workapi.BuildReadyFilter(req)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	limit := filter.Limit
	filter = workapi.WithReadyRowsBeforeThePage(filter, req.Offset)
	if filter.Limit > 0 {
		// The store seam has no HasMore of its own, so ask for one row past the
		// page — which the line above has already widened to cover the rows the
		// epilogue skips — and let the extra row's presence be the answer.
		filter.Limit++
	}
	items, err := r.store.GetReadyWorkWithCounts(ctx, filter)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	// Ready has no display order to apply — the ordering is the sort POLICY
	// the query ran under — so the epilogue's sort is a no-op here and only
	// its skip, its trim and its verdict do any work. It is still the shared
	// one: a second trim written out longhand is how the two arms of List came
	// apart.
	items, hasMore := workapi.FinishPageAt(items, "", false, req.Offset, limit, false)
	return issueops.IssuePage{Items: items, HasMore: hasMore}, nil
}

// List answers one issue listing.
//
// The two knobs this body and its unit-of-work sibling once answered opposite
// ways are both honored here now, and neither is honored by anything written
// below. MaxRows rides on the filter the shared builder produces and the search
// path enforces it after the scan (internal/storage/issueops,
// EnforceMaxRowsCap), so the answer is *ErrTooManyRows instead of a page.
// Offset rides on the two workapi calls this method already made for the
// has-more probe row — one widens the bound, the other cuts the page.
func (r *storeReader) List(ctx context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	cfg, err := workapi.LoadStoreListConfig(ctx, r.store)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	filter, err := workapi.BuildListFilter(req, cfg)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	// Reach past the rows the epilogue skips before the probe row is sized, so
	// the cap the seam enforces counts every row the query matched.
	filter = workapi.WithFetchOneExtra(workapi.WithRowsBeforeThePage(filter, req.Offset))

	var items []*types.IssueWithCounts
	if req.ReadyFlag {
		items, err = r.store.GetReadyWorkWithCounts(ctx, workapi.ReadyFilterFromIssueFilter(filter))
	} else {
		items, err = r.store.SearchIssuesWithCounts(ctx, "", filter)
	}
	if err != nil {
		return issueops.IssuePage{}, err
	}

	// The sort, the skip, the trim and the HasMore verdict are
	// workapi.FinishPageAt's, not this implementation's: `bd list` on both its
	// routes and the uow-backed sibling of this method call the same function,
	// so the only thing left that can differ between a CLI listing and an HTTP
	// one is presentation. This seam reports no HasMore of its own, so the
	// over-fetched row above is what speaks.
	items, hasMore := workapi.FinishPageAt(items, req.SortBy, req.Reverse, req.Offset, workapi.PageLimit(req), false)
	return issueops.IssuePage{Items: items, HasMore: hasMore}, nil
}

func (r *storeReader) Get(ctx context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	src := workapi.NewStoreDetailSource(r.store)
	issue, isWisp, err := workapi.GetIssueOrWisp(ctx, src, req.ID)
	if err != nil {
		return nil, err
	}
	return workapi.BuildIssueDetails(ctx, src, issue, isWisp, workapi.DetailOptions{
		IncludeDependents: req.IncludeDependents,
		IncludeComments:   req.IncludeComments,
		BriefDeps:         req.BriefDeps,
	})
}
