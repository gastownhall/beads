package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// crossRigPresentationResolver is the resolver the show and dep list views
// read foreign dependency targets through; tests swap it. beadsDir is the
// anchor's own workspace (a prefix-routed result's .beads directory), so a
// bare cross-rig target resolves through the DEPENDENT workspace's routes the
// same way the readiness policy does; empty falls back to the launching
// command's workspace. When no route leads to another workspace
// (crossRigRoutesFollowable) no foreign row is possible, and the nil resolver
// spares the view its edge read: `bd show` of an issue with an external: ref
// costs no request for a row it could never show.
var crossRigPresentationResolver = func(beadsDir string) externaldeps.IssueResolver {
	dir := beadsDir
	if dir == "" {
		dir = crossRigRoutesDir()
	}
	if !crossRigRoutesFollowable(dir) {
		return nil
	}
	return crossRigIssueResolver(func() string { return dir })
}

// foreignDependencyRows returns one view-only row per stored outgoing edge of
// anchorID whose target is a bare cross-prefix id this workspace does not
// hold and the cross-rig resolver can read (GH#5784). Each row carries the
// edge's own type and External set. A target already in shown (a local
// neighbor), an external: capability ref, and a target the resolver cannot
// read get no row, so an unresolved edge keeps its existing unresolved count
// and dropped-edge notice. A read or resolver error yields no rows: the view
// stays what it was before cross-rig resolution.
func foreignDependencyRows(ctx context.Context, reader issueops.EdgeReader, anchorID string, depTypes []types.DependencyType, shown []*types.IssueWithDependencyMetadata, resolve externaldeps.IssueResolver) []*types.IssueWithDependencyMetadata {
	if reader == nil || resolve == nil || anchorID == "" {
		return nil
	}
	result, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: []string{anchorID}, Types: depTypes})
	if err != nil || len(result.Anchors) != 1 {
		return nil
	}
	known := make(map[string]struct{}, len(shown))
	for _, iss := range shown {
		if iss != nil {
			known[iss.ID] = struct{}{}
		}
	}
	var edges []*types.Dependency
	var ids []string
	queued := make(map[string]struct{})
	for _, dep := range result.Anchors[0].Edges {
		if dep == nil {
			continue
		}
		target := dep.DependsOnID
		if _, ok := known[target]; ok || !crossRigDepTarget(dep.IssueID, target) {
			continue
		}
		edges = append(edges, dep)
		if _, ok := queued[target]; !ok {
			queued[target] = struct{}{}
			ids = append(ids, target)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	found, _, err := resolve(ctx, ids)
	if err != nil {
		return nil
	}
	var rows []*types.IssueWithDependencyMetadata
	for _, dep := range edges {
		issue := found[dep.DependsOnID]
		if issue == nil {
			continue
		}
		rows = append(rows, &types.IssueWithDependencyMetadata{Issue: *issue, DependencyType: dep.Type, External: true})
	}
	return rows
}

// externalRowMarker is the human marker a foreign dependency row carries.
func externalRowMarker(dep *types.IssueWithDependencyMetadata) string {
	if dep != nil && dep.External {
		return " (external)"
	}
	return ""
}

// mutedExternalRowMarker is externalRowMarker for a muted (closed) row.
func mutedExternalRowMarker(dep *types.IssueWithDependencyMetadata) string {
	if marker := externalRowMarker(dep); marker != "" {
		return ui.RenderMuted(marker)
	}
	return ""
}

// appendForeignDependencyDetails adds the foreign rows of a --json show
// detail view to its dependencies, in the brief form when brief is set, and
// takes them off unresolvable_dependencies. It reads nothing unless the view
// already counts an unresolvable edge: a foreign row is one of those edges.
// beadsDir is the anchor's workspace (crossRigPresentationResolver).
func appendForeignDependencyDetails(ctx context.Context, edgeReader func() (issueops.EdgeReader, error), beadsDir string, details *types.IssueDetails, brief bool) {
	if details == nil || details.UnresolvableDependencies == nil || *details.UnresolvableDependencies <= 0 {
		return
	}
	reader, err := edgeReader()
	if err != nil {
		return
	}
	rows := foreignDependencyRows(ctx, reader, details.ID, nil, details.Dependencies, crossRigPresentationResolver(beadsDir))
	if len(rows) == 0 {
		return
	}
	for _, row := range rows {
		if brief {
			row = &types.IssueWithDependencyMetadata{
				Issue: types.Issue{
					ID:          row.ID,
					Status:      row.Status,
					IssueType:   row.IssueType,
					Priority:    row.Priority,
					Title:       row.Title,
					CloseReason: row.CloseReason,
				},
				DependencyType: row.DependencyType,
				External:       true,
			}
		}
		details.Dependencies = append(details.Dependencies, row)
	}
	if n := *details.UnresolvableDependencies - int64(len(rows)); n > 0 {
		details.UnresolvableDependencies = &n
	} else {
		details.UnresolvableDependencies = nil
	}
}

// withForeignDependencyRows adds the foreign rows of anchorID to the text
// show's dependency listing. It reads nothing unless both the count and the
// listing succeeded and the listing came back short, so a failed read and the
// unresolvable-edge notice keep their meaning. beadsDir is the anchor's
// workspace (crossRigPresentationResolver).
func withForeignDependencyRows(ctx context.Context, edgeReader func() (issueops.EdgeReader, error), beadsDir, anchorID string, counts depCounts, deps []*types.IssueWithDependencyMetadata, depsErr error) []*types.IssueWithDependencyMetadata {
	if depsErr != nil || counts.deps.err != nil || counts.deps.n <= int64(len(deps)) {
		return deps
	}
	reader, err := edgeReader()
	if err != nil {
		return deps
	}
	return append(deps, foreignDependencyRows(ctx, reader, anchorID, nil, deps, crossRigPresentationResolver(beadsDir))...)
}
