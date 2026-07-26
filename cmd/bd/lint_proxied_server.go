package main

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

func runLintProxiedServer(ctx context.Context, args []string, typeFilter, statusFilter string) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	var issues []*types.Issue
	if len(args) > 0 {
		issues = lintCollectByIDs(ctx, args, uw.IssueUseCase().GetIssue)
	} else {
		page, err := uw.IssueUseCase().SearchIssues(ctx, "", buildLintFilter(typeFilter, statusFilter))
		if err != nil {
			return HandleError("%v", err)
		}
		issues = page.Items
	}

	return runLint(issues)
}
