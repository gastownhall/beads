package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
)

func proxiedMutateIssue(ctx context.Context, id, commitMsg string, mutate func(ctx context.Context, uw uow.UnitOfWork, issue *types.Issue, isWisp bool) error) (*types.Issue, error) {
	if uowProvider == nil {
		return nil, fmt.Errorf("proxied-server UOW provider not initialized")
	}
	var updated *types.Issue
	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		issue, isWisp := proxiedResolveIssueOrWisp(ctx, uw, id)
		if issue == nil {
			return "", fmt.Errorf("issue %s not found", id)
		}
		if err := validateIssueUpdatable(id, issue); err != nil {
			return "", err
		}
		if err := mutate(ctx, uw, issue, isWisp); err != nil {
			return "", err
		}
		if isWisp {
			updated, _ = uw.IssueUseCase().GetWisp(ctx, issue.ID)
		} else {
			updated, _ = uw.IssueUseCase().GetIssue(ctx, issue.ID)
		}
		return commitMsg, nil
	})
	if err != nil {
		return nil, err
	}
	commandDidWrite.Store(true)
	return updated, nil
}

func proxiedUpdateIssueFields(ctx context.Context, id, commitMsg string, updates map[string]any) (*types.Issue, error) {
	return proxiedMutateIssue(ctx, id, commitMsg, func(ctx context.Context, uw uow.UnitOfWork, issue *types.Issue, isWisp bool) error {
		if isWisp {
			return uw.IssueUseCase().UpdateWisp(ctx, issue.ID, updates, actor)
		}
		return uw.IssueUseCase().UpdateIssue(ctx, issue.ID, updates, actor)
	})
}

func runAssignProxiedServer(ctx context.Context, args []string) error {
	id := args[0]
	assignee := args[1]
	updated, err := proxiedUpdateIssueFields(ctx, id, "bd: assign "+id, map[string]any{"assignee": assignee})
	if err != nil {
		return HandleErrorRespectJSON("assign %s: %v", id, err)
	}
	if jsonOutput {
		if updated != nil {
			return outputJSON(updated)
		}
		return nil
	}
	title := issueTitleOrEmpty(updated)
	if assignee == "" {
		fmt.Printf("%s Unassigned %s\n", ui.RenderPass("✓"), formatFeedbackID(id, title))
	} else {
		fmt.Printf("%s Assigned %s to %s\n", ui.RenderPass("✓"), formatFeedbackID(id, title), assignee)
	}
	return nil
}

func runPriorityProxiedServer(ctx context.Context, args []string) error {
	id := args[0]
	priority, err := validation.ValidatePriority(args[1])
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	updated, err := proxiedUpdateIssueFields(ctx, id, "bd: priority "+id, map[string]any{"priority": priority})
	if err != nil {
		return HandleErrorRespectJSON("priority %s: %v", id, err)
	}
	if jsonOutput {
		if updated != nil {
			return outputJSON(updated)
		}
		return nil
	}
	fmt.Printf("%s Set priority of %s to P%d\n", ui.RenderPass("✓"), formatFeedbackID(id, issueTitleOrEmpty(updated)), priority)
	return nil
}

func runNoteProxiedServer(ctx context.Context, id, noteText string) error {
	updated, err := proxiedMutateIssue(ctx, id, "bd: note "+id, func(ctx context.Context, uw uow.UnitOfWork, issue *types.Issue, isWisp bool) error {
		combined := issue.Notes
		if combined != "" {
			combined += "\n"
		}
		combined += noteText
		updates := map[string]any{"notes": combined}
		if isWisp {
			return uw.IssueUseCase().UpdateWisp(ctx, issue.ID, updates, actor)
		}
		return uw.IssueUseCase().UpdateIssue(ctx, issue.ID, updates, actor)
	})
	if err != nil {
		return HandleErrorRespectJSON("note %s: %v", id, err)
	}
	if jsonOutput {
		if updated != nil {
			return outputJSON(updated)
		}
		return nil
	}
	fmt.Printf("%s Note added to %s\n", ui.RenderPass("✓"), formatFeedbackID(id, issueTitleOrEmpty(updated)))
	return nil
}

func runTagProxiedServer(ctx context.Context, args []string) error {
	id := args[0]
	label := args[1]
	updated, err := proxiedMutateIssue(ctx, id, "bd: tag "+id, func(ctx context.Context, uw uow.UnitOfWork, issue *types.Issue, isWisp bool) error {
		if isWisp {
			return uw.LabelUseCase().AddWispLabel(ctx, issue.ID, label, actor)
		}
		return uw.LabelUseCase().AddLabel(ctx, issue.ID, label, actor)
	})
	if err != nil {
		return HandleErrorRespectJSON("tag %s: %v", id, err)
	}
	if jsonOutput {
		if updated != nil {
			return outputJSON(updated)
		}
		return nil
	}
	fmt.Printf("%s Added label %q to %s\n", ui.RenderPass("✓"), label, formatFeedbackID(id, issueTitleOrEmpty(updated)))
	return nil
}
