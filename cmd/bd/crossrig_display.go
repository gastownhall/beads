package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/externaldeps"
	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// displayForeignResolver builds the bare-cross-prefix resolver for a display
// surface (GH#5784 AC-6): `bd show` and single-anchor `bd dep list` render the
// dependency rows a worker looks at, so a bare cross-prefix edge must resolve
// its target the same way the readiness policy does — through the DEPENDENT
// workspace's own routes.jsonl. beadsDir is the anchor's workspace when the
// caller knows it (a routed result's, or the launching command's own); empty
// falls back to the command's resolved workspace, the same fallback the
// externaldeps wiring takes (§210).
func displayForeignResolver(beadsDir string) externaldeps.ForeignIssueResolver {
	if beadsDir == "" {
		beadsDir = resolveCommandBeadsDir(dbPath)
	}
	return newPrefixRouteResolver(beadsDir)
}

// enrichWithForeignDepRows appends one view row per OUTGOING bare cross-prefix
// edge of anchorID whose target the resolver can answer with an issue row
// (GH#5784 AC-6). The appended rows carry the foreign issue's real fields, the
// edge's own type, and External set — additive view metadata; every row the
// storage layer builds keeps External unset, so pre-existing JSON shapes are
// untouched.
//
// WHAT IT IS FOR: Relations and the metadata-bearing dependency listings drop
// every edge whose far end has no row in this database, so a stored
// cross-rig dependency rendered as no dependency at all — indistinguishable
// from having none (be-lpi, GH#5784 §224). This puts the row back beside the
// local ones instead.
//
// WHAT IT IS NOT FOR: it never invents a row. A target the routes cannot
// answer — no route, unreadable rig, no such issue — stays absent from the
// answer, and the caller's own dropped-edge diagnostics (warnDroppedDepEdges,
// warnUnresolvableDepEdges) keep reporting it, exactly as before. An
// `external:` reference is not a bare ID and keeps its existing shapes.
//
// Failure policy: enrichment is BEST EFFORT. A reader or resolver error leaves
// shown unchanged — a display path that cannot resolve a foreign row degrades
// to the pre-enrichment view (which is truthful), never to a failed command.
func enrichWithForeignDepRows(ctx context.Context, resolver externaldeps.ForeignIssueResolver, reader issueops.EdgeReader, anchorID, typeFilter string, shown []*issueops.RelatedIssue) []*issueops.RelatedIssue {
	if resolver == nil || reader == nil || anchorID == "" {
		return shown
	}
	var depTypes []types.DependencyType
	if typeFilter != "" {
		depTypes = []types.DependencyType{types.DependencyType(typeFilter)}
	}
	result, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: []string{anchorID}, Types: depTypes})
	if err != nil || len(result.Anchors) != 1 {
		return shown
	}

	// Endpoints already carried by the listing stay as they are: a row the
	// local store answered needs no foreign view of the same issue.
	known := make(map[string]struct{}, len(shown))
	for _, iss := range shown {
		known[iss.ID] = struct{}{}
	}
	var targets []string
	for _, edge := range result.Anchors[0].Edges {
		// Classification is the same rule the add path reports by
		// (markExternalJSON): a bare ID whose prefix belongs to another rig.
		// An explicit external: reference is excluded — it has its own
		// existing shape and is not a bare cross-prefix edge.
		if edge == nil || edge.DependsOnID == "" || IsExternalRef(edge.DependsOnID) {
			continue
		}
		if !storeissueops.IsExternalDepTarget(anchorID, edge.DependsOnID) {
			continue
		}
		if _, ok := known[edge.DependsOnID]; ok {
			continue
		}
		targets = append(targets, edge.DependsOnID)
	}
	if len(targets) == 0 {
		return shown
	}
	resolved, err := resolver.ResolveForeignIssues(ctx, targets)
	if err != nil || len(resolved) == 0 {
		return shown
	}

	// One row per stored edge, in the reader's pinned edge order, so the
	// listing's own ordering rule stays the answer's ordering rule.
	out := shown
	for _, edge := range result.Anchors[0].Edges {
		if edge == nil {
			continue
		}
		if _, ok := known[edge.DependsOnID]; ok {
			continue
		}
		res := resolved[edge.DependsOnID]
		if res == nil || res.Issue == nil {
			// Unresolved: no row, no fabrication — the caller's
			// dropped-edge diagnostics own that target.
			continue
		}
		out = append(out, &issueops.RelatedIssue{Issue: *res.Issue, DependencyType: edge.Type, External: true})
		known[edge.DependsOnID] = struct{}{}
	}
	return out
}

// enrichShowDetailsForeignDeps applies enrichWithForeignDepRows to a detail
// view's dependency rows (the `bd show --json` shape) and recomputes the
// unresolvable-edges delta it carries: each appended foreign row is an edge
// whose far end is now represented, so it is no longer counted among the
// edges that could not be. The delta is only ever SHRUNK, and only when it was
// already published — a details value that did not compute one (a failed or
// skipped listing read) keeps its silence.
func enrichShowDetailsForeignDeps(ctx context.Context, resolver externaldeps.ForeignIssueResolver, reader issueops.EdgeReader, details *issueops.IssueDetails) *issueops.IssueDetails {
	if details == nil {
		return details
	}
	details.Dependencies = enrichWithForeignDepRows(ctx, resolver, reader, details.ID, "", details.Dependencies)
	if details.UnresolvableDependencies != nil && details.DependencyCount != nil {
		if n := *details.DependencyCount - int64(len(details.Dependencies)); n > 0 {
			details.UnresolvableDependencies = &n
		} else {
			details.UnresolvableDependencies = nil
		}
	}
	return details
}
