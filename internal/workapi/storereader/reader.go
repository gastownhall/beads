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
// packages, and the cmd-bd-reader-constructor depguard rule in .golangci.yml
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
// today the store-backed role answers `bd show --json` on the direct route,
// and the HTTP surface and the proxied `bd show --json` reach the uow-backed
// one. `bd ready` and `bd list` still call the workapi builders directly —
// they consume the FILTER for the max-rows cap, --claim, --watch, the
// hierarchical --parent tree and the text renderings — so Ready and List below
// are reached only by tests until a front door moves. They are covered by
// reader_test.go for exactly that reason, and their page epilogue is
// workapi.FinishPage, which `bd list` DOES run in every mode but the
// hierarchical --parent tree, so the untravelled path here is the query and
// not the tail. See issueops.Reader's doc comment for why routing only those
// commands' JSON paths through the role would be worse.
func New(store storage.DoltStorage) (issueops.Reader, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "storereader.New", Backend: "nil"}
	}
	return &storeReader{store: store}, nil
}

type storeReader struct{ store storage.DoltStorage }

var _ issueops.Reader = (*storeReader)(nil)

func (r *storeReader) Ready(ctx context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	filter, err := workapi.BuildReadyFilter(req)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	limit := filter.Limit
	if limit > 0 {
		// The store seam has no HasMore of its own, so ask for one row past
		// the page and let its presence be the answer.
		filter.Limit = limit + 1
	}
	items, err := r.store.GetReadyWorkWithCounts(ctx, filter)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	// Ready has no display order to apply — the ordering is the sort POLICY
	// the query ran under — so the epilogue's sort is a no-op here and only
	// its trim and its verdict do any work. It is still the shared one: a
	// second trim written out longhand is how the two arms of List came apart.
	items, hasMore := workapi.FinishPage(items, "", false, limit, false)
	return issueops.IssuePage{Items: items, HasMore: hasMore}, nil
}

func (r *storeReader) List(ctx context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	cfg, err := workapi.LoadStoreListConfig(ctx, r.store)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	filter, err := workapi.BuildListFilter(req, cfg)
	if err != nil {
		return issueops.IssuePage{}, err
	}

	var items []*types.IssueWithCounts
	if req.ReadyFlag {
		items, err = r.store.GetReadyWorkWithCounts(ctx, workapi.ReadyFilterFromIssueFilter(workapi.WithFetchOneExtra(filter)))
	} else {
		items, err = r.store.SearchIssuesWithCounts(ctx, "", workapi.WithFetchOneExtra(filter))
	}
	if err != nil {
		return issueops.IssuePage{}, err
	}

	// The sort, the trim and the HasMore verdict are workapi.FinishPage's, not
	// this implementation's: `bd list` on both its routes and the uow-backed
	// sibling of this method call the same function, so the only thing left
	// that can differ between a CLI listing and an HTTP one is presentation.
	// This seam reports no HasMore of its own, so the over-fetched row above
	// is what speaks.
	items, hasMore := workapi.FinishPage(items, req.SortBy, req.Reverse, workapi.PageLimit(req), false)
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
	})
}
