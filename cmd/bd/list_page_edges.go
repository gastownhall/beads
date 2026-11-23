package main

import (
	"context"

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
// These are the only edges any `bd list` text rendering reads. Every reader
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
	for _, anchor := range result.Anchors {
		if len(anchor.Edges) > 0 {
			deps[anchor.ID] = anchor.Edges
		}
	}
	return deps, nil
}
