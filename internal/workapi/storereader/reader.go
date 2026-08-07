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

// offsetBackend names the backend an Offset refusal comes from. One name for
// both engines on purpose: the server-backed and embedded stores hand back
// THIS body, and it is the body — not the engine underneath it — that cannot
// page by offset.
const offsetBackend = "dolt-store"

// refuseOffset is the shared refusal. The store seam renders LIMIT without
// OFFSET (internal/storage/issueops/ready_work.go, search_counts.go), so a
// request carrying one used to come back as the unpaged first page with no
// error on it — a caller paging with Offset saw the same rows forever. The
// field is not dead: `bd ready/list/query --offset` are real flags, and their
// direct routes already reject a non-zero offset before reaching any of this
// (cmd/bd/ready.go, list.go, query.go). This is the same refusal one layer
// down, for the callers that are not the CLI.
func refuseOffset(op string) error {
	return &issueops.ErrUnsupported{Op: op, Backend: offsetBackend}
}

func (r *storeReader) Ready(ctx context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	if req.Offset != 0 {
		return issueops.IssuePage{}, refuseOffset("Reader.Ready(Offset)")
	}
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

// List answers one issue listing.
//
// The two knobs this body and its unit-of-work sibling answer differently sit
// side by side here and point opposite ways. Offset is refused below because
// this seam renders LIMIT without OFFSET. MaxRows is HONORED, and nothing
// below mentions it: the cap rides on the filter the shared builder produces,
// and the search path enforces it after the scan (internal/storage/issueops,
// EnforceMaxRowsCap), so the answer is *ErrTooManyRows instead of a page. The
// sibling refuses it, for the reason stated there.
func (r *storeReader) List(ctx context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	if req.Offset != 0 {
		return issueops.IssuePage{}, refuseOffset("Reader.List(Offset)")
	}
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
