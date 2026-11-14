package issueops

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// ExportRequest describes which issues `bd export` writes.
type ExportRequest struct {
	// All exports every row: every status, every type (infra, templates,
	// gates), and both planes, wisps included.
	All bool
	// IncludeInfra admits the workspace's infrastructure types (agents, roles,
	// messages by default) without lifting the other default exclusions.
	IncludeInfra bool
}

// ExportRow is one exported issue, fully hydrated: its labels, its stored
// outgoing dependency records and its comment bodies, plus the three
// cardinalities the export line carries.
type ExportRow struct {
	Issue           *types.Issue
	DependencyCount int
	DependentCount  int
	CommentCount    int
}

// ReadExport answers `bd export`'s read set by composing three role reads that
// every backend serves (a remote one as listIssues, listDependencies and
// getIssue):
//
//   - Reader.List, unlimited, for the rows, their labels and their counts;
//   - EdgeReader.ReadEdges, for every row's stored dependency records;
//   - Reader.Get with IncludeComments, for the comment bodies of each row whose
//     comment count is nonzero (a row with no comments costs no read).
//
// THE SELECTION IS EXPORT'S, not the listing's. Every status is exported
// (AllFlag), and gates are always exported. Without All, templates and the
// workspace's infra types are excluded (IncludeInfra admits the latter), and
// TRUE wisps are excluded while no-history rows, which live on the wisps plane
// with ephemeral unset, are kept (GH#3649). A listing cannot say "the wisps
// plane minus its ephemeral rows", so the plane is admitted and the ephemeral
// rows are dropped here. That is exact because the listing is unlimited: no
// row is dropped before a limit could have counted it.
//
// A FAILED READ IS AN ERROR. Export is a data-integrity path, so a refusal or
// a missing relation never degrades to a shorter export. An anchor the edge
// read reports missing (a row deleted between the reads) is an error too, and
// so is a listing page marked HasMore: the listing is unlimited, so a
// truncated page means the reader did not honor that.
func ReadExport(ctx context.Context, rd Reader, edges EdgeReader, req ExportRequest) ([]ExportRow, error) {
	if rd == nil || edges == nil {
		return nil, &ErrUnsupported{Op: "ReadExport", Backend: "nil"}
	}
	unlimited := 0
	list := ListRequest{
		AllFlag:          true,
		IncludeGates:     true,
		IncludeEphemeral: true,
		Limit:            &unlimited,
	}
	// --all is EXPORT's scope, spelled as export defines it (infra,
	// templates, gates and wisps), not ListRequest.IncludeAllTypes: that
	// union is the listing's "hide nothing" contract, which a remote reader
	// refuses to restate, and export has always named its own four.
	list.IncludeInfra = req.All || req.IncludeInfra
	list.IncludeTemplates = req.All
	page, err := rd.List(ctx, list)
	if err != nil {
		return nil, fmt.Errorf("export: listing issues: %w", err)
	}
	if page.HasMore {
		return nil, errors.New("export: listing issues: the listing returned a truncated page; export must never truncate")
	}

	rows := make([]ExportRow, 0, len(page.Items))
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		if item == nil || item.Issue == nil {
			continue
		}
		if !req.All && item.Ephemeral {
			continue
		}
		rows = append(rows, ExportRow{
			Issue:           item.Issue,
			DependencyCount: item.DependencyCount,
			DependentCount:  item.DependentCount,
			CommentCount:    item.CommentCount,
		})
		ids = append(ids, item.ID)
	}
	if len(rows) == 0 {
		return rows, nil
	}

	edgeResult, err := edges.ReadEdges(ctx, EdgeReadRequest{IDs: ids})
	if err != nil {
		return nil, fmt.Errorf("export: reading dependencies: %w", err)
	}
	deps := make(map[string][]*types.Dependency, len(edgeResult.Anchors))
	for _, anchor := range edgeResult.Anchors {
		if anchor.Missing {
			return nil, fmt.Errorf("export: reading dependencies: %s is no longer in the store", anchor.ID)
		}
		if len(anchor.Edges) > 0 {
			deps[anchor.ID] = anchor.Edges
		}
	}

	for i := range rows {
		issue := rows[i].Issue
		issue.Dependencies = deps[issue.ID]
		issue.Comments = nil
		if rows[i].CommentCount == 0 {
			continue
		}
		detail, err := rd.Get(ctx, GetRequest{ID: issue.ID, IncludeComments: true})
		if err != nil {
			return nil, fmt.Errorf("export: reading comments of %s: %w", issue.ID, err)
		}
		if detail != nil {
			issue.Comments = detail.Comments
		}
	}
	return rows, nil
}
