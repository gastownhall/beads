package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

// resolveLabelTargetProxied is `bd label`'s exact-id lookup on the
// proxied-server route: the same issue-then-wisp resolution
// workapi.GetIssueOrWisp gives every other proxied front door, with not-found
// normalized to the shape the direct route's resolver produces.
//
// It is what is LEFT of this route's label plumbing. The four-way
// wisp-by-operation switch that used to live here, and the read that used to
// ask a label use case for one issue's labels, are gone: both routes now end at
// issueops.Lifecycle and issueops.Reader, which resolve the plane themselves
// inside their own transaction. Only the resolution stays, because the roles
// take an exact id by contract.
func resolveLabelTargetProxied(ctx context.Context, id string) (string, error) {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return "", err
	}
	defer uw.Close(ctx)

	issue, _, rerr := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
	if errors.Is(rerr, storage.ErrNotFound) {
		return "", errors.New("not found")
	}
	if rerr != nil {
		return "", rerr
	}
	return issue.ID, nil
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
	// THE QUESTION HAS NO ROLE, which is the reason ga-26w10 left `list-all`
	// behind and the reason it is still here: no issueops surface answers
	// "every distinct label in this workspace, with a count". Reader.Get is one
	// detail view per call, so asking it would be one round trip per bead to
	// build a histogram. A label-vocabulary role is the follow-up
	// (ga-2ltro.12).
	if len(permIDs) > 0 {
		byIssue, err := uw.LabelUseCase().GetLabelsForIssues(ctx, permIDs) //nolint:forbidigo // label histogram; no role answers the workspace's label vocabulary
		if err != nil {
			return HandleErrorRespectJSON("getting labels: %v", err)
		}
		accumulate(byIssue)
	}
	if len(wispIDs) > 0 {
		byWisp, err := uw.LabelUseCase().GetLabelsForWisps(ctx, wispIDs) //nolint:forbidigo // label histogram; see GetLabelsForIssues above
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
		parent, _, rerr := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), args[0])
		if errors.Is(rerr, storage.ErrNotFound) {
			return "", fmt.Errorf("resolving parent %q: not found", args[0])
		}
		if rerr != nil {
			return "", fmt.Errorf("resolving parent %q: %w", args[0], rerr)
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
		// THE ONE SURVIVING WISP SWITCH, and it is named rather than hidden.
		// This is the same four-way shape ga-26w10 deleted from `bd label` and
		// ga-2ltro.12 deleted from `bd tag`, `bd set-state` and the molecule
		// port — the branch a front door can get backwards and put a wisp's
		// label in the durable table. It survives because propagate is a search
		// plus a fan-out that must land as ONE transaction over N children, and
		// a per-child Lifecycle.Update is N transactions: the atomicity this
		// command has today would be the price of the migration. The shape that
		// keeps both is issueops.BatchApplier.ApplyBatch with one ItemUpdate per
		// child carrying this same label patch, and it is blocked on a cmd/bd
		// accessor for that role. That is the follow-up this waiver names
		// (ga-2ltro.12); it is the last one on this list that WRITES.
		for _, child := range children {
			var e error
			if child.Ephemeral {
				e = uw.LabelUseCase().AddWispLabel(ctx, child.ID, label, actor) //nolint:forbidigo // atomic N-child fan-out; awaits a BatchApplier accessor
			} else {
				e = uw.LabelUseCase().AddLabel(ctx, child.ID, label, actor) //nolint:forbidigo // atomic N-child fan-out; awaits a BatchApplier accessor
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
