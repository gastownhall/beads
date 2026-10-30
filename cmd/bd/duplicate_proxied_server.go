package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

func runDuplicateProxiedServer(_ *cobra.Command, args []string) error {
	CheckReadonly("duplicate")

	evt := metrics.NewCommandEvent("duplicate")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	actor := getActor()
	ids, err := uow.RunTxResult(rootCtx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) ([2]string, string, error) {
		var ids [2]string
		reader := uowMolReader{uw: uw}
		for i, input := range []string{args[0], duplicateOf} {
			id, err := utils.ResolvePartialID(ctx, reader, input)
			if err != nil {
				return ids, "", fmt.Errorf("failed to resolve %s: %w", input, err)
			}
			ids[i] = id
		}
		if ids[0] == ids[1] {
			return ids, "", fmt.Errorf("cannot mark an issue as duplicate of itself")
		}
		if _, err := uw.IssueUseCase().GetIssue(ctx, ids[1]); err != nil {
			return ids, "", fmt.Errorf("canonical issue not found: %s: %w", ids[1], err)
		}
		dep := &types.Dependency{IssueID: ids[0], DependsOnID: ids[1], Type: types.DepDuplicates}
		if err := uw.DependencyUseCase().AddDependency(ctx, dep, actor); err != nil {
			return ids, "", fmt.Errorf("failed to add duplicate link: %w", err)
		}
		if _, err := uw.IssueUseCase().CloseIssueChecked(ctx, ids[0], domain.CloseIssueParams{}, actor, false); err != nil {
			return ids, "", fmt.Errorf("failed to close duplicate: %w", err)
		}
		return ids, "bd: duplicate " + ids[0] + " of " + ids[1], nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	commandDidWrite.Store(true)
	if isJSONOutput() {
		return outputJSON(map[string]interface{}{"duplicate": ids[0], "canonical": ids[1], "status": "closed"})
	}
	fmt.Printf("%s Marked %s as duplicate of %s (closed)\n", ui.RenderPass("✓"), ids[0], ids[1])
	return nil
}
