package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

func runLabelAddProxiedServer(ctx context.Context, args []string) error {
	return labelMutateProxied(ctx, args, "added")
}

func runLabelRemoveProxiedServer(ctx context.Context, args []string) error {
	return labelMutateProxied(ctx, args, "removed")
}

func labelMutateProxied(ctx context.Context, args []string, operation string) error {
	issueIDs, labels := parseLabelArgs(args)
	if len(labels) == 0 {
		return HandleErrorRespectJSON("label cannot be empty")
	}
	if operation == "added" {
		for _, label := range labels {
			if strings.HasPrefix(label, "provides:") {
				return HandleErrorRespectJSON("'provides:' labels are reserved for cross-project capabilities. Hint: use 'bd ship %s' instead", strings.TrimPrefix(label, "provides:"))
			}
		}
	}
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	labelDesc := strings.Join(labels, "', '")
	commitMsg := fmt.Sprintf("bd: label %s '%s' on %d issue(s)", operation, labelDesc, len(issueIDs))

	var resolvedIDs []string
	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		resolvedIDs = resolvedIDs[:0]
		for _, inputID := range issueIDs {
			issue, isWisp := proxiedResolveIssueOrWisp(ctx, uw, inputID)
			if issue == nil {
				return "", fmt.Errorf("resolving issue ID %q: not found", inputID)
			}
			for _, label := range labels {
				var e error
				switch {
				case operation == "added" && isWisp:
					e = uw.LabelUseCase().AddWispLabel(ctx, issue.ID, label, actor)
				case operation == "added":
					e = uw.LabelUseCase().AddLabel(ctx, issue.ID, label, actor)
				case isWisp:
					e = uw.LabelUseCase().RemoveWispLabel(ctx, issue.ID, label, actor)
				default:
					e = uw.LabelUseCase().RemoveLabel(ctx, issue.ID, label, actor)
				}
				if e != nil {
					return "", fmt.Errorf("%s label '%s' on %s: %w", operation, label, issue.ID, e)
				}
			}
			resolvedIDs = append(resolvedIDs, issue.ID)
		}
		return commitMsg, nil
	})
	if err != nil {
		return HandleErrorRespectJSON("label %s: %v", operation, err)
	}
	commandDidWrite.Store(true)

	if jsonOutput {
		results := make([]map[string]interface{}, 0, len(resolvedIDs)*len(labels))
		for _, issueID := range resolvedIDs {
			for _, label := range labels {
				results = append(results, map[string]interface{}{
					"status":   operation,
					"issue_id": issueID,
					"label":    label,
				})
			}
		}
		return outputJSON(results)
	}

	verb, prep := "Added", "to"
	if operation == "removed" {
		verb, prep = "Removed", "from"
	}
	noun := "label"
	if len(labels) > 1 {
		noun = "labels"
	}
	for _, issueID := range resolvedIDs {
		fmt.Printf("%s %s %s '%s' %s %s\n", ui.RenderPass("✓"), verb, noun, labelDesc, prep, issueID)
	}
	return nil
}

func runLabelListProxiedServer(ctx context.Context, args []string) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	issue, isWisp, err := proxiedGetIssueOrWisp(ctx, uw, args[0])
	if err != nil {
		return HandleErrorRespectJSON("resolving %s: %v", args[0], err)
	}
	if issue == nil {
		return HandleErrorRespectJSON("resolving %s: not found", args[0])
	}
	issueID := issue.ID

	var labels []string
	if isWisp {
		labels, err = uw.LabelUseCase().GetWispLabels(ctx, issueID)
	} else {
		labels, err = uw.LabelUseCase().GetLabels(ctx, issueID)
	}
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		if labels == nil {
			labels = []string{}
		}
		return outputJSON(labels)
	}
	if len(labels) == 0 {
		fmt.Printf("\n%s has no labels\n", issueID)
		return nil
	}
	fmt.Printf("\n%s Labels for %s:\n", ui.RenderAccent("🏷"), issueID)
	for _, label := range labels {
		fmt.Printf("  - %s\n", label)
	}
	fmt.Println()
	return nil
}

func runLabelListAllProxiedServer(ctx context.Context) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	var permIDs, wispIDs []string
	for _, issue := range page.Items {
		if issue.Ephemeral {
			wispIDs = append(wispIDs, issue.ID)
		} else {
			permIDs = append(permIDs, issue.ID)
		}
	}

	labelCounts := make(map[string]int)
	accumulate := func(byIssue map[string][]string) {
		for _, labels := range byIssue {
			for _, label := range labels {
				labelCounts[label]++
			}
		}
	}
	if len(permIDs) > 0 {
		byIssue, err := uw.LabelUseCase().GetLabelsForIssues(ctx, permIDs)
		if err != nil {
			return HandleErrorRespectJSON("getting labels: %v", err)
		}
		accumulate(byIssue)
	}
	if len(wispIDs) > 0 {
		byWisp, err := uw.LabelUseCase().GetLabelsForWisps(ctx, wispIDs)
		if err != nil {
			return HandleErrorRespectJSON("getting labels: %v", err)
		}
		accumulate(byWisp)
	}

	type labelInfo struct {
		Label string `json:"label"`
		Count int    `json:"count"`
	}
	if len(labelCounts) == 0 {
		if jsonOutput {
			return outputJSON([]labelInfo{})
		}
		fmt.Println("\nNo labels found in database")
		return nil
	}

	labels := make([]string, 0, len(labelCounts))
	for label := range labelCounts {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	if jsonOutput {
		result := make([]labelInfo, 0, len(labels))
		for _, label := range labels {
			result = append(result, labelInfo{Label: label, Count: labelCounts[label]})
		}
		return outputJSON(result)
	}
	fmt.Printf("\n%s All labels (%d unique):\n", ui.RenderAccent("🏷"), len(labels))
	maxLen := 0
	for _, label := range labels {
		if len(label) > maxLen {
			maxLen = len(label)
		}
	}
	for _, label := range labels {
		padding := strings.Repeat(" ", maxLen-len(label))
		fmt.Printf("  %s%s  (%d issues)\n", label, padding, labelCounts[label])
	}
	fmt.Println()
	return nil
}

func runLabelPropagateProxiedServer(ctx context.Context, args []string) error {
	label := strings.TrimSpace(args[1])
	if label == "" {
		return HandleErrorRespectJSON("label cannot be empty")
	}
	if strings.HasPrefix(label, "provides:") {
		return HandleErrorRespectJSON("'provides:' labels are reserved for cross-project capabilities. Hint: use 'bd ship %s' instead", strings.TrimPrefix(label, "provides:"))
	}
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	var (
		children []*types.Issue
		parentID string
	)
	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		parent, _ := proxiedResolveIssueOrWisp(ctx, uw, args[0])
		if parent == nil {
			return "", fmt.Errorf("resolving parent %q: not found", args[0])
		}
		parentID = parent.ID

		page, err := uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{ParentID: &parentID})
		if err != nil {
			return "", fmt.Errorf("searching children of %s: %w", parentID, err)
		}
		children = page.Items
		if len(children) == 0 {
			return "", nil
		}
		for _, child := range children {
			var e error
			if child.Ephemeral {
				e = uw.LabelUseCase().AddWispLabel(ctx, child.ID, label, actor)
			} else {
				e = uw.LabelUseCase().AddLabel(ctx, child.ID, label, actor)
			}
			if e != nil {
				return "", fmt.Errorf("add label '%s' on %s: %w", label, child.ID, e)
			}
		}
		return fmt.Sprintf("bd: propagate label '%s' from %s to %d children", label, parentID, len(children)), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("label propagate: %v", err)
	}

	if len(children) == 0 {
		if jsonOutput {
			return outputJSON([]map[string]interface{}{})
		}
		fmt.Printf("No children found for %s\n", parentID)
		return nil
	}
	commandDidWrite.Store(true)

	if jsonOutput {
		results := make([]map[string]interface{}, 0, len(children))
		for _, child := range children {
			results = append(results, map[string]interface{}{
				"status":   "propagated",
				"issue_id": child.ID,
				"label":    label,
			})
		}
		return outputJSON(results)
	}
	for _, child := range children {
		fmt.Printf("%s Propagated label '%s' to %s\n", ui.RenderPass("✓"), label, child.ID)
	}
	return nil
}
