package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func runGateListProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	allFlag, _ := cmd.Flags().GetBool("all")
	limit, _ := cmd.Flags().GetInt("limit")

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	if len(args) == 1 {
		target, isWisp, err := proxiedGetIssueOrWisp(ctx, uw, args[0])
		if err != nil || target == nil {
			return HandleErrorRespectJSON("issue not found: %s", args[0])
		}
		var metas []*types.IssueWithDependencyMetadata
		if isWisp {
			metas, err = uw.DependencyUseCase().ListWispWithIssueMetadata(ctx, target.ID, domain.DepListFilter{Direction: domain.DepDirectionOut})
		} else {
			metas, err = uw.DependencyUseCase().ListWithIssueMetadata(ctx, target.ID, domain.DepListFilter{Direction: domain.DepDirectionOut})
		}
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		deps := make([]*types.Issue, 0, len(metas))
		for _, m := range metas {
			if m != nil {
				deps = append(deps, &m.Issue)
			}
		}
		gates := filterIssueGates(deps, allFlag, limit)
		if jsonOutput {
			return outputJSON(gates)
		}
		displayGates(gates, allFlag)
		return nil
	}

	gateType := types.IssueType("gate")
	filter := types.IssueFilter{
		IssueType: &gateType,
		Limit:     limit,
	}
	if !allFlag {
		filter.ExcludeStatus = []types.Status{types.StatusClosed}
	}
	page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if jsonOutput {
		return outputJSON(page.Items)
	}
	displayGates(page.Items, allFlag)
	return nil
}
