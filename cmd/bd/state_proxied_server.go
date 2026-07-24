package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

func proxiedStateLabels(ctx context.Context, uw uow.UnitOfWork, issueID string) (string, []string, error) {
	issue, isWisp, err := proxiedGetIssueOrWisp(ctx, uw, issueID)
	if err != nil {
		return "", nil, HandleErrorRespectJSON("resolving %s: %v", issueID, err)
	}
	if issue == nil {
		return "", nil, HandleErrorRespectJSON("resolving %s: not found", issueID)
	}
	var labels []string
	if isWisp {
		labels, err = uw.LabelUseCase().GetWispLabels(ctx, issue.ID)
	} else {
		labels, err = uw.LabelUseCase().GetLabels(ctx, issue.ID)
	}
	if err != nil {
		return "", nil, HandleErrorRespectJSON("%v", err)
	}
	return issue.ID, labels, nil
}

func runStateProxiedServer(ctx context.Context, issueID, dimension string) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	fullID, labels, err := proxiedStateLabels(ctx, uw, issueID)
	if err != nil {
		return err
	}

	prefix := dimension + ":"
	var value string
	for _, label := range labels {
		if strings.HasPrefix(label, prefix) {
			value = strings.TrimPrefix(label, prefix)
			break
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"issue_id":  fullID,
			"dimension": dimension,
			"value":     value,
		}
		if value == "" {
			result["value"] = nil
		}
		return outputJSON(result)
	}

	if value == "" {
		fmt.Printf("(no %s state set)\n", dimension)
	} else {
		fmt.Println(value)
	}
	return nil
}

type setStateResult struct {
	fullID     string
	oldValue   string
	eventID    string
	changed    bool
	removeWarn string
}

func runSetStateProxiedServer(ctx context.Context, issueID, dimension, newValue, reason string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	newLabel := dimension + ":" + newValue

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (setStateResult, string, error) {
		issue, isWisp := proxiedResolveIssueOrWisp(ctx, uw, issueID)
		if issue == nil {
			return setStateResult{}, "", fmt.Errorf("issue %s not found", issueID)
		}
		fullID := issue.ID

		var labels []string
		var lerr error
		if isWisp {
			labels, lerr = uw.LabelUseCase().GetWispLabels(ctx, fullID)
		} else {
			labels, lerr = uw.LabelUseCase().GetLabels(ctx, fullID)
		}
		if lerr != nil {
			return setStateResult{}, "", lerr
		}

		prefix := dimension + ":"
		var oldLabel, oldValue string
		for _, label := range labels {
			if strings.HasPrefix(label, prefix) {
				oldLabel = label
				oldValue = strings.TrimPrefix(label, prefix)
				break
			}
		}

		if oldLabel == newLabel {
			return setStateResult{fullID: fullID, oldValue: oldValue, changed: false}, "", nil
		}

		eventDesc := fmt.Sprintf("Set %s to %s", dimension, newValue)
		if oldValue != "" {
			eventDesc = fmt.Sprintf("Changed %s from %s to %s", dimension, oldValue, newValue)
		}
		if reason != "" {
			eventDesc += "\n\nReason: " + reason
		}

		event := &types.Issue{
			Title:       fmt.Sprintf("State change: %s → %s", dimension, newValue),
			Description: eventDesc,
			Status:      types.StatusClosed,
			Priority:    4,
			IssueType:   types.TypeEvent,
			CreatedBy:   getActorWithGit(),
		}
		params := domain.CreateIssueParams{Issue: event, ParentID: fullID}

		var eventID string
		if isWisp {
			cr, cerr := uw.IssueUseCase().CreateWisp(ctx, params, actor)
			if cerr != nil {
				return setStateResult{}, "", fmt.Errorf("creating event: %w", cerr)
			}
			eventID = cr.Issue.ID
		} else {
			cr, cerr := uw.IssueUseCase().CreateIssue(ctx, params, actor)
			if cerr != nil {
				return setStateResult{}, "", fmt.Errorf("creating event: %w", cerr)
			}
			eventID = cr.Issue.ID
		}

		var removeWarn string
		if oldLabel != "" {
			var rerr error
			if isWisp {
				rerr = uw.LabelUseCase().RemoveWispLabel(ctx, fullID, oldLabel, actor)
			} else {
				rerr = uw.LabelUseCase().RemoveLabel(ctx, fullID, oldLabel, actor)
			}
			if rerr != nil {
				removeWarn = fmt.Sprintf("failed to remove old label %s: %v", oldLabel, rerr)
			}
		}
		if isWisp {
			if aerr := uw.LabelUseCase().AddWispLabel(ctx, fullID, newLabel, actor); aerr != nil {
				return setStateResult{}, "", fmt.Errorf("adding label: %w", aerr)
			}
		} else {
			if aerr := uw.LabelUseCase().AddLabel(ctx, fullID, newLabel, actor); aerr != nil {
				return setStateResult{}, "", fmt.Errorf("adding label: %w", aerr)
			}
		}

		return setStateResult{fullID: fullID, oldValue: oldValue, eventID: eventID, changed: true, removeWarn: removeWarn}, "bd: set-state " + fullID, nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if res.removeWarn != "" {
		WarnError("%s", res.removeWarn)
	}

	if !res.changed {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"issue_id":  res.fullID,
				"dimension": dimension,
				"value":     newValue,
				"changed":   false,
			})
		}
		fmt.Printf("(no change: %s already set to %s)\n", dimension, newValue)
		return nil
	}

	commandDidWrite.Store(true)

	if jsonOutput {
		result := map[string]interface{}{
			"issue_id":  res.fullID,
			"dimension": dimension,
			"old_value": res.oldValue,
			"new_value": newValue,
			"event_id":  res.eventID,
			"changed":   true,
		}
		if res.oldValue == "" {
			result["old_value"] = nil
		}
		return outputJSON(result)
	}

	fmt.Printf("%s Set %s = %s on %s\n", ui.RenderPass("✓"), dimension, newValue, res.fullID)
	if res.oldValue != "" {
		fmt.Printf("  Previous: %s\n", res.oldValue)
	}
	fmt.Printf("  Event: %s\n", res.eventID)
	return nil
}

func runStateListProxiedServer(ctx context.Context, issueID string) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	fullID, labels, err := proxiedStateLabels(ctx, uw, issueID)
	if err != nil {
		return err
	}

	states := make(map[string]string)
	for _, label := range labels {
		if idx := strings.Index(label, ":"); idx > 0 {
			states[label[:idx]] = label[idx+1:]
		}
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"issue_id": fullID,
			"states":   states,
		})
	}

	if len(states) == 0 {
		fmt.Printf("\n%s has no state labels\n", fullID)
		return nil
	}

	fmt.Printf("\n%s State for %s:\n", ui.RenderAccent("📊"), fullID)
	for dimension, value := range states {
		fmt.Printf("  %s: %s\n", dimension, value)
	}
	fmt.Println()
	return nil
}
