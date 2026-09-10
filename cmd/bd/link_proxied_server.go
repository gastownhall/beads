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

	dt := types.DependencyType(depType)
	if isDisallowedHierarchicalDependency(id1, id2, dt) {
		return HandleErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", id1, id2)
	}

	if !dt.IsValid() {
		return HandleErrorRespectJSON("invalid dependency type %q: must be non-empty and at most %d characters", depType, types.MaxDependencyTypeLen)
	}

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	if err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		dep := &types.Dependency{IssueID: id1, DependsOnID: id2, Type: dt}
		// Source-routed, like the direct twin's store.AddDependencyWithOptions:
		// `bd link` takes whatever id the caller names, and a wisp source has no
		// row in the issues plane for the edge to hang off.
		if _, err := uw.DependencyUseCase().AddDependencies(ctx, []*types.Dependency{dep}, actor, domain.BulkAddDepsOpts{}); err != nil {
			return "", err
		}
		return fmt.Sprintf("bd: link %s %s", id1, id2), nil
	}); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	// The sweep and the titles come AFTER the write commits, for the reason
	// depEdgeFeedback gives: a cycle warning computed inside a transaction that
	// has not committed describes a graph nobody else can see. This route used
	// to run both inside the write.
	res := depEdgeFeedback(ctx, id1, id2, true)

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
