package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The two seams `bd list` reaches its roles through, and the one projection its
// text renderings need. Both routes call these, so the page and its decoration
// come from one statement rather than two that happen to agree.

// listPageIssues projects a counted page onto the plain rows the text
// renderings take, keeping the page's ORDER. A nil row is dropped rather than
// dereferenced: the role promises none for a successful call, and a listing is
// not where a broken implementation should become a panic.
func listPageIssues(page issueops.IssuePage) ([]*types.Issue, bool) {
	issues := make([]*types.Issue, 0, len(page.Items))
	for _, row := range page.Items {
		if row == nil || row.Issue == nil {
			continue
		}
		issues = append(issues, row.Issue)
	}
	return issues, page.HasMore
}

// listBlocking is the decoration keyed the way the two formatters read it.
//
// The role answers with a SLICE in request order, because that is what an HTTP
// body needs. The formatters index by id, so the three maps are rebuilt at the
// front door, once per route, rather than in a role that would then have to
// promise three separate containers can never disagree.
type listBlocking struct {
	blockedBy map[string][]string
	blocks    map[string][]string
	parent    map[string]string
}

func newListBlocking(result issueops.BlockingResult) listBlocking {
	out := listBlocking{
		blockedBy: make(map[string][]string, len(result.Items)),
		blocks:    make(map[string][]string, len(result.Items)),
		parent:    make(map[string]string, len(result.Items)),
	}
	for _, entry := range result.Items {
		if len(entry.BlockedBy) > 0 {
			out.blockedBy[entry.ID] = entry.BlockedBy
		}
		if len(entry.Blocks) > 0 {
			out.blocks[entry.ID] = entry.Blocks
		}
		if entry.Parent != "" {
			out.parent[entry.ID] = entry.Parent
		}
	}
	return out
}

// annotateListBlocking is the DIRECT route's decoration read. It swallows every
// failure, including the accessor's, because that is what this route has always
// done: `bd list` renders the page undecorated rather than refusing to print it.
func annotateListBlocking(ctx context.Context, store storage.DoltStorage, ids []string) listBlocking {
	annotator, err := store.BlockingAnnotator()
	if err != nil {
		return newListBlocking(issueops.BlockingResult{})
	}
	result, err := annotator.AnnotateBlocking(ctx, issueops.BlockingRequest{IDs: ids})
	if err != nil {
		return newListBlocking(issueops.BlockingResult{})
	}
	return newListBlocking(result)
}

// proxiedBlockingAnnotator hands back the guarded blocking-decoration surface
// for the proxied-server provider, through the provider's own accessor.
func proxiedBlockingAnnotator() (issueops.BlockingAnnotator, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.BlockingAnnotatorSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the blocking-decoration surface", uowProvider)
	}
	return src.BlockingAnnotator()
}
