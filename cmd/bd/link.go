package main

import (
	"fmt"

	"github.com/spf13/cobra"
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
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("link")

		id1 := args[0]
		id2 := args[1]
		depType, _ := cmd.Flags().GetString("type")

		ctx := rootCtx

		// Resolve partial IDs with routing support
		fromID, fromStore, fromCleanup, err := resolveIDWithRouting(ctx, store, id1)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}
		defer fromCleanup()

		toID, _, toCleanup, err := resolveIDWithRouting(ctx, store, id2)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}
		defer toCleanup()

		// Check for child→parent dependency anti-pattern
		if isChildOf(fromID, toID) {
			FatalErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", fromID, toID)
		}

		// Validate dependency type
		dt := types.DependencyType(depType)
		if !dt.IsValid() {
			FatalErrorRespectJSON("invalid dependency type %q: must be non-empty and at most 50 characters", depType)
		}

		dep := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        dt,
		}

		if err := fromStore.AddDependency(ctx, dep, actor); err != nil {
			FatalErrorRespectJSON("%v", err)
		}

		// Check for cycles after adding dependency
		warnIfCyclesExist(fromStore)

		if isEmbeddedMode() && fromStore != nil {
			if _, err := fromStore.CommitPending(ctx, actor); err != nil {
				FatalErrorRespectJSON("failed to commit: %v", err)
			}
		}

		SetLastTouchedID(fromID)

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":        "added",
				"issue_id":      fromID,
				"depends_on_id": toID,
				"type":          depType,
			})
		} else {
			fmt.Printf("%s Linked: %s depends on %s (%s)\n",
				ui.RenderPass("✓"), formatFeedbackIDParen(fromID, lookupTitle(fromID)), formatFeedbackIDParen(toID, lookupTitle(toID)), depType)
		}
	},
}

func init() {
	linkCmd.Flags().StringP("type", "t", "blocks", "Dependency type (blocks|tracks|related|parent-child|discovered-from)")
	linkCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(linkCmd)
}
