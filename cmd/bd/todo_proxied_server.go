package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

func todoListProxied(ctx context.Context, filter types.IssueFilter) ([]*types.Issue, error) {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return nil, err
	}
	defer uw.Close(ctx)

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, HandleError("failed to list TODOs: %v", err)
	}
	return page.Items, nil
}

func runTodoAddProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	title := strings.Join(args, " ")

	priority, _ := cmd.Flags().GetInt("priority")
	description, _ := cmd.Flags().GetString("description")

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	issue := &types.Issue{
		Title:       title,
		Description: description,
		Priority:    priority,
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Assignee:    getActorWithGit(),
		Owner:       getOwner(),
		CreatedBy:   getActorWithGit(),
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*types.Issue, string, error) {
		result, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{Issue: issue}, getActorWithGit())
		if err != nil {
			return nil, "", err
		}
		return result.Issue, fmt.Sprintf("bd: create %s", result.Issue.ID), nil
	})
	if err != nil {
		return HandleError("failed to create TODO: %v", err)
	}
	commandDidWrite.Store(true)

	if jsonOutput {
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return HandleError("failed to marshal JSON: %v", err)
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("Created %s: %s\n", ui.RenderID(res.ID), res.Title)
	return nil
}

func runTodoDoneProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	reason, _ := cmd.Flags().GetString("reason")
	if reason == "" {
		reason = "Completed"
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	var closedIDs []string
	for _, issueID := range args {
		_, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (struct{}, string, error) {
			issue, isWisp := proxiedResolveIssueOrWisp(ctx, uw, issueID)
			if issue == nil {
				return struct{}{}, "", fmt.Errorf("issue %s not found", issueID)
			}
			params := domain.CloseIssueParams{Reason: reason}
			var e error
			if isWisp {
				_, e = uw.IssueUseCase().CloseWisp(ctx, issue.ID, params, getActorWithGit())
			} else {
				_, e = uw.IssueUseCase().CloseIssue(ctx, issue.ID, params, getActorWithGit())
			}
			if e != nil {
				return struct{}{}, "", e
			}
			return struct{}{}, fmt.Sprintf("bd: close %s", issue.ID), nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to close %s: %v\n", issueID, err)
			continue
		}
		closedIDs = append(closedIDs, issueID)
	}

	if len(closedIDs) > 0 {
		commandDidWrite.Store(true)
	}

	if jsonOutput {
		data, err := json.MarshalIndent(map[string]interface{}{
			"closed": closedIDs,
			"reason": reason,
		}, "", "  ")
		if err != nil {
			return HandleError("failed to marshal JSON: %v", err)
		}
		fmt.Println(string(data))
		return nil
	}
	for _, id := range closedIDs {
		fmt.Printf("Closed %s\n", ui.RenderID(id))
	}
	return nil
}
