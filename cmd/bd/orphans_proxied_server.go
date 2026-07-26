package main

import (
	"context"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

func runOrphansProxiedServer(ctx context.Context, labels, labelsAny []string, fix, details bool) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	provider := &uowIssueProvider{uw: uw, ctx: ctx, labels: labels, labelsAny: labelsAny}
	orphans, err := findOrphanedIssuesWithProvider(".", provider)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	return reportOrphans(orphans, fix, details)
}

type uowIssueProvider struct {
	uw        uow.UnitOfWork
	ctx       context.Context
	labels    []string
	labelsAny []string
}

func (p *uowIssueProvider) GetOpenIssues(ctx context.Context) ([]*types.Issue, error) {
	openStatus := types.StatusOpen
	openPage, err := p.uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{
		Status:    &openStatus,
		Labels:    p.labels,
		LabelsAny: p.labelsAny,
	})
	if err != nil {
		return nil, err
	}
	inProgressStatus := types.StatusInProgress
	inProgressPage, err := p.uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{
		Status:    &inProgressStatus,
		Labels:    p.labels,
		LabelsAny: p.labelsAny,
	})
	if err != nil {
		return nil, err
	}
	return append(openPage.Items, inProgressPage.Items...), nil
}

func (p *uowIssueProvider) GetIssuePrefix() string {
	if yamlPrefix := config.GetString("issue-prefix"); yamlPrefix != "" {
		return yamlPrefix
	}
	prefix, err := p.uw.ConfigUseCase().GetConfig(p.ctx, "issue_prefix")
	if err != nil || prefix == "" {
		return "bd"
	}
	return prefix
}
