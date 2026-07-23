package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var linkCmd = &cobra.Command{
	Use:     "link <id1> <id2>",
	GroupID: "issues",
	Short:   "Link two issues with a dependency",
	Long: `Link two issues with a dependency.

Shorthand for 'bd dep add <id1> <id2>'. By default creates a "blocks"
dependency (id2 blocks id1). Use --type to specify a different relationship.

Examples:
  bd link bd-123 bd-456                    # bd-456 blocks bd-123
  bd link bd-123 bd-456 --type related     # bd-123 related to bd-456
  bd link bd-123 bd-456 --type parent-child`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("link")

		evt := metrics.NewCommandEvent("link")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLinkProxiedServer(cmd, rootCtx, args)
		}

		id1 := args[0]
		id2 := args[1]
		depType, _ := cmd.Flags().GetString("type")

		ctx := rootCtx

		// Resolve partial IDs with routing support. The source issue's store
		// is mutated by AddDependency below, so resolve it write-intent
		// (#4141); the dependency target is only resolved by ID and stays
		// read-only (bd-6dnrw.32, GH#3231).
		fromID, fromStore, fromCleanup, err := resolveIDForMutation(ctx, store, id1)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		defer fromCleanup()

		toID, _, toCleanup, err := resolveIDWithRouting(ctx, store, id2)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		defer toCleanup()

		if isChildOf(fromID, toID) {
			return HandleErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", fromID, toID)
		}

		dt := types.DependencyType(depType)
		if !dt.IsValid() {
			return HandleErrorRespectJSON("invalid dependency type %q: must be non-empty and at most 50 characters", depType)
		}

		dep := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        dt,
		}

		if err := fromStore.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		warnIfCyclesExist(fromStore)

		if err := commitPendingIfEmbedded(ctx, fromStore, actor, doltAutoCommitParams{
			Command:  "link",
			IssueIDs: []string{fromID, toID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(fromID)

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":        "added",
				"issue_id":      fromID,
				"depends_on_id": toID,
				"type":          depType,
			})
		}
		fmt.Printf("%s Linked: %s depends on %s (%s)\n",
			ui.RenderPass("✓"), formatFeedbackIDParen(fromID, lookupTitle(fromID)), formatFeedbackIDParen(toID, lookupTitle(toID)), depType)
		return nil
	},
}

func init() {
	linkCmd.Flags().StringP("type", "t", "blocks", "Dependency type (blocks|tracks|related|parent-child|discovered-from)")
	linkCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(linkCmd)
}
