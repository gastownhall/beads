package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

func runStatusProxiedServer(ctx context.Context, showAssigned, noActivity bool) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	stats, err := uw.IssueUseCase().GetStatistics(ctx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if showAssigned {
		stats, err = proxiedAssignedStatistics(ctx, uw, actor)
		if err != nil {
			return HandleErrorRespectJSON("failed to get assigned statistics: %v", err)
		}
	}

	var recentActivity *RecentActivitySummary
	if !noActivity {
		recentActivity = getGitActivity(24)
	}

	return renderStatus(stats, recentActivity)
}

func proxiedAssignedStatistics(ctx context.Context, uw uow.UnitOfWork, assignee string) (*types.Statistics, error) {
	assigneePtr := assignee
	page, err := uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{Assignee: &assigneePtr})
	if err != nil {
		return nil, err
	}

	readyCount := 0
	readyPage, err := uw.IssueUseCase().GetReadyWork(ctx, types.WorkFilter{Assignee: &assigneePtr})
	if err == nil {
		readyCount = len(readyPage.Items)
	}

	return buildAssignedStats(page.Items, readyCount), nil
}
