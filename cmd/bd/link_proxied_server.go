package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

func runLinkProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	id1 := args[0]
	id2 := args[1]
	depType, _ := cmd.Flags().GetString("type")

	if isChildOf(id1, id2) {
		return HandleErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", id1, id2)
	}

	dt := types.DependencyType(depType)
	if !dt.IsValid() {
		return HandleErrorRespectJSON("invalid dependency type %q: must be non-empty and at most 50 characters", depType)
	}

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (depAddResult, string, error) {
		dep := &types.Dependency{IssueID: id1, DependsOnID: id2, Type: dt}
		if _, err := uw.DependencyUseCase().AddDependencies(ctx, []*types.Dependency{dep}, actor, domain.BulkAddDepsOpts{}); err != nil {
			return depAddResult{}, "", err
		}
		cycles, cycleErr := uw.DependencyUseCase().DetectCycles(ctx)
		return depAddResult{
			fromTitle: proxiedLookupTitle(ctx, uw, id1),
			toTitle:   proxiedLookupTitle(ctx, uw, id2),
			cycles:    cycles,
			cycleErr:  cycleErr,
		}, fmt.Sprintf("bd: link %s %s", id1, id2), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	printCycleDetectionError(res.cycleErr)
	printCycleWarnings(res.cycles)

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"status":        "added",
			"issue_id":      id1,
			"depends_on_id": id2,
			"type":          depType,
		})
	}
	fmt.Printf("%s Linked: %s depends on %s (%s)\n",
		ui.RenderPass("✓"),
		formatFeedbackIDParen(id1, res.fromTitle),
		formatFeedbackIDParen(id2, res.toTitle),
		depType)
	return nil
}
