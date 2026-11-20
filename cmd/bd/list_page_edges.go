package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// listEdgeSource is the one accessor listDependencyRecords needs. Every
// storage.DoltStorage satisfies it, and the --watch display takes it rather
// than the whole store so a test can hand it a stub.
type listEdgeSource interface {
	EdgeReader() (issueops.EdgeReader, error)
}

// listDependencyRecords returns the stored outgoing dependency edges of the
// listed issues, keyed by issue id, in the shape GetAllDependencyRecords
// returns: no key for an issue with no edges, and a non-nil map on success.
//
// The flat page's text renderings on a store-backed route take these edges from
// the page instead (listPageDependencies); this read serves the --parent tree,
// --watch, --format and a remote backend's page.
//
// The listed issues' own outgoing edges are the only edges any `bd list` text
// rendering reads, from either source. Every reader
// keys the map by a LISTED issue's id: buildIssueTreeWithDepsOrdered nests a
// child under a parent only when both are listed, and it looks the edge up
// under the child; orderSiblingsByDeps and depRender.annotationsFor read the
// edges of the sibling or node they are drawing; gatesByIssueID reads the
// edges of the listed ids it decorates; and outputFormattedList prints the
// edges of listed issues whose target is listed too. An edge that an unlisted
// issue owns is never read, even when it points at a listed one, and a parent
// that is not listed nests nothing. So the listed ids are the whole question,
// and a full-store read would only fetch edges that are then ignored.
//
// It reads through the EdgeReader role rather than GetDependencyRecordsForIssues
// because every backend serves the role. The http store refuses the raw batch
// method (internal/httpclient/unsupported_gen.go) and answers the role with one
// anchored listDependencies request per 100 ids. GetAllDependencyRecords has no
// wire operation of its own, so over http it pages through every issue id in
// the workspace and then reads every id's edges in chunks of 100: about 60
// sequential requests for a 20-row page of a ~3,800-issue workspace.
func listDependencyRecords(ctx context.Context, src listEdgeSource, issues []*types.Issue) (map[string][]*types.Dependency, error) {
	deps := make(map[string][]*types.Dependency)
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue != nil {
			ids = append(ids, issue.ID)
		}
	}
	if len(ids) == 0 {
		return deps, nil
	}
	reader, err := src.EdgeReader()
	if err != nil {
		return nil, err
	}
	result, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: ids})
	if err != nil {
		return nil, err
	}
	addAnchorEdges(deps, result)
	return deps, nil
}

// addAnchorEdges keys each anchor's edges by its id, leaving out an anchor with
// none, so a map built from an edge read has no key for an issue with no
// edges.
func addAnchorEdges(deps map[string][]*types.Dependency, result issueops.EdgeReadResult) {
	for _, anchor := range result.Anchors {
		if len(anchor.Edges) > 0 {
			deps[anchor.ID] = anchor.Edges
		}
	}
}

// pageDependencyRecords returns the outgoing dependency edges the page read
// already returned with each listed issue (types.Issue.Dependencies), in the
// shape and order listDependencyRecords returns: no key for an issue with no
// edges, a non-nil map, and each issue's edges sorted by target and then type.
// It reads nothing.
//
// The page query projects every listed row's edges beside the row, from the
// dependency table of the row's own plane (sqlbuild.SearchCountsSQL, deps_json;
// that projection is not affected by SkipCounts or Lite). The edges go through
// issueops.FinishEdgeRead, the function that orders and filters an edge read,
// with every listed row present: a row on the page exists in the page's own
// snapshot, so the rule that a missing anchor carries no edges never applies.
//
// Snapshot: the edges come from the same statement as their row, inside the
// page's one read transaction, so they agree with the rows they are drawn
// under. The edge read
// this replaces was a separate, later transaction. Between the two, another
// process could add or remove an edge or delete a listed issue; the render then
// showed the later edges, and an issue deleted in between kept its row but lost
// its edges. Now a row and its edges are both shown as of the page read. That
// is acceptable because the listing is informational and nothing here guards a
// write. The gate and blocking decorations still read their targets afterward,
// as before.
//
// Plane: for an id present in both issues and wisps, a durable listing (the
// default, which skips the wisps table) holds the durable row, and its edges
// come from dependencies. The edge read went to wisp_dependencies for such an
// id, so it drew the wisp's edges under the durable row. A listing that merges
// the wisps table holds the wisp row and its wisp_dependencies edges, as the
// edge read did.
func pageDependencyRecords(issues []*types.Issue) map[string][]*types.Dependency {
	deps := make(map[string][]*types.Dependency)
	ids := make([]string, 0, len(issues))
	present := make(map[string]struct{}, len(issues))
	edges := make(map[string][]*types.Dependency, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		ids = append(ids, issue.ID)
		present[issue.ID] = struct{}{}
		edges[issue.ID] = issue.Dependencies
	}
	addAnchorEdges(deps, storeissueops.FinishEdgeRead(storeissueops.EdgeReadAnchors(ids), present, edges, nil))
	return deps
}

// listPageDependencies returns the listed issues' outgoing edges for the direct
// route's flat page: from the page itself (pageDependencyRecords) when the page
// carries them, otherwise from one edge read (listDependencyRecords).
func listPageDependencies(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) (map[string][]*types.Dependency, error) {
	if pageCarriesDependencies(st) {
		return pageDependencyRecords(issues), nil
	}
	return listDependencyRecords(ctx, st, issues)
}

// pageCarriesDependencies reports whether a page that st's IssueReader lists
// carries each row's outgoing edges. A store-backed reader's page does (see
// pageDependencyRecords). A remote backend's page is a server's wire response,
// and an empty edge list there cannot be told apart from edges the server did
// not send, so the edges are read instead.
func pageCarriesDependencies(st storage.DoltStorage) bool {
	return st != nil && !storeIsRemoteBackend(st)
}
