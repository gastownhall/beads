package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// The proxied-server twins of the four `bd human` subcommands. The direct
// route reaches storage through ensureStoreActive(), which in proxied mode
// would lazily open a DIRECT store and silently bypass the proxy (bd-m7zzd) —
// so each RunE branches here BEFORE that call. Reads ride one read-only unit
// of work; each write invocation is exactly ONE RunTx with a real commit
// message, per the proxied write convention.

// proxiedHumanSearch runs the shared human-label query and hands back the
// issues, inside the caller's unit of work.
func proxiedHumanSearch(ctx context.Context, uw uow.UnitOfWork, status string) ([]*types.Issue, error) {
	filter := types.IssueFilter{
		Labels: []string{"human"},
	}
	if status != "" {
		s := types.Status(status)
		filter.Status = &s
	}
	page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func runHumanListProxiedServer(ctx context.Context, status string) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("listing human beads: %v", err)
	}
	defer uw.Close(ctx)

	issues, err := proxiedHumanSearch(ctx, uw, status)
	if err != nil {
		return HandleErrorRespectJSON("listing human beads: %v", err)
	}

	if jsonOutput {
		issueIDs := make([]string, len(issues))
		for i, issue := range issues {
			issueIDs[i] = issue.ID
		}
		labelsMap, _ := uw.LabelUseCase().GetLabelsForIssues(ctx, issueIDs)
		for _, issue := range issues {
			issue.Labels = labelsMap[issue.ID]
		}

		data, err := json.MarshalIndent(issues, "", "  ")
		if err != nil {
			return HandleErrorRespectJSON("encoding JSON: %v", err)
		}
		fmt.Println(string(data))
		return nil
	}

	printHumanList(issues)
	return nil
}

func runHumanStatsProxiedServer(ctx context.Context) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("getting human bead stats: %v", err)
	}
	defer uw.Close(ctx)

	issues, err := proxiedHumanSearch(ctx, uw, "")
	if err != nil {
		return HandleErrorRespectJSON("getting human bead stats: %v", err)
	}

	printHumanStats(issues)
	return nil
}

// proxiedHumanCloseTarget is the shared pre-flight for respond and dismiss,
// run INSIDE the write transaction so the already-closed verdict and the close
// ride the same snapshot: the classic existence / already-closed refusals with
// their exact wording, plus the missing-'human'-label observation the caller
// warns about after the transaction lands.
func proxiedHumanCloseTarget(ctx context.Context, uw uow.UnitOfWork, issueID string) (hasHumanLabel bool, err error) {
	issue, err := proxiedRequireIssue(ctx, uw, issueID)
	if err != nil {
		return false, err
	}
	if issue == nil {
		return false, fmt.Errorf("issue not found: %s", issueID)
	}
	if issue.Status == types.StatusClosed {
		return false, fmt.Errorf("issue %s is already closed", issueID)
	}

	labelsMap, _ := uw.LabelUseCase().GetLabelsForIssues(ctx, []string{issueID})
	for _, label := range labelsMap[issueID] {
		if label == "human" {
			return true, nil
		}
	}
	return false, nil
}

func runHumanRespondProxiedServer(ctx context.Context, issueID, response string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	hasHumanLabel, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (bool, string, error) {
		hasLabel, err := proxiedHumanCloseTarget(ctx, uw, issueID)
		if err != nil {
			return false, "", err
		}
		commentText := fmt.Sprintf("Response: %s", response)
		if _, err := uw.CommentUseCase().AddCommentToIssue(ctx, issueID, actor, commentText); err != nil {
			return false, "", fmt.Errorf("adding comment: %w", err)
		}
		if _, err := uw.IssueUseCase().CloseIssue(ctx, issueID, domain.CloseIssueParams{Reason: "Responded"}, actor); err != nil {
			return false, "", fmt.Errorf("closing bead: %w", err)
		}
		return hasLabel, fmt.Sprintf("bd: human respond %s", issueID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if !hasHumanLabel {
		fmt.Fprintf(os.Stderr, "Warning: Issue %s does not have 'human' label\n", issueID)
	}

	fmt.Printf("%s Bead %s closed with response.\n", ui.RenderPass("✔"), issueID)
	return nil
}

func runHumanDismissProxiedServer(ctx context.Context, issueID, reason string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	closeReason := "Dismissed"
	if reason != "" {
		closeReason = fmt.Sprintf("Dismissed: %s", reason)
	}

	hasHumanLabel, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (bool, string, error) {
		hasLabel, err := proxiedHumanCloseTarget(ctx, uw, issueID)
		if err != nil {
			return false, "", err
		}
		if _, err := uw.IssueUseCase().CloseIssue(ctx, issueID, domain.CloseIssueParams{Reason: closeReason}, actor); err != nil {
			return false, "", fmt.Errorf("closing bead: %w", err)
		}
		return hasLabel, fmt.Sprintf("bd: human dismiss %s", issueID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if !hasHumanLabel {
		fmt.Fprintf(os.Stderr, "Warning: Issue %s does not have 'human' label\n", issueID)
	}

	fmt.Printf("%s Bead %s dismissed.\n", ui.RenderPass("✔"), issueID)
	return nil
}
