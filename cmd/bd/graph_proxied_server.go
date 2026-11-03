package main

import (
	"context"
	"fmt"
	"io"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// runGraphProxiedServer carries the cap the RunE resolved once, and enforces
// it the way each arm's direct counterpart does: --all threads it into every
// per-status search (loadAllGraphSubgraphsUOW), a single-issue graph checks
// the connected-component node count after the walk
// (enforceGraphSubgraphCap). A cap overage exits 2 through the shared
// handleMaxRowsError mapping, with nothing rendered.
func runGraphProxiedServer(ctx context.Context, out io.Writer, args []string, maxRows int, maxRowsSource string) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	if graphAll {
		subgraphs, err := loadAllGraphSubgraphsUOW(ctx, uw, maxRows, maxRowsSource)
		if err != nil {
			if capErr := handleMaxRowsError(err); capErr != nil {
				return capErr
			}
			return HandleErrorRespectJSON("loading all issues: %v", err)
		}
		return renderGraphAllSubgraphs(out, subgraphs)
	}

	root, err := uw.IssueUseCase().GetIssue(ctx, args[0])
	if err != nil || root == nil {
		return HandleErrorRespectJSON("issue '%s' not found", args[0])
	}
	subgraph, err := loadGraphSubgraphUOW(ctx, uw, root)
	if err != nil {
		return HandleErrorRespectJSON("loading graph: %v", err)
	}
	if err := enforceGraphSubgraphCap(subgraph, maxRows, maxRowsSource); err != nil {
		return err
	}
	return renderGraphSingleSubgraph(out, subgraph)
}

func runGraphCheckProxiedServer(ctx context.Context) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	cycles, err := uw.DependencyUseCase().DetectCycles(ctx)
	if err != nil {
		return HandleErrorRespectJSON("cycle detection failed: %v", err)
	}
	return renderGraphCheck(cycles)
}

func loadGraphSubgraphUOW(ctx context.Context, uw uow.UnitOfWork, root *types.Issue) (*TemplateSubgraph, error) {
	subgraph := &TemplateSubgraph{
		Root:     root,
		Issues:   []*types.Issue{root},
		IssueMap: map[string]*types.Issue{root.ID: root},
	}

	queue := []string{root.ID}
	visited := map[string]bool{root.ID: true}

	addNeighbors := func(currentID string, dir domain.DepDirection) {
		metas, err := uw.DependencyUseCase().ListWithIssueMetadata(ctx, currentID, domain.DepListFilter{Direction: dir})
		if err != nil {
			return
		}
		for _, d := range metas {
			iss := d.Issue
			if visited[iss.ID] {
				continue
			}
			visited[iss.ID] = true
			subgraph.Issues = append(subgraph.Issues, &iss)
			subgraph.IssueMap[iss.ID] = &iss
			queue = append(queue, iss.ID)
		}
	}

	for len(queue) > 0 {
		currentID := queue[0]
		queue = queue[1:]
		addNeighbors(currentID, domain.DepDirectionIn)  // dependents
		addNeighbors(currentID, domain.DepDirectionOut) // dependencies
	}

	ids := make([]string, 0, len(subgraph.Issues))
	for _, iss := range subgraph.Issues {
		ids = append(ids, iss.ID)
	}
	recs, err := uw.DependencyUseCase().GetIssueDependencyRecords(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("loading dependencies: %w", err)
	}
	for _, iss := range subgraph.Issues {
		for _, dep := range recs[iss.ID] {
			if _, ok := subgraph.IssueMap[dep.DependsOnID]; ok {
				subgraph.Dependencies = append(subgraph.Dependencies, dep)
			}
		}
	}

	return subgraph, nil
}

// loadAllGraphSubgraphsUOW is the proxied twin of loadAllGraphSubgraphs, and
// the cap flows the same way: propagated to each per-status SearchIssues call
// so any single status that exceeds it returns the typed error directly — the
// direct route's documented per-status behavior (up to 3x the cap can load
// in total before one status trips it), not a bound on the union.
func loadAllGraphSubgraphsUOW(ctx context.Context, uw uow.UnitOfWork, maxRows int, maxRowsSource string) ([]*TemplateSubgraph, error) {
	var allIssues []*types.Issue
	for _, status := range []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusBlocked} {
		statusCopy := status
		page, err := uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{
			Status:        &statusCopy,
			MaxRows:       maxRows,
			MaxRowsSource: maxRowsSource,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to search issues: %w", err)
		}
		allIssues = append(allIssues, page.Items...)
	}

	if len(allIssues) == 0 {
		return nil, nil
	}

	issueMap := make(map[string]*types.Issue, len(allIssues))
	ids := make([]string, 0, len(allIssues))
	for _, issue := range allIssues {
		issueMap[issue.ID] = issue
		ids = append(ids, issue.ID)
	}

	recs, err := uw.DependencyUseCase().GetIssueDependencyRecords(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to load dependencies: %w", err)
	}
	var allDeps []*types.Dependency
	for _, issue := range allIssues {
		for _, dep := range recs[issue.ID] {
			if _, ok := issueMap[dep.DependsOnID]; ok {
				allDeps = append(allDeps, dep)
			}
		}
	}

	return assembleAllSubgraphs(allIssues, issueMap, allDeps), nil
}
