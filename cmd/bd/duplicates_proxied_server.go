package main

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

func runDuplicatesProxiedServer(ctx context.Context, autoMerge, dryRun bool) error {
	if autoMerge && !dryRun {
		return HandleErrorRespectJSON("duplicates --auto-merge is not supported in proxied-server mode")
	}

	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return HandleError("fetching issues: %v", err)
	}
	allIssues := page.Items

	duplicateGroups := findDuplicateGroups(openIssuesOf(allIssues))
	if len(duplicateGroups) == 0 {
		return outputNoDuplicates()
	}

	refCounts := countReferences(allIssues)
	depCounts, _ := uw.DependencyUseCase().CountsByIssueIDs(ctx, collectDuplicateGroupIDs(duplicateGroups))
	structuralScores := buildStructuralScores(duplicateGroups, depCounts)

	return outputDuplicates(duplicateGroups, refCounts, structuralScores, autoMerge, dryRun, nil)
}
