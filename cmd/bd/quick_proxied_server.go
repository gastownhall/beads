package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

func runQuickProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	title := strings.Join(args, " ")

	priorityStr, _ := cmd.Flags().GetString("priority")
	issueType, _ := cmd.Flags().GetString("type")
	labels, _ := cmd.Flags().GetStringSlice("labels")
	parentID, _ := cmd.Flags().GetString("parent")

	priority, err := validation.ValidatePriority(priorityStr)
	if err != nil {
		return HandleError("%v", err)
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	issue := &types.Issue{
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  priority,
		IssueType: types.IssueType(issueType).Normalize(),
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*types.Issue, string, error) {
		params := domain.CreateIssueParams{
			Issue:                   issue,
			ParentID:                parentID,
			Labels:                  labels,
			InheritLabelsFromParent: parentID != "",
		}
		result, err := uw.IssueUseCase().CreateIssue(ctx, params, actor)
		if err != nil {
			return nil, "", err
		}
		return result.Issue, fmt.Sprintf("bd: create %s", result.Issue.ID), nil
	})
	if err != nil {
		return HandleError("%v", err)
	}
	commandDidWrite.Store(true)

	fmt.Println(res.ID)
	return nil
}
