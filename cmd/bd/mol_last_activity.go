package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/utils"
)

var molLastActivityCmd = &cobra.Command{
	Use:   "last-activity <molecule-id>",
	Short: "Show last activity timestamp for a molecule",
	Long: `Show the most recent activity timestamp for a molecule.

Returns the timestamp of the most recent change to any step in the molecule,
making it easy to detect stale or stuck molecules.

Activity sources:
  step_closed      - A step was closed
  step_updated     - A step was updated (claimed, edited, etc.)
  molecule_updated - The molecule root itself was updated

Examples:
  bd mol last-activity hq-wisp-0laki
  bd mol last-activity hq-wisp-0laki --json`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		if store == nil {
			FatalError("no database connection")
		}

		moleculeID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			FatalError("molecule '%s' not found", args[0])
		}

		activity, err := store.GetMoleculeLastActivity(ctx, moleculeID)
		if err != nil {
			FatalError("%v", err)
		}

		if jsonOutput {
			outputJSON(activity)
			return
		}

		fmt.Println(activity.LastActivity.UTC().Format("2006-01-02T15:04:05Z"))
	},
}

func init() {
	molCmd.AddCommand(molLastActivityCmd)
}
